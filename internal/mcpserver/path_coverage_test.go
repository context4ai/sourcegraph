package mcpserver

import (
	"strings"
	"testing"
)

func TestUnsupportedPathCoverageOnEmptySearch(t *testing.T) {
	response := map[string]any{
		"Meta":   map[string]any{"Coverage": map[string]any{"UnsupportedPathsSkipped": true}},
		"Result": map[string]any{"Files": []any{}},
	}
	if got := searchText(response); !strings.Contains(got, "UnsupportedPathsSkipped: true") {
		t.Fatalf("missing exclusion warning: %s", got)
	}
}
