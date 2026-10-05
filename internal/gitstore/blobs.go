package gitstore

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// VisitBlobs streams selected, already hydrated blobs through one Git process.
// Files over maxBytes are reported with nil content and their actual Size.
func (s *Store) VisitBlobs(ctx context.Context, repo, commit string, entries []Entry, maxBytes int, visit func(Entry, []byte) error) error {
	if maxBytes < 1 || maxBytes > MaxScanBytes {
		return fmt.Errorf("invalid blob stream limit")
	}
	p, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return err
	}
	sizes, err := s.blobSizes(ctx, p, entries)
	if err != nil {
		return err
	}
	var input strings.Builder
	selected := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Kind != "file" && e.Kind != "symlink" {
			continue
		}
		if !fullSHA.MatchString(e.BlobOID) {
			return fmt.Errorf("invalid blob identity")
		}
		size, ok := sizes[e.BlobOID]
		if !ok {
			return contract.Fail("GIT_OBJECT_NOT_HYDRATED", "A selected object is unavailable; prepare the scope again.", 503)
		}
		e.Size = &size
		if size > int64(maxBytes) {
			if err := visit(e, nil); err != nil {
				return err
			}
			continue
		}
		input.WriteString(e.BlobOID + "\n")
		selected = append(selected, e)
	}
	if len(selected) == 0 {
		return nil
	}
	bounded, cancel := context.WithCancel(ctx)
	defer cancel()
	c := s.command(bounded, p, "cat-file", "--batch")
	c.Stdin = strings.NewReader(input.String())
	c.Stderr = io.Discard
	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err = c.Start(); err != nil {
		return err
	}
	waited := false
	defer func() {
		if !waited {
			cancel()
			c.Wait()
		}
	}()
	reader := bufio.NewReader(stdout)
	for _, entry := range selected {
		if err = ctx.Err(); err != nil {
			return err
		}
		line, e := reader.ReadString('\n')
		if e != nil {
			return e
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != entry.BlobOID || fields[1] != "blob" {
			return contract.Fail("GIT_OBJECT_NOT_HYDRATED", "A selected object is unavailable; prepare the scope again.", 503)
		}
		size, e := strconv.ParseInt(fields[2], 10, 64)
		if e != nil || size < 0 {
			return fmt.Errorf("invalid blob stream size")
		}
		entry.Size = &size
		var content []byte
		if size <= int64(maxBytes) {
			content = make([]byte, int(size))
			_, e = io.ReadFull(reader, content)
		} else {
			_, e = io.CopyN(io.Discard, reader, size)
		}
		if e != nil {
			return e
		}
		separator, e := reader.ReadByte()
		if e != nil || separator != '\n' {
			return fmt.Errorf("invalid blob stream separator")
		}
		if e = visit(entry, content); e != nil {
			return e
		}
	}
	err = c.Wait()
	waited = true
	return err
}
