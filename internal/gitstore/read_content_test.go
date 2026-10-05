package gitstore

import (
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
)

func TestReadContentCompleteLineBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		start, end        int
		more, lfs         bool
	}{
		{"empty", "", "", 1, 10, false, false},
		{"crlf", "a\r\n你\r\nlast", "你\r\nlast", 2, 10, false, false},
		{"newline", "one\n", "one\n", 1, 1, false, false},
		{"no-newline", "你好", "你好", 1, 1, false, false},
		{"buffer-boundary", strings.Repeat("a", 4096), strings.Repeat("a", 4096), 1, 1, false, false},
		{"buffer-unicode-boundary", strings.Repeat("a", 4095) + "你\nnext\n", strings.Repeat("a", 4095) + "你\n", 1, 1, true, false},
		{"lfs", "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 123\n", "oid sha256:abc\n", 2, 2, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err, _ := readContent(strings.NewReader(tc.input), File{}, int64(len(tc.input)), tc.start, tc.end, MaxReadBytes, false)
			if err != nil || out.Content != tc.want || out.HasMore == nil || *out.HasMore != tc.more || out.IsLFSPointer != tc.lfs || !utf8.ValidString(out.Content) {
				t.Fatalf("result=%+v err=%v", out, err)
			}
			if tc.more && (out.NextStartLine == nil || *out.NextStartLine != tc.end+1) {
				t.Fatal("incorrect next start line")
			}
		})
	}
}

func TestReadContentErrorsAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name, input, code string
		start, budget     int
		batch             bool
	}{
		{"eof", "one\n", "LINE_RANGE_NOT_SATISFIABLE", 2, MaxReadBytes, false},
		{"binary", "one\x00\n", "BINARY_FILE", 1, MaxReadBytes, false},
		{"encoding", "one\xff\n", "UNSUPPORTED_TEXT_ENCODING", 1, MaxReadBytes, false},
		{"oversized-line", strings.Repeat("a", MaxReadBytes+1), "FILE_LINE_TOO_LARGE", 1, MaxReadBytes, false},
		{"batch-remainder", "你好\n", "READ_BUDGET_EXCEEDED", 1, 6, true},
		{"scan-limit", strings.Repeat("a", MaxScanBytes+1), "READ_SCAN_LIMIT", 2, MaxReadBytes, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err, _ := readContent(strings.NewReader(tc.input), File{}, int64(len(tc.input)), tc.start, tc.start+1, tc.budget, tc.batch)
			var problem *contract.Error
			if !errors.As(err, &problem) || problem.Code != tc.code {
				t.Fatalf("expected %s got %v", tc.code, err)
			}
		})
	}
	input := strings.Repeat("a", MaxReadBytes-4) + "\n" + "你好\n"
	out, err, _ := readContent(strings.NewReader(input), File{}, int64(len(input)), 1, 2, MaxReadBytes, false)
	if err != nil || !out.Truncated || out.Content != input[:MaxReadBytes-3] || *out.NextStartLine != 2 {
		t.Fatalf("single read truncation metadata changed: %v", err)
	}
}

type countedReader struct {
	reader io.Reader
	bytes  int
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

func TestReadContentStopsBackendReadAtRequestedRange(t *testing.T) {
	input := "first\n" + strings.Repeat("unread suffix\n", 200000)
	reader := &countedReader{reader: strings.NewReader(input)}
	out, err, scanned := readContent(reader, File{}, int64(len(input)), 1, 1, MaxReadBytes, false)
	if err != nil || out.Content != "first\n" || scanned != 6 || reader.bytes > 4096 {
		t.Fatalf("read excess data: bytes=%d scanned=%d err=%v", reader.bytes, scanned, err)
	}
	reader = &countedReader{reader: strings.NewReader(strings.Repeat("x", 1<<20))}
	out, err, _ = readContent(reader, File{}, 1<<20, 1, 1, 20, true)
	var problem *contract.Error
	if !errors.As(err, &problem) || problem.Code != "READ_BUDGET_EXCEEDED" || out.Content != "" || reader.bytes > 4096 {
		t.Fatalf("budget should stop before draining the line: bytes=%d err=%v", reader.bytes, err)
	}
}
