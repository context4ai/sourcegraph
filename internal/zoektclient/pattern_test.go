package zoektclient

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/sourcegraph/zoekt/query"
)

func patternCode(t *testing.T, p Pattern) string {
	t.Helper()
	_, err := PatternQuery(p)
	var ce *contract.Error
	if err == nil {
		return ""
	}
	if !errors.As(err, &ce) || ce.HTTPStatus != 400 {
		t.Fatalf("%+v: unexpected error %v", p, err)
	}
	return ce.Code
}

func TestPatternQueryRejectsWhatRgDefaultRejects(t *testing.T) {
	for _, p := range []Pattern{
		{Text: ""},
		{Text: string([]byte{0xff})},
		{Text: "a\x00"},
		{Text: strings.Repeat("x", MaxQueryBytes+1)},
		{Text: `foo(?=bar)`},   // lookahead: PCRE only (rg -P)
		{Text: `(?<!a)b`},      // lookbehind
		{Text: `(a)\1`},        // backreference
		{Text: `createStore(`}, // unbalanced, as in rg
		{Text: "a\nb"},         // literal newline needs rg -U
		{Text: `a\nb`},
		{Text: `[\n]`},
		{Text: "line one\nline two", FixedStrings: true},
		{Text: `x*`}, // matches the empty string
		{Text: `^`},
	} {
		if code := patternCode(t, p); code != "INVALID_PATTERN" {
			t.Errorf("%q accepted or misclassified: %q", p.Text, code)
		}
	}
}

func TestPatternQueryAcceptsAgentStylePatterns(t *testing.T) {
	for _, p := range []Pattern{
		{Text: `WithTimeout\(`},
		{Text: `createStore(`, FixedStrings: true},
		{Text: `func (s *Service)`, FixedStrings: true},
		{Text: `OpenOnboardingV7 =|enum OnboardingSystem|getVersionList\(`},
		{Text: `content:"x" or file:y`, FixedStrings: true},
		{Text: `say "hi" \ there`},
		{Text: `C:\path`, FixedStrings: true},
		{Text: `直播.*审核`},
		{Text: `(?i)version`},
		{Text: `max_(?i:version)`},
		{Text: `\p{Han}+\pL`},
		{Text: `[^;]*;`},
		{Text: `(?s)a.b`},
	} {
		q, err := PatternQuery(p)
		if err != nil {
			t.Fatalf("%q rejected: %v", p.Text, err)
		}
		if _, err := query.Parse(q); err != nil {
			t.Fatalf("%q generated unparseable %q: %v", p.Text, q, err)
		}
	}
}

func TestPatternQueryCaseIsExplicit(t *testing.T) {
	searcher := equivalenceSearcher(t)
	for _, p := range []Pattern{{Text: "version"}, {Text: "Version"}, {Text: "Version", IgnoreCase: true}, {Text: "(?i)Version"}, {Text: "(?i)Version", FixedStrings: true}, {Text: "max_(?i)version"}, {Text: "(?-i)Version", IgnoreCase: true}} {
		if got, want := engineHits(t, searcher, p), lineOracle(t, p); !slices.Equal(got, want) {
			t.Fatalf("%+v: got %v want %v", p, got, want)
		}
	}
}

func TestGlobValidation(t *testing.T) {
	for _, g := range []string{"", "!", "src/", "/", "[abc", "{a,b", "{a,{b,c}}", strings.Repeat("a", maxGlobBytes+1), "a\nb"} {
		if code := patternCode(t, Pattern{Text: "x", Globs: []string{g}}); code != "INVALID_GLOB" {
			t.Errorf("glob %q accepted: %q", g, code)
		}
	}
	many := make([]string, maxGlobs+1)
	for i := range many {
		many[i] = fmt.Sprintf("*.%d", i)
	}
	if code := patternCode(t, Pattern{Text: "x", Globs: many}); code != "INVALID_GLOB" {
		t.Fatal("too many globs accepted")
	}
	for _, g := range []string{"*.go", "!vendor/", "!**/*_test.go", "src/**", "**/src/*.{ts,tsx}", "[!_]*.go", `\[x\].md`, "中文/*.md"} {
		if code := patternCode(t, Pattern{Text: "x", Globs: []string{g}}); code != "" {
			t.Errorf("glob %q rejected: %q", g, code)
		}
	}
}
