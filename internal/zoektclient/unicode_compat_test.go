package zoektclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os/exec"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sourcegraph/zoekt"
)

func TestUnicodeSearchMatchesRg(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Fatal("rg is required for compatibility verification")
	}
	original := equivalenceCorpus
	equivalenceCorpus = maps.Clone(original)
	t.Cleanup(func() { equivalenceCorpus = original })
	equivalenceCorpus["unicode/中.ts"] = "marker\n中文\n中 文\n中文abc\nabc中文\n"
	equivalenceCorpus["unicode/😀.ts"] = "marker\néclair e\u0301 Ⅷ १२٣４\nx\u00a0y x\u2003y x\u0085y\n"
	equivalenceCorpus["unicode/a中b.ts"] = "marker\n foo foo-bar foo_bar\n\u200c \u200d \u0345\n"
	equivalenceCorpus["unicode/a.ts"] = "marker\nabc ab abbc\n"
	equivalenceCorpus["unicode/é.ts"] = "marker\n"
	equivalenceCorpus["unicode/中文.ts"] = "marker\n"
	s := equivalenceSearcher(t)
	dir := writeCorpus(t)
	patterns := []string{`\w+`, `\W+`, `\d+`, `\D+`, `\s+marker`, `x\sy`, `x\Sy`, `[\w]+`, `[^\w]+`, `[\W]+`, `[[:alpha:]\d]+`, `[]\w]+`, `[a\W]+`, `[^\W]+`, `[\D\w]+`, `[\d_]+`, `[\D]+`, `[\s]+x`, `[\S]+`, `\b中文\b`, `\B中文`, `中文\B`, `\b\w+\b`, `\babc\b`, `\béclair\b`, `\be\pM\b`, `\bⅧ\b`, `\b१२٣４\b`, `\bfoo(?:-bar)?\b`, `(?:\bfoo\b|\b中文\b)`, `\b(?:ab|abc)\b`, `\b(?:abc|ab)\b`, `\b.*?\babc`, `^\b中文\b$`, `\B文\B`, `\b\w+?\b`, `(?i)\bÉCLAIR\b`, `\b(?:a|b)+\b`, `\b\w+\b.*\b\w+\b`}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			p := Pattern{Text: pattern}
			got := engineHits(t, s, p)
			want := toolHits(t, dir, rg, rgArgs(p)...)
			if !slices.Equal(got, want) {
				t.Fatalf("engine %v\nrg %v", got, want)
			}
		})
	}
	for _, glob := range []string{"unicode/?.ts", "unicode/??.ts", "unicode/???.ts", "unicode/????.ts", "unicode/??????.ts", "unicode/*?.ts", "unicode/?*.ts", "unicode/a???b.ts", "unicode/[aé].ts", "unicode/[!a].ts", "unicode/[!中]*.ts", "unicode/{???,????}.ts"} {
		t.Run(glob, func(t *testing.T) {
			p := Pattern{Text: "marker", Globs: []string{glob}}
			if got, want := engineHits(t, s, p), toolHits(t, dir, rg, rgArgs(p)...); !slices.Equal(got, want) {
				t.Fatalf("engine %v\nrg %v", got, want)
			}
		})
	}
}

