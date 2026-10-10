package gitstore

import (
	"strings"
	"testing"
)

func TestReadableTreeSkipsUnsupportedPathsWithoutRewriting(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, name := range []string{"line\nbreak.md", "tab\tname", "back\\slash", "bad\xff", "../escape", "/absolute", "a//b", strings.Repeat("x", 4097)} {
		t.Run(name, func(t *testing.T) {
			bad := []byte("100644 blob " + oid + "\t" + name + "\x00")
			data := append(append([]byte{}, bad...), []byte("100644 blob "+oid+"\t文档 空格.md\x00")...)
			entries, skipped, err := parseReadableEntries(data, "")
			if err != nil || !skipped || len(entries) != 1 || entries[0].Path != "文档 空格.md" {
				t.Fatalf("entries=%+v skipped=%v err=%v", entries, skipped, err)
			}
			if _, err := parseEntries(bad, ""); problemCode(err) != "UNSUPPORTED_PATH_ENCODING" {
				t.Fatalf("strict parser accepted unsupported path: %v", err)
			}
		})
	}
}

func TestReadableTreeStillRejectsMalformedGitOutput(t *testing.T) {
	for _, data := range []string{"missing tab", "100644 blob invalid\tname\x00", "100644 blob " + strings.Repeat("a", 40) + " nope\tname\x00"} {
		if _, _, err := parseReadableEntries([]byte(data), ""); problemCode(err) != "INVALID_GIT_RESULT" {
			t.Fatalf("malformed response accepted: %q %v", data, err)
		}
	}
	entries, skipped, err := parseReadableEntries([]byte(""), "")
	if err != nil || skipped || len(entries) != 0 {
		t.Fatalf("empty tree: %v %v %v", entries, skipped, err)
	}
}
