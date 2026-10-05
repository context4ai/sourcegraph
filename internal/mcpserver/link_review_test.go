package mcpserver

import (
	"strings"
	"testing"
)

func TestSearchLinkCoverageAndViaPlacement(t *testing.T) {
	for _, mode := range []string{"not_followed", "mixed"} {
		value := map[string]any{"Meta": map[string]any{"Coverage": map[string]any{"Symlinks": mode, "SymlinkExpansionComplete": false}}, "Result": map[string]any{"Files": []any{map[string]any{"FileName": "docs/a", "Via": []string{"context/a"}}}}}
		text := searchText(value)
		file, via := strings.Index(text, "File: \"docs/a\""), strings.Index(text, "Via:")
		if file < 0 || via < file || !strings.Contains(text, "Symlinks: "+mode) || !strings.Contains(text, "Zero matches do not prove absence") {
			t.Fatal(text)
		}
	}
}
