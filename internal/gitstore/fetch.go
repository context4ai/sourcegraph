package gitstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
)

type FetchOptions struct {
	CheckGrowth      func() error
	DefaultBranch    bool
	CredentialHelper string
	Token            string
	Branches         []string
	MinFreeBytes     uint64
	Timeout          time.Duration
}
type Commit struct {
	SHA           string
	CommitterTime time.Time
}

// Fetch receives an already authorized canonical repository. It never follows
// redirects, initializes submodules, checks out files or runs repository hooks.
func (s *Store) Fetch(ctx context.Context, repo string, o FetchOptions) error {
	_, err := s.FetchSelection(ctx, repo, o)
	return err
}

func (s *Store) FetchSelection(ctx context.Context, repo string, o FetchOptions) (string, error) {
	var selected string
	err := s.fetch(ctx, repo, o, &selected)
	return selected, err
}

func (s *Store) fetch(ctx context.Context, repo string, o FetchOptions, selected *string) error {
	if err := ValidateRepo(repo); err != nil {
		return err
	}
	slots := len(o.Branches)
	if o.DefaultBranch {
		slots++
	}
	if slots < 1 || slots > 8 {
		return contract.Fail("INVALID_BRANCHES", "Choose 1..8 exact branches.", 400)
	}
	for _, b := range o.Branches {
		if ValidateRevision(b) != nil || strings.HasPrefix(b, "refs/") || fullSHA.MatchString(b) {
			return contract.Fail("INVALID_BRANCH", "Use a short branch name.", 400)
		}
	}
	if !filepath.IsAbs(o.CredentialHelper) || strings.ContainsAny(o.CredentialHelper, " \t\r\n\"'\\") {
		return contract.Fail("GIT_CREDENTIAL_UNAVAILABLE", "Git credential is not configured.", 503)
	}
	if o.Timeout <= 0 || o.Timeout > 30*time.Minute {
		o.Timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	p, pathErr := s.repositoryDir(repo)
	if pathErr != nil {
		return pathErr
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil || real != filepath.Dir(p) {
		return errors.New("unsafe repository parent")
	}
	fd, err := syscall.Open(filepath.Join(real, "fetch.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	if err = checkFree(s.root, o.MinFreeBytes); err != nil {
		return err
	}
	if _, err = os.Lstat(p); os.IsNotExist(err) {
		// A failed fetch leaves a valid empty bare repository for the next attempt.
		if _, err = s.run(ctx, p, 1024, "init", "--bare", p); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err = s.repoPath(repo); err != nil {
		return err
	}
	remote := remoteURL(repo)
	if o.DefaultBranch {
		branch, err := s.discoverDefaultBranch(ctx, p, remote, o)
		if err != nil {
			return err
		}
		*selected = branch
		found := false
		for _, b := range o.Branches {
			if b == branch {
				found = true
			}
		}
		if !found {
			o.Branches = append(append([]string(nil), o.Branches...), branch)
		}
	}
	args := fetchConfiguration(o)
	if s.group != "" {
		if err := s.requirePartialClone(ctx, p, remote, o); err != nil {
			return err
		}
		for _, setting := range [][]string{{"remote.origin.url", remote}, {"remote.origin.promisor", "true"}, {"remote.origin.partialclonefilter", "blob:none"}} {
			if _, err = s.run(ctx, p, 1024, "config", setting[0], setting[1]); err != nil {
				return err
			}
		}
	}
	args = append(args, "fetch", "--no-tags", "--force")
	if s.group != "" {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, remote)
	for _, b := range o.Branches {
		args = append(args, "+refs/heads/"+b+":refs/heads/"+b)
	}
	command := s.command(ctx, p, args...)
	command.Env = append(command.Env, "SOURCEGRAPH_GIT_TOKEN="+o.Token)
	command.Stdout = io.Discard
	diagnostic := &prefixTrace{limit: 64 << 10}
	command.Stderr = diagnostic
	done := make(chan struct{})
	var capacity error
	go func() {
		defer close(done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if e := checkFree(s.root, o.MinFreeBytes); e != nil {
					capacity = e
					cancel()
					return
				}
			}
		}
	}()
	err = command.Run()
	contextErr := ctx.Err()
	cancel()
	<-done
	if capacity != nil {
		return capacity
	}
	if contextErr != nil {
		return contextErr
	}
	if err != nil {
		return remoteFailure(diagnostic.String(), "GIT_FETCH_FAILED", "Git fetch failed; check access and connectivity.")
	}
	return nil
}
func checkFree(root string, min uint64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return err
	}
	if uint64(st.Bavail)*uint64(st.Bsize) < min {
		return contract.Fail("INSUFFICIENT_STORAGE", "Available disk space is below the preparation reserve.", 503)
	}
	return nil
}

// History returns the retained first-parent commits plus the tip. More than the
// explicit cap is an error, never a silently truncated policy window.
func (s *Store) History(ctx context.Context, repo, branch string, days, count int, now time.Time) ([]Commit, error) {
	if days < 0 || days > 365 || count < 0 || count > 4096 || (days == 0) == (count == 0) {
		return nil, contract.Fail("INVALID_RETENTION", "Choose days 1..365 or count 1..4096.", 400)
	}
	sha, err := s.Resolve(ctx, repo, "refs/heads/"+branch)
	if err != nil {
		return nil, err
	}
	p, err := s.repoPath(repo)
	if err != nil {
		return nil, err
	}
	// Read up to one over the configured service ceiling; history outside the
	// window can remain reachable in Git without being query-eligible.
	limit := 250001
	if count > 0 {
		limit = count
	}
	args := []string{"log", "--first-parent", "--format=%H %ct", "--max-count=" + strconv.Itoa(limit), sha, "--"}
	data, err := s.run(ctx, p, 16<<20, args...)
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	result := []Commit{}
	for i, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(row)
		if len(fields) != 2 || !fullSHA.MatchString(fields[0]) {
			return nil, fmt.Errorf("invalid commit metadata")
		}
		n, e := strconv.ParseInt(fields[1], 10, 64)
		if e != nil {
			return nil, e
		}
		ts := time.Unix(n, 0).UTC()
		if count > 0 || i == 0 || !ts.Before(cutoff) {
			result = append(result, Commit{fields[0], ts})
		}
	}
	if days > 0 && (len(strings.Split(strings.TrimSpace(string(data)), "\n")) == limit || len(result) > 4096) {
		return nil, contract.Fail("HISTORY_SCAN_LIMIT", "History exceeds the bounded scan; narrow the retention policy or use a count.", 422)
	}
	return result, nil
}
