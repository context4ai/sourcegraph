package mcpserver

import (
	"strings"
	"testing"
)

func TestSearchByteBudgetExplained(t *testing.T) {
	value := map[string]any{
		"Meta":   map[string]any{"Commit": strings.Repeat("a", 40), "Truncated": true, "TruncationReason": "response_bytes", "FilesOnly": true},
		"Result": map[string]any{"Files": []any{map[string]any{"FileName": "src/large.xml"}}},
	}
	text := searchText(value)
	if !strings.Contains(text, "FilesOnly: true") || !strings.Contains(text, "src/large.xml") || strings.Contains(text, "path match:") {
		t.Fatalf("file-only result not explained correctly: %s", text)
	}
	if !strings.Contains(text, "Response byte budget reached") || !strings.Contains(text, "Truncated: true") {
		t.Fatal("missing truncation status")
	}
}
