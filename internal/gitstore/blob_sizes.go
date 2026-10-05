package gitstore

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/context4ai/sourcegraph/internal/contract"
)

func (s *Store) blobSizes(ctx context.Context, p string, entries []Entry) (map[string]int64, error) {
	sizes := map[string]int64{}
	var input strings.Builder
	for _, e := range entries {
		if e.Kind == "file" || e.Kind == "symlink" {
			if !fullSHA.MatchString(e.BlobOID) {
				return nil, contract.Fail("INVALID_BLOB", "Invalid object identity.", 500)
			}
			input.WriteString(e.BlobOID + "\n")
		}
	}
	if input.Len() == 0 {
		return sizes, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	c := s.command(ctx, p, "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	c.Stdin = strings.NewReader(input.String())
	c.Stderr = io.Discard
	out := &limitWriter{limit: len(entries)*100 + 1024}
	c.Stdout = out
	if err := c.Run(); err != nil {
		return nil, contract.Fail("GIT_OPERATION_FAILED", "Selected object metadata is unavailable.", 500)
	}
	for _, row := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		f := strings.Fields(row)
		if len(f) == 2 && f[1] == "missing" {
			continue
		}
		if len(f) != 3 || !fullSHA.MatchString(f[0]) || f[1] != "blob" {
			return nil, contract.Fail("INVALID_GIT_RESULT", "Invalid object metadata.", 500)
		}
		n, e := strconv.ParseInt(f[2], 10, 64)
		if e != nil || n < 0 {
			return nil, contract.Fail("INVALID_GIT_RESULT", "Invalid object size.", 500)
		}
		sizes[f[0]] = n
	}
	return sizes, nil
}
