package gitstore

import (
	"context"
	"io"
	"sort"

	"github.com/context4ai/sourcegraph/internal/contract"
)

const MaxReadBytes = 256 << 10
const MaxScanBytes = 8 << 20

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
}

type File struct {
	Via               string `json:",omitempty"`
	FollowError       string `json:",omitempty"`
	Path              string
	BlobOID           string
	Commit            string
	Kind              string
	Encoding          string
	Content           string
	ReturnedStartLine *int
	ReturnedEndLine   *int
	NextStartLine     *int
	Truncated         bool
	HasMore           *bool
	IsLFSPointer      bool
}

// prefix reads at most limit bytes of a blob and kills the producer on a bounded
// read. No shell, pager, textconv, checkout or symlink traversal is involved.
func (s *Store) prefix(ctx context.Context, p, oid string, limit int) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	c := s.command(bounded, p, "cat-file", "blob", oid)
	c.Stderr = io.Discard
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = c.Start(); err != nil {
		return nil, contract.Fail("GIT_OPERATION_FAILED", "Cannot read Git object.", 500)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)))
	if len(data) == limit {
		cancel()
	}
	waitErr := c.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if readErr != nil {
		return nil, contract.Fail("GIT_OPERATION_FAILED", "Cannot read Git object.", 500)
	}
	if len(data) < limit && waitErr != nil {
		return nil, contract.Fail("GIT_OPERATION_FAILED", "Cannot read Git object.", 500)
	}
	return data, nil
}
