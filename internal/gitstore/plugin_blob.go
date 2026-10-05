package gitstore

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/contract"
)

// ReadBlobPath is a bounded binary read of a regular file at a fixed commit.
// It never follows links, interprets a user-supplied object ID or fetches LFS.
func (s *Store) ReadBlobPath(ctx context.Context, repo, commit, path string, limit int) ([]byte, error) {
	if e := ValidatePath(path, false); e != nil {
		return nil, e
	}
	if !s.allows(path, false) {
		return nil, contract.Fail("PATH_NOT_INDEXED", "Path is outside registered coverage.", 404)
	}
	if limit < 1 {
		return nil, contract.Fail("INVALID_LIMIT", "Invalid blob budget.", 400)
	}
	p, e := s.fixed(ctx, repo, commit)
	if e != nil {
		return nil, e
	}
	entry, e := s.entry(ctx, p, commit, path)
	if e != nil {
		return nil, e
	}
	if entry.Kind != "file" {
		return nil, contract.Fail("NOT_A_FILE", "Plugin reads require regular Git files; links and submodules are not followed.", 422)
	}
	if entry.Size == nil || *entry.Size > int64(limit) {
		return nil, contract.Fail("PLUGIN_FILE_TOO_LARGE", "File exceeds configured plugin read budget.", 422)
	}
	data, e := s.prefix(ctx, p, entry.BlobOID, limit+1)
	if e != nil {
		return nil, e
	}
	if len(data) > limit {
		return nil, contract.Fail("PLUGIN_FILE_TOO_LARGE", "File exceeds configured plugin read budget.", 422)
	}
	return data, nil
}