func TestBoundaryMatchFragmentsAgainstRg(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{`\b\w+\b`, `\b\w+?\b`, `\B\w+\B`, `\b(?:ab|abc)\b`, `\b(?:abc|ab)\b`, `\bfoo\b|\b中文\b`, `\b.*?\babc`, `\b\w+\b.*\b\w+\b`} {
		for _, line := range []string{"中文abc abc中文 中文 abc", "éclair e\u0301 Ⅷ", "ab abc abbc foo foo-bar", "abcabc abc", "!!!"} {
			t.Run(pattern+line, func(t *testing.T) {
				re, e := parsePattern(Pattern{Text: pattern})
				if e != nil {
					t.Fatal(e)
				}
				vm, e := newBoundaryProgram(re)
				if e != nil {
					t.Fatal(e)
				}
				got, e := vm.ranges(context.Background(), line)
				if e != nil {
					t.Fatal(e)
				}
				cmd := exec.Command(rg, "--no-config", "--json", "-e", pattern)
				cmd.Stdin = strings.NewReader(line + "\n")
				out, e := cmd.Output()
				if e != nil {
					var exit *exec.ExitError
					if !errors.As(e, &exit) || exit.ExitCode() != 1 {
						t.Fatal(e)
					}
				}
				want := [][2]int{}
				for _, record := range strings.Split(string(out), "\n") {
					var event struct {
						Type string
						Data struct{ Submatches []struct{ Start, End int } }
					}
					if json.Unmarshal([]byte(record), &event) == nil && event.Type == "match" {
						for _, m := range event.Data.Submatches {
							if m.End > m.Start {
								want = append(want, [2]int{m.Start, m.End})
							}
						}
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %v want %v", got, want)
				}
			})
		}
	}
}

func TestPatternFilterPreservesTruncationAndCancellation(t *testing.T) {
	p := Pattern{Text: `\bfoo\b`}
	f, err := newPatternFilter(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(&zoekt.SearchResult{Stats: zoekt.Stats{FileCount: 101, MatchCount: 2000}, Files: []zoekt.FileMatch{{FileName: "a", LineMatches: []zoekt.LineMatch{{Line: []byte("中foo文\n"), LineNumber: 1}}}}})
	filtered, err := f.apply(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var got zoekt.SearchResult
	json.Unmarshal(filtered, &got)
	if len(got.Files) != 0 || got.FileCount != 101 || got.MatchCount != 2000 {
		t.Fatal(string(filtered))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = f.apply(ctx, raw); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	re, _ := parsePattern(Pattern{Text: `\b.*x\b`})
	vm, _ := newBoundaryProgram(re)
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err = vm.ranges(ctx, strings.Repeat("a", 2<<20)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

// Exercise the production client path, including file-only requests: filtering
// needs content internally, but must not leak content into the projection.
func TestUnicodeClientProjection(t *testing.T) {
	target := Target{Repo: "org/repo", Commit: strings.Repeat("a", 40), RepositoryID: 7}
	for _, filesOnly := range []bool{false, true} {
		c, _ := New("http://127.0.0.1:6070")
		c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body struct{ Q string }
			json.NewDecoder(req.Body).Decode(&body)
			if strings.HasPrefix(body.Q, "type:file") {
				t.Fatal("lost content before boundary verification")
			}
			files := []zoekt.FileMatch{}
			for i, line := range []string{"中foo文\n", "foo 中文\n"} {
				files = append(files, zoekt.FileMatch{Repository: target.EngineName(), RepositoryID: 7, Version: target.Commit, FileName: fmt.Sprintf("%d.go", i), LineMatches: []zoekt.LineMatch{{Line: []byte(line), LineNumber: 1}}})
			}
			data, _ := json.Marshal(map[string]any{"Result": zoekt.SearchResult{Files: files, Stats: zoekt.Stats{FileCount: 2, MatchCount: 2}}})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
		})
		p := Pattern{Text: `\bfoo\b`}
		q, err := PatternQuery(p)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := c.SearchManyWithOptions(context.Background(), []Target{target}, q, DisplayOptions{FilesOnly: filesOnly, Pattern: &p})
		if err != nil {
			t.Fatal(err)
		}
		var got zoekt.SearchResult
		if err = json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Files) != 1 || got.Files[0].FileName != "1.go" || got.FileCount != 1 {
			t.Fatal(string(raw))
		}
		if filesOnly {
			if len(got.Files[0].LineMatches) != 0 {
				t.Fatal(string(raw))
			}
		} else if len(got.Files[0].LineMatches) != 1 || got.Files[0].LineMatches[0].LineFragments[0].MatchLength != 3 {
			t.Fatal(string(raw))
		}
	}
}

func TestUnicodeEscapedLiteral(t *testing.T) {
	re, err := parsePattern(Pattern{Text: `\Q\w\E`})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := regexp.Compile(re.String())
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.MatchString(`\w`) || compiled.MatchString("中文") {
		t.Fatal(re.String())
	}
}
