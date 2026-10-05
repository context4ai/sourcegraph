package mcpserver

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSearchOutputControls(t *testing.T) {
	lines := []any{}
	for i := 1; i <= 12; i++ {
		lines = append(lines, map[string]any{"LineNumber": i, "Line": base64.StdEncoding.EncodeToString([]byte("unique-body"))})
	}
	value := map[string]any{"Meta": map[string]any{"Commit": "fixed-sha", "Truncated": false}, "Result": map[string]any{"Files": []any{map[string]any{"FileName": "a.go", "LineMatches": lines}}}}
	text := searchTextWithOptions(value, searchInput{})
	if strings.Count(text, "unique-body") != 10 || !strings.Contains(text, "LinesTruncated: true") {
		t.Fatal(text)
	}
	text = searchTextWithOptions(value, searchInput{MaxLinesPerFile: 1})
	if strings.Count(text, "unique-body") != 1 {
		t.Fatal(text)
	}
	text = searchTextWithOptions(value, searchInput{Output: "files"})
	if strings.Contains(text, "unique-body") || !strings.Contains(text, "a.go") || !strings.Contains(text, "fixed-sha") {
		t.Fatal(text)
	}
	if len(value["Result"].(map[string]any)["Files"].([]any)[0].(map[string]any)["LineMatches"].([]any)) != 12 {
		t.Fatal("mutated result")
	}
}
func TestLongSearchLineDoesNotHideLaterFiles(t *testing.T) {
	value := map[string]any{"Meta": map[string]any{}, "Result": map[string]any{"Files": []any{map[string]any{"FileName": "large", "LineMatches": []any{map[string]any{"LineNumber": 1, "Line": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 1<<20)))}}}, map[string]any{"FileName": "later", "LineMatches": []any{map[string]any{"LineNumber": 2, "Line": base64.StdEncoding.EncodeToString([]byte("useful later evidence"))}}}}}}
	text := searchTextWithOptions(value, searchInput{})
	if !strings.Contains(text, "LineTruncated: true") || !strings.Contains(text, "useful later evidence") || len(text) > 4096 {
		t.Fatal("large line hid other evidence")
	}
}

func TestZeroHitHintOnlyForEngineFilterSyntax(t *testing.T) {
	empty := map[string]any{"Meta": map[string]any{}, "Result": map[string]any{"Files": []any{}}}
	hinted := func(in searchInput) bool { return strings.Contains(searchTextWithOptions(empty, in), "Hint:") }
	for _, p := range []string{"content:review", "file:src/ WithTimeout", "(lang:go foo)", "-file:_test bar"} {
		if !hinted(searchInput{Pattern: p}) {
			t.Fatal("missing hint", p)
		}
	}
	for _, in := range []searchInput{{Pattern: "WithTimeout"}, {Pattern: "https://example.com"}, {Pattern: "namespace::type"}, {Pattern: "content:review", FixedStrings: true}} {
		if hinted(in) {
			t.Fatal("unexpected hint", in.Pattern)
		}
	}
	hit := map[string]any{"Meta": map[string]any{}, "Result": map[string]any{"Files": []any{map[string]any{"FileName": "a.go"}}}}
	if strings.Contains(searchTextWithOptions(hit, searchInput{Pattern: "content:review"}), "Hint:") {
		t.Fatal("hint despite hits")
	}
}
