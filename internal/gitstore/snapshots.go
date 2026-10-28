package gitstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Snapshot reads one commit without traversing its parents.
func (s *Store) Snapshot(ctx context.Context, repo, revision string) (Commit, error) {
	sha, err := s.Resolve(ctx, repo, revision)
	if err != nil {
		return Commit{}, err
	}
	p, err := s.repoPath(repo)
	if err != nil {
		return Commit{}, err
	}
	data, err := s.run(ctx, p, 128, "show", "-s", "--format=%ct", sha, "--")
	if err != nil {
		return Commit{}, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return Commit{SHA: sha, CommitterTime: time.Unix(n, 0).UTC()}, err
}

// RetainSnapshots cuts parent traversal at each retained commit and reclaims
// unreachable objects. The caller must drain readers of expired snapshots and exclude writers.
// References are installed before changing boundaries; interruption can leak
// bytes but cannot remove a retained snapshot. Re-running finishes collection.
func (s *Store) RetainSnapshots(ctx context.Context, repo string, commits []string, checks ...func() error) error {
	p, err := s.repoPath(repo)
	if err != nil {
		return err
	}
	if len(commits) == 0 || len(commits) > 4096 {
		return errors.New("invalid snapshot retention set")
	}
	sortedCommits := append([]string(nil), commits...)
	sort.Strings(sortedCommits)
	marker := []byte(strings.Join(sortedCommits, "\n") + "\n")
	markerPath := filepath.Join(p, "snapshot-retention")
	previous, previousErr := os.ReadFile(markerPath)
	previousInfo, statErr := os.Stat(markerPath)
	// Adding shallow snapshots cannot make existing packs unreachable. Avoid
	// rewriting the entire repository on every branch update; still collect
	// daily to recover objects left behind by interrupted fetches.
	needsPack := previousErr != nil || statErr != nil
	if !needsPack {
		needsPack = time.Since(previousInfo.ModTime()) >= 24*time.Hour
	}
	currentSet := map[string]bool{}
	for _, sha := range commits {
		currentSet[sha] = true
	}
	for _, sha := range strings.Fields(string(previous)) {
		if !currentSet[sha] {
			needsPack = true
		}
	}
	keep := map[string]bool{}
	for _, sha := range commits {
		if !fullSHA.MatchString(sha) {
			return errors.New("invalid retained commit")
		}
		if _, err := s.Snapshot(ctx, repo, sha); err != nil {
			return err
		}
		keep[sha] = true
	}
	shas := make([]string, 0, len(keep))
	for sha := range keep {
		if _, err = s.run(ctx, p, 1024, "update-ref", "refs/snapshots/"+sha, sha); err != nil {
			return err
		}
		shas = append(shas, sha)
	}
	sort.Strings(shas)
	// Every kept commit is a root. Parent commits are not needed for snapshot reads.
	tmp := filepath.Join(p, "shallow.snapshots")
	if err = os.WriteFile(tmp, []byte(strings.Join(shas, "\n")+"\n"), 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(p, "shallow")); err != nil {
		return err
	}
	refs, err := s.run(ctx, p, 2<<20, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(refs)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return errors.New("invalid reference")
		}
		if !keep[fields[1]] {
			if _, err = s.run(ctx, p, 1024, "update-ref", "-d", fields[0]); err != nil {
				return err
			}
		}
	}
	if _, err = s.run(ctx, p, 1024, "reflog", "expire", "--expire=now", "--all"); err != nil {
		return err
	}
	if !needsPack {
		if err = os.WriteFile(markerPath, marker, 0600); err != nil {
			return err
		}
		return os.Chtimes(markerPath, previousInfo.ModTime(), previousInfo.ModTime())
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	repackCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Promisor repack deliberately retains unreachable remote objects. Build a
	// reachable-only replacement pack instead, publishing it before removing old packs.
	oldPacks, err := filepath.Glob(filepath.Join(p, "objects", "pack", "pack-*.pack"))
	if err != nil {
		return err
	}
	command := s.command(repackCtx, p, "pack-objects", "--revs", "--missing=allow-promisor", "--non-empty", filepath.Join(p, "objects", "pack", "pack"))
	command.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	output := &limitWriter{limit: 1024}
	command.Stdout = output
	command.Stderr = io.Discard
	done := make(chan struct{})
	finished := make(chan struct{})
	var capacity error
	go func() {
		defer close(finished)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-repackCtx.Done():
				return
			case <-ticker.C:
				for _, check := range checks {
					if e := check(); e != nil {
						capacity = e
						cancel()
						return
					}
				}
			}
		}
	}()
	err = command.Run()
	close(done)
	<-finished
	if capacity != nil {
		return capacity
	}
	if err != nil {
		return err
	}
	packID := strings.TrimSpace(output.String())
	if !fullSHA.MatchString(packID) {
		return errors.New("invalid replacement pack identity")
	}
	base := filepath.Join(p, "objects", "pack", "pack-"+packID)
	if s.group != "" {
		if err = os.WriteFile(base+".promisor", nil, 0600); err != nil {
			return err
		}
	}
	// A stale multi-pack index must not refer to packs about to be removed.
	if err = os.Remove(filepath.Join(p, "objects", "pack", "multi-pack-index")); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, old := range oldPacks {
		oldBase := strings.TrimSuffix(old, ".pack")
		if oldBase == base {
			continue
		}
		for _, ext := range []string{".pack", ".idx", ".rev", ".bitmap", ".promisor", ".mtimes"} {
			if err = os.Remove(oldBase + ext); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	if _, err = s.run(ctx, p, 4096, "prune", "--expire=now"); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p, "snapshot-retention"), marker, 0600)
}

// CheckFree enforces the global preparation reserve during snapshot collection.
func (s *Store) CheckFree(min uint64) error { return checkFree(s.root, min) }
