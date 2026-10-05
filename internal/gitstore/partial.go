package gitstore

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	deployment "github.com/context4ai/sourcegraph/config"
	"github.com/context4ai/sourcegraph/internal/contract"
)

func remoteURL(repo string) string {
	return strings.TrimRight(deployment.Load().CodeHost, "/") + "/" + repo + ".git"
}
func fetchConfiguration(o FetchOptions) []string {
	return []string{"-c", "credential.helper=", "-c", "credential.helper=" + o.CredentialHelper, "-c", "credential.useHttpPath=true", "-c", "http.followRedirects=false", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.version=2", "-c", "gc.auto=0"}
}

// requirePartialClone rejects unsupported servers before any pack is fetched.
// Git otherwise silently accepts --filter while downloading the entire history.
func (s *Store) requirePartialClone(ctx context.Context, p, remote string, o FetchOptions) error {
	args := append(fetchConfiguration(o), "ls-remote", "--exit-code", remote, "HEAD")
	c := s.command(ctx, p, args...)
	c.Env = append(c.Env, "SOURCEGRAPH_GIT_TOKEN="+o.Token, "GIT_TRACE_PACKET=1")
	c.Stdout = io.Discard
	trace := &prefixTrace{limit: 1 << 20}
	c.Stderr = trace
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return remoteFailure(trace.String(), "GIT_FETCH_FAILED", "Cannot inspect remote partial-clone support.")
	}
	supported := false
	for _, line := range strings.Split(trace.String(), "\n") {
		if i := strings.Index(line, "fetch="); i >= 0 {
			for _, cap := range strings.Fields(line[i+len("fetch="):]) {
				if cap == "filter" {
					supported = true
				}
			}
		}
	}
	if !supported {
		return contract.Fail("PARTIAL_CLONE_UNSUPPORTED", "The Git server does not advertise partial clone; monorepo indexing requires blob filtering.", 422)
	}
	return nil
}

// HydratePaths downloads only objects used by this immutable selected snapshot.
// Queries always set GIT_NO_LAZY_FETCH; network hydration belongs to workers.
func (s *Store) HydratePaths(ctx context.Context, repo, commit string, paths []string, o FetchOptions) error {
	if s.group == "" {
		return errors.New("path hydration requires a group store")
	}
	if len(paths) == 0 {
		return contract.Fail("INVALID_PATHS", "A monorepo group must contain at least one path.", 400)
	}
	if !filepath.IsAbs(o.CredentialHelper) || strings.ContainsAny(o.CredentialHelper, " \t\r\n\"'\\") || o.Token == "" {
		return contract.Fail("GIT_CREDENTIAL_UNAVAILABLE", "Git credential is not configured.", 503)
	}
	if o.Timeout <= 0 || o.Timeout > 30*time.Minute {
		o.Timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	entries, err := s.SnapshotEntries(ctx, repo, commit, paths)
	if err != nil {
		return err
	}
	return s.HydrateEntries(ctx, repo, commit, entries, o)
}

// HydrateEntries is worker-only; callers bound expansion before fetching targets.
func (s *Store) HydrateEntries(ctx context.Context, repo, commit string, entries []Entry, o FetchOptions) error {
	if s.group == "" {
		return errors.New("object hydration requires a group store")
	}
	if !filepath.IsAbs(o.CredentialHelper) || strings.ContainsAny(o.CredentialHelper, " \t\r\n\"'\\") || o.Token == "" {
		return contract.Fail("GIT_CREDENTIAL_UNAVAILABLE", "Git credential is not configured.", 503)
	}
	if o.Timeout <= 0 || o.Timeout > 30*time.Minute {
		o.Timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	sizes, err := s.BlobSizes(ctx, repo, commit, entries)
	if err != nil {
		return err
	}
	p, err := s.repoPath(repo)
	if err != nil {
		return err
	}
	missing := []string{}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Kind != "file" && entry.Kind != "symlink" {
			continue
		}
		if _, ok := sizes[entry.BlobOID]; !ok && !seen[entry.BlobOID] {
			missing = append(missing, entry.BlobOID)
			seen[entry.BlobOID] = true
		}
	}
	// Bound each request and check free space between batches. Direct blob wants
	// have no ancestor closure, so they never materialize unrelated directories.
	for start := 0; start < len(missing); start += 256 {
		if o.CheckGrowth != nil {
			if err = o.CheckGrowth(); err != nil {
				return err
			}
		}
		if err = checkFree(s.root, o.MinFreeBytes); err != nil {
			return err
		}
		end := start + 256
		if end > len(missing) {
			end = len(missing)
		}
		args := append(fetchConfiguration(o), "fetch", "--no-tags", "--no-write-fetch-head", "--stdin", remoteURL(repo))
		batchCtx, batchCancel := context.WithCancel(ctx)
		c := s.command(batchCtx, p, args...)
		c.Env = append(c.Env, "SOURCEGRAPH_GIT_TOKEN="+o.Token)
		c.Stdin = strings.NewReader(strings.Join(missing[start:end], "\n") + "\n")
		c.Stdout = io.Discard
		diagnostic := &prefixTrace{limit: 64 << 10}
		c.Stderr = diagnostic
		done := make(chan struct{})
		var capacity error
		go func() {
			defer close(done)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-batchCtx.Done():
					return
				case <-ticker.C:
					if o.CheckGrowth != nil {
						if e := o.CheckGrowth(); e != nil {
							capacity = e
							batchCancel()
							return
						}
					}
					if e := checkFree(s.root, o.MinFreeBytes); e != nil {
						capacity = e
						batchCancel()
						return
					}
				}
			}
		}()
		err = c.Run()
		// A per-batch monitor must stop without cancelling subsequent batches.
		batchCancel()
		<-done
		if capacity != nil {
			return capacity
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return remoteFailure(diagnostic.String(), "GIT_HYDRATION_FAILED", "Cannot download selected path objects; the server must allow reachable object requests.")
		}
	}
	if o.CheckGrowth != nil {
		if err = o.CheckGrowth(); err != nil {
			return err
		}
	}
	return checkFree(s.root, o.MinFreeBytes)
}

// Capability packets precede refs. Discard excess diagnostics without killing Git.
type prefixTrace struct {
	data  []byte
	limit int
}

func (w *prefixTrace) Write(p []byte) (int, error) {
	n := len(p)
	left := w.limit - len(w.data)
	if left > n {
		left = n
	}
	w.data = append(w.data, p[:left]...)
	return n, nil
}
func (w *prefixTrace) String() string { return string(w.data) }
