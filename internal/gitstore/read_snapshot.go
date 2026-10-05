package gitstore

import (
	"context"
	"fmt"
	"io"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// Reader retains a checked immutable commit and scope. The service must hold its
// storage lock for the reader's lifetime, just as it does for individual reads.
type Reader struct {
	store  *Store
	path   string
	commit string
}

func (s *Store) OpenReader(ctx context.Context, repo, commit string) (*Reader, error) {
	p, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return nil, err
	}
	return &Reader{store: s, path: p, commit: commit}, nil
}

func ValidateReadRange(path string, start, end int) error {
	if err := ValidatePath(path, false); err != nil {
		return err
	}
	if start < 1 || end < start {
		return contract.Fail("INVALID_LINE_RANGE", fmt.Sprintf("Invalid line range %d..%d: start_line must be positive and end_line must be >= start_line. Bounds are inclusive; at most 500 lines are returned per item.", start, end), 400)
	}
	return nil
}

func (s *Store) Read(ctx context.Context, repo, commit, path string, start, end int) (File, error) {
	result := File{Path: path, Commit: commit, Encoding: "utf-8"}
	if err := ValidateReadRange(path, start, end); err != nil {
		return result, err
	}
	if !s.allows(path, false) {
		return result, contract.Fail("PATH_NOT_INDEXED", "Path is outside the registered scope.", 404)
	}
	r, err := s.OpenReader(ctx, repo, commit)
	if err != nil {
		return result, err
	}
	return r.read(ctx, path, start, end, MaxReadBytes, false)
}

// Read uses the caller's remaining raw-content budget, returning complete lines
// only. Budget truncation can return both usable File metadata and an error.
func (r *Reader) Read(ctx context.Context, path string, start, end, maxBytes int) (File, error) {
	return r.read(ctx, path, start, end, maxBytes, true)
}

func (r *Reader) read(ctx context.Context, path string, start, end, maxBytes int, batch bool) (File, error) {
	result := File{Path: path, Commit: r.commit, Encoding: "utf-8"}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := ValidateReadRange(path, start, end); err != nil {
		return result, err
	}
	if !r.store.allows(path, false) {
		return result, contract.Fail("PATH_NOT_INDEXED", "Path is outside the registered scope.", 404)
	}
	if maxBytes < 1 || maxBytes > MaxReadBytes {
		return result, contract.Fail("INVALID_READ_BUDGET", "Use a positive read budget no greater than 256 KiB.", 400)
	}
	e, err := r.store.resolvePath(ctx, r.path, "", r.commit, path, nil)
	if err != nil && LinkErrorCode(err) != "" {
		raw, rawErr := r.store.entry(ctx, r.path, r.commit, path)
		if rawErr == nil && raw.Kind == "symlink" {
			result.FollowError = LinkErrorCode(err)
			e, err = raw, nil
		}
	}
	if err != nil {
		return result, err
	}
	if !r.store.allows(e.Path, false) {
		return result, contract.Fail("PATH_NOT_INDEXED", "Link target is outside this snapshot coverage.", 409)
	}
	if e.Path != path {
		result.Path, result.Via = e.Path, path
	}
	result.BlobOID, result.Kind = e.BlobOID, e.Kind
	if e.Kind == "submodule" {
		result.Encoding = "gitlink"
		more := false
		result.HasMore = &more
		return result, nil
	}
	if e.Kind == "directory" {
		return result, contract.Fail("NOT_A_FILE", "Path is a directory; use list to browse it.", 422)
	}
	if e.Size == nil {
		sizes, err := r.store.blobSizes(ctx, r.path, []Entry{e})
		if err != nil {
			return result, err
		}
		size, ok := sizes[e.BlobOID]
		if !ok {
			return result, contract.Fail("GIT_OBJECT_NOT_HYDRATED", "Target object is unavailable; prepare this scope.", 503)
		}
		e.Size = &size
	}
	bounded, cancel := context.WithTimeout(ctx, r.store.timeout)
	defer cancel()
	c := r.store.command(bounded, r.path, "cat-file", "blob", e.BlobOID)
	c.Stderr = io.Discard
	stdout, err := c.StdoutPipe()
	if err != nil {
		return result, contract.Fail("GIT_OPERATION_FAILED", "Cannot read Git object.", 500)
	}
	if err = c.Start(); err != nil {
		if bounded.Err() != nil {
			return result, bounded.Err()
		}
		return result, contract.Fail("GIT_OPERATION_FAILED", "Cannot read Git object.", 500)
	}
	result, readErr, consumed := readContent(stdout, result, *e.Size, start, end, maxBytes, batch)
	// Save the original deadline state before intentional producer cancellation.
	deadlineErr := bounded.Err()
	stopped := consumed < *e.Size
	if stopped {
		cancel()
	}
	waitErr := c.Wait()
	if ctx.Err() != nil {
		return File{Path: path, Commit: r.commit, Encoding: "utf-8"}, ctx.Err()
	}
	if deadlineErr != nil || (!stopped && bounded.Err() != nil) {
		if deadlineErr == nil {
			deadlineErr = bounded.Err()
		}
		return File{Path: path, Commit: r.commit, Encoding: "utf-8"}, deadlineErr
	}
	if !stopped && waitErr != nil {
		return File{Path: path, Commit: r.commit, Encoding: "utf-8"}, contract.Fail("GIT_OPERATION_FAILED", "Cannot read Git object.", 500)
	}
	return result, readErr
}
