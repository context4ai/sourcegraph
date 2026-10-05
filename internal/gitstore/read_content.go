package gitstore

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// readContent scans at most MaxScanBytes, buffers at most one remaining-budget
// line, and stops immediately at the requested end. bufio may read ahead 4 KiB;
// the caller kills the Git producer instead of draining the rest of the blob.
func readContent(src io.Reader, result File, size int64, start, end, maxBytes int, batch bool) (File, error, int64) {
	limited := end-start >= 500
	if limited {
		end = start + 499
	}
	reader := bufio.NewReaderSize(io.LimitReader(src, MaxScanBytes), 4096)
	var consumed int64
	var output, lineBytes []byte
	line, last := 1, 0
	prefix := make([]byte, 0, 64)
	finish := func(more, truncated bool, err error) (File, error, int64) {
		result.Content = string(output)
		result.HasMore, result.Truncated = &more, truncated
		result.IsLFSPointer = strings.HasPrefix(string(prefix), "version https://git-lfs.github.com/spec/v1\n")
		if last > 0 {
			result.ReturnedStartLine, result.ReturnedEndLine = &start, &last
		}
		if more {
			next := start
			if last > 0 {
				next = last + 1
			}
			result.NextStartLine = &next
		}
		return result, err, consumed
	}
	fail := func(code, message string, status int) (File, error, int64) {
		return result, contract.Fail(code, message, status), consumed
	}
	for {
		chunk, readErr := reader.ReadSlice('\n')
		consumed += int64(len(chunk))
		if len(prefix) < cap(prefix) {
			prefix = append(prefix, chunk[:min(len(chunk), cap(prefix)-len(prefix))]...)
		}
		if readErr != nil && readErr != io.EOF && readErr != bufio.ErrBufferFull {
			return fail("GIT_OPERATION_FAILED", "Cannot read Git object.", 500)
		}
		if readErr == io.EOF && consumed < size && consumed < MaxScanBytes {
			return fail("GIT_OPERATION_FAILED", "Git object ended before its declared size.", 500)
		}
		if bytes.IndexByte(chunk, 0) >= 0 {
			return fail("BINARY_FILE", "File contains binary data.", 422)
		}
		if line >= start {
			if len(output)+len(lineBytes)+len(chunk) > maxBytes {
				if len(lineBytes)+len(chunk) > MaxReadBytes {
					return fail("FILE_LINE_TOO_LARGE", "A line exceeds the read budget.", 422)
				}
				if batch {
					return finish(true, true, contract.Fail("READ_BUDGET_EXCEEDED", "Remaining batch byte budget cannot fit the next complete line.", 422))
				}
				return finish(true, true, nil)
			}
			lineBytes = append(lineBytes, chunk...)
		}
		complete := readErr == nil || (consumed == size && len(lineBytes) > 0)
		if complete {
			if line >= start {
				if !utf8.Valid(lineBytes) {
					return fail("UNSUPPORTED_TEXT_ENCODING", "File is not UTF-8.", 422)
				}
				output = append(output, lineBytes...)
				last = line
				lineBytes = lineBytes[:0]
			}
			if line == end {
				return finish(consumed < size, limited && consumed < size, nil)
			}
			line++
		}
		if consumed == size {
			if last == 0 && size > 0 {
				return fail("LINE_RANGE_NOT_SATISFIABLE", "Start line is beyond end of file.", 416)
			}
			return finish(false, false, nil)
		}
		if readErr == io.EOF {
			if last == 0 {
				return fail("READ_SCAN_LIMIT", "Line range exceeds the scan budget.", 422)
			}
			return finish(true, true, nil)
		}
	}
}
