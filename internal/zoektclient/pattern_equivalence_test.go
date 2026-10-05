package zoektclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
)

// The corpus covers what Agents search for: code punctuation, Chinese text,
// mixed case, quotes, backslashes and text that only matches across lines.
var equivalenceCorpus = map[string]string{
	"extra/a,b.ts":     "marker spaces (literal) . * + ? [x] {y} $ ^ |\nabc123 xyz456\n",
	"extra/!bang.ts":   "marker\n",
	"extra/.hidden.ts": "marker\n",
	"extra/中文.ts":      "marker 直播审核\n",
	"extra/case.txt":   "fooBAR fooBar FOObar\nfoobar foo_bar foo-bar\naaaa ab ac abc abbc\n",
	"extra/crlf.txt":   "marker\r\nlast marker",

	"src/server.go": `package server

func (s *Service) Search(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return s.run(ctx) // WithTimeout(ctx keeps the deadline
}
`,
	"src/server_test.go": `package server

func TestSearch(t *testing.T) {
	_ = context.WithTimeout
	path := "C:\path\to\file"
	msg := "line\n"
}
`,
	"src/web/app.ts": `export const OpenOnboardingV7 = true;
enum OnboardingSystem { A, B }
function getVersionList(a*b: number) { return a\*b; }
const MAX_VERSION_POLL_TIMES = 3;
let max_Version = 1;
const version = "1.0"; // Version
say "hi" \ there
`,
	"src/web/App.tsx": `const Version = 2;
const title = 'version';
`,
	"lib/src/k.go": `package src
// TODO: 直播间审核流程
var phone = "555-1234"
`,
	"lib/z.go": `package lib
// 直播 审核 规则
func Area(width int) int { return width }
`,
	"vendor/x/v.go": `package x
func WithTimeout() {}
`,
	"docs/notes.md": `# 直播间
a
b
foo
bar;
Ünïcode ÄBC äbc
tabs	and  spaces
same line: axb a  b foo x; foo--bar foo-bar
`,
	"[id].md": "bracket file WithTimeout\n",
}

type equivalenceCase struct {
	name  string
	p     Pattern
	grepE bool // also compare with grep -E (only where ERE and RE2 agree)
}

var equivalenceCases = []equivalenceCase{
	{name: "escaped punctuation", p: Pattern{Text: `\(literal\) \. \* \+ \? \[x\] \{y\} \$ \^ \|`}},
	{name: "fixed metacharacters", p: Pattern{Text: "(literal) . * + ? [x] {y} $ ^ |", FixedStrings: true}},
	{name: "counted repeat", p: Pattern{Text: `ab{1,2}c`}},
	{name: "noncapturing alternatives", p: Pattern{Text: `foo(?:bar|_bar|-bar)`}},
	{name: "lazy quantifier", p: Pattern{Text: `a+?b`}},
	{name: "POSIX character class", p: Pattern{Text: `[[:alpha:]]+[[:digit:]]+`}},
	{name: "hex character escape", p: Pattern{Text: `\x61bc`}},
	{name: "inline scoped case disable", p: Pattern{Text: `(?i:foo)(?-i:BAR)`, IgnoreCase: true}},
	{name: "nested case flags", p: Pattern{Text: `(?i:foo(?-i:Bar))`}},
	{name: "line anchors and CRLF", p: Pattern{Text: `^marker\r?$`}},
	{name: "final line without newline", p: Pattern{Text: `marker$`}},
	{name: "glob hidden files", p: Pattern{Text: "marker", Globs: []string{"*.ts"}}},
	{name: "glob literal comma", p: Pattern{Text: "marker", Globs: []string{`extra/a\,b.ts`}}},
	{name: "glob literal exclamation", p: Pattern{Text: "marker", Globs: []string{`\!bang.ts`}}},
	{name: "glob braces path alternatives", p: Pattern{Text: "marker", Globs: []string{"extra/{a,b,!bang}.ts"}}},
	{name: "glob root anchored", p: Pattern{Text: "marker", Globs: []string{"/extra/*.ts"}}},
	{name: "glob recursive exclude and reinclude", p: Pattern{Text: "marker", Globs: []string{"*.ts", "!a,b.ts", "a,b.ts"}}},

	{name: "inline case override after leading flag", p: Pattern{Text: "(?i)(?-i)Version"}},
	{name: "inline case override of option", p: Pattern{Text: "(?-i)Version", IgnoreCase: true}},
	{name: "local case sensitive group", p: Pattern{Text: "(?i:version)|(?-i:WithTimeout)", IgnoreCase: true}},
	{name: "plain word", p: Pattern{Text: "WithTimeout"}, grepE: true},
	{name: "escaped paren", p: Pattern{Text: `WithTimeout\(`}, grepE: true},
	{name: "fixed paren", p: Pattern{Text: "WithTimeout(ctx", FixedStrings: true}, grepE: true},
	{name: "fixed method receiver", p: Pattern{Text: "func (s *Service)", FixedStrings: true}, grepE: true},
	{name: "alternation", p: Pattern{Text: `OpenOnboardingV7 =|enum OnboardingSystem|getVersionList\(`}, grepE: true},
	{name: "case sensitive by default", p: Pattern{Text: "version"}, grepE: true},
	{name: "case sensitive upper", p: Pattern{Text: "Version"}, grepE: true},
	{name: "ignore case", p: Pattern{Text: "version", IgnoreCase: true}, grepE: true},
	{name: "leading inline ignore case", p: Pattern{Text: "(?i)version"}},
	{name: "inline ignore case mid pattern", p: Pattern{Text: "MAX_(?i:version)_POLL"}},
	{name: "inline ignore case mid pattern lower", p: Pattern{Text: "max_(?i)version"}},
	{name: "fixed ignore case", p: Pattern{Text: "WITHTIMEOUT(", FixedStrings: true, IgnoreCase: true}, grepE: true},
	{name: "chinese literal", p: Pattern{Text: "直播间"}, grepE: true},
	{name: "chinese regex", p: Pattern{Text: "直播.*审核"}, grepE: true},
	{name: "han class", p: Pattern{Text: `\p{Han}{3}`}},
	{name: "fixed quotes", p: Pattern{Text: `say "hi"`, FixedStrings: true}, grepE: true},
	{name: "regex quotes and backslash", p: Pattern{Text: `"hi" \\ there`}, grepE: true},
	{name: "fixed backslashes", p: Pattern{Text: `C:\path\to`, FixedStrings: true}, grepE: true},
	{name: "backslash n in source", p: Pattern{Text: `line\\n`}, grepE: true},
	{name: "fixed star", p: Pattern{Text: "a*b", FixedStrings: true}, grepE: true},
	{name: "escaped star", p: Pattern{Text: `a\\\*b`}, grepE: true},
	{name: "line anchors", p: Pattern{Text: `^func `}, grepE: true},
	{name: "end anchor", p: Pattern{Text: `\)$`}, grepE: true},
	{name: "word boundary ascii", p: Pattern{Text: `\bctx\b`}},
	{name: "digits", p: Pattern{Text: `\d{3}-\d{4}`}},
	{name: "dot never crosses lines", p: Pattern{Text: "a.b"}, grepE: true},
	{name: "dotall still line oriented", p: Pattern{Text: "(?s)foo.bar"}},
	{name: "whitespace never crosses lines", p: Pattern{Text: `a\s+b`}},
	{name: "negated class never crosses lines", p: Pattern{Text: `foo[^;]*;`}, grepE: true},
	{name: "non-word never crosses lines", p: Pattern{Text: `foo\W+bar`}},
	{name: "tab and spaces", p: Pattern{Text: `tabs\tand {2}spaces`}},
	{name: "unicode ignore case", p: Pattern{Text: "äbc", IgnoreCase: true}},

	{name: "glob extension", p: Pattern{Text: "WithTimeout", Globs: []string{"*.go"}}},
	{name: "glob exclude tests", p: Pattern{Text: "WithTimeout", Globs: []string{"*.go", "!*_test.go"}}},
	{name: "glob later include wins", p: Pattern{Text: "WithTimeout", Globs: []string{"!*_test.go", "*.go"}}},
	{name: "glob exclude directory prunes", p: Pattern{Text: "WithTimeout", Globs: []string{"!vendor"}}},
	{name: "glob exclude dir beats later include", p: Pattern{Text: "WithTimeout", Globs: []string{"!vendor", "*.go"}}},
	{name: "glob exclude directory any depth", p: Pattern{Text: "package", Globs: []string{"!src"}}},
	{name: "glob exclude directory only", p: Pattern{Text: "package", Globs: []string{"!src/"}}},
	{name: "glob anchored exclude", p: Pattern{Text: "package", Globs: []string{"!lib/src"}}},
	{name: "glob subtree", p: Pattern{Text: "version", IgnoreCase: true, Globs: []string{"src/**"}}},
	{name: "glob double star prefix", p: Pattern{Text: "package", Globs: []string{"**/src/*.go"}}},
	{name: "glob anchored single level", p: Pattern{Text: "package", Globs: []string{"lib/*.go"}}},
	{name: "glob braces", p: Pattern{Text: "Version", Globs: []string{"*.{ts,tsx}"}}},
	{name: "glob class", p: Pattern{Text: "package", Globs: []string{"[kz].go"}}},
	{name: "glob negated class", p: Pattern{Text: "package", Globs: []string{"[!k].go"}}},
	{name: "glob case sensitive", p: Pattern{Text: "Version", Globs: []string{"*.tsx", "!app.tsx"}}},
	{name: "glob escaped bracket", p: Pattern{Text: "WithTimeout", Globs: []string{`\[id\].md`}}},
	{name: "glob question mark", p: Pattern{Text: "package", Globs: []string{"?.go"}}},
	{name: "glob exclude only", p: Pattern{Text: "WithTimeout", Globs: []string{"!*.go"}}},
	{name: "glob multiple includes", p: Pattern{Text: "直播", Globs: []string{"*.md", "lib/**"}}},
}

type memIndexFile struct{ data []byte }

func (m *memIndexFile) Read(off, sz uint32) ([]byte, error) { return m.data[off : off+sz], nil }
func (m *memIndexFile) Size() (uint32, error)               { return uint32(len(m.data)), nil }
func (m *memIndexFile) Close()                              {}
func (m *memIndexFile) Name() string                        { return "equivalence" }

func equivalenceSearcher(t *testing.T) zoekt.Searcher {
	t.Helper()
	b, err := index.NewShardBuilder(&zoekt.Repository{Name: "org/repo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range slices.Sorted(maps.Keys(equivalenceCorpus)) {
		if err := b.AddFile(name, []byte(equivalenceCorpus[name])); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := b.Write(&buf); err != nil {
		t.Fatal(err)
	}
	s, err := index.NewSearcher(&memIndexFile{buf.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// engineHits sends the generated query through the same parse step as
// zoekt-webserver and returns path:line for every matching line.
func engineHits(t *testing.T, s zoekt.Searcher, p Pattern) []string {
	t.Helper()
	wire, err := PatternQuery(p)
	if err != nil {
		t.Fatalf("PatternQuery: %v", err)
	}
	q, err := query.Parse(wire)
	if err != nil {
		t.Fatalf("engine cannot parse %q: %v", wire, err)
	}
	res, err := s.Search(context.Background(), q, &zoekt.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	filter, err := newPatternFilter(p)
	if err != nil {
		t.Fatal(err)
	}
	if filter.active() {
		raw, _ := json.Marshal(res)
		raw, err = filter.apply(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		res = &zoekt.SearchResult{}
		if err = json.Unmarshal(raw, res); err != nil {
			t.Fatal(err)
		}
	}
	hits := []string{}
	for _, f := range res.Files {
		for _, m := range f.LineMatches {
			if m.FileName {
				t.Fatalf("%q matched a path; pattern must only match contents", wire)
			}
			hits = append(hits, f.FileName+":"+strconv.Itoa(m.LineNumber))
		}
	}
	slices.Sort(hits)
	return slices.Compact(hits)
}

// lineOracle is rg's line-oriented model: each line is matched independently.
func lineOracle(t *testing.T, p Pattern) []string {
	t.Helper()
	expr := p.Text
	if p.FixedStrings {
		expr = regexp.QuoteMeta(expr)
	}
	if p.IgnoreCase {
		expr = "(?i)" + expr
	}
	re := regexp.MustCompile(expr)
	hits := []string{}
	for name, content := range equivalenceCorpus {
		for i, line := range strings.Split(content, "\n") {
			if re.MatchString(line) {
				hits = append(hits, name+":"+strconv.Itoa(i+1))
			}
		}
	}
	slices.Sort(hits)
	return hits
}

func writeCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range equivalenceCorpus {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// toolHits runs a local search tool printing path:line:text and treats
// exit status 1 as "no matches", like rg and grep do.
func toolHits(t *testing.T, dir, tool string, args ...string) []string {
	t.Helper()
	cmd := exec.Command(tool, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=en_US.UTF-8", "RIPGREP_CONFIG_PATH=")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		t.Fatalf("%s %v: %v", tool, args, err)
	}
	hits := []string{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(line, "./"), ":", 3)
		if len(parts) < 3 {
			t.Fatalf("unexpected %s output %q", tool, line)
		}
		hits = append(hits, parts[0]+":"+parts[1])
	}
	slices.Sort(hits)
	return hits
}

func rgArgs(p Pattern) []string {
	args := []string{"--no-config", "--no-ignore", "--hidden", "--no-heading", "--with-filename", "-n", "--color", "never"}
	if p.FixedStrings {
		args = append(args, "-F")
	}
	if p.IgnoreCase {
		args = append(args, "-i")
	}
	for _, g := range p.Globs {
		args = append(args, "-g", g)
	}
	return append(args, "-e", p.Text, ".")
}

func grepArgs(p Pattern) []string {
	args := []string{"-rn"}
	if p.FixedStrings {
		args = append(args, "-F")
	} else {
		args = append(args, "-E")
	}
	if p.IgnoreCase {
		args = append(args, "-i")
	}
	return append(args, "-e", p.Text, ".")
}

func TestSearchMatchesRgLineSemantics(t *testing.T) {
	s := equivalenceSearcher(t)
	rg, rgErr := exec.LookPath("rg")
	grep, grepErr := exec.LookPath("grep")
	dir := writeCorpus(t)
	for _, tc := range equivalenceCases {
		t.Run(tc.name, func(t *testing.T) {
			got := engineHits(t, s, tc.p)
			if len(tc.p.Globs) == 0 {
				if want := lineOracle(t, tc.p); !slices.Equal(got, want) {
					t.Errorf("line oracle mismatch for %+v\n engine: %v\n oracle: %v", tc.p, got, want)
				}
			}
			if rgErr == nil {
				if want := toolHits(t, dir, rg, rgArgs(tc.p)...); !slices.Equal(got, want) {
					t.Errorf("rg mismatch for %+v\n engine: %v\n rg:     %v", tc.p, got, want)
				}
			} else if len(tc.p.Globs) > 0 {
				t.Skip("rg is not installed; glob semantics are verified against rg")
			}
			if tc.grepE && grepErr == nil {
				if want := toolHits(t, dir, grep, grepArgs(tc.p)...); !slices.Equal(got, want) {
					t.Errorf("grep mismatch for %+v\n engine: %v\n grep:   %v", tc.p, got, want)
				}
			}
		})
	}
}

// Control: the same regexps sent to the engine verbatim differ from rg, which
// is why PatternQuery rewrites them.
func TestEngineDiffersFromRgWithoutRewrite(t *testing.T) {
	s := equivalenceSearcher(t)
	raw := func(re string) []string {
		q, err := query.Parse("case:yes content:" + quoteQ(re))
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Search(context.Background(), q, &zoekt.SearchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		hits := []string{}
		for _, f := range res.Files {
			for _, m := range f.LineMatches {
				hits = append(hits, f.FileName+":"+strconv.Itoa(m.LineNumber))
			}
		}
		slices.Sort(hits)
		return hits
	}
	// A whole fold-case literal becomes a case-sensitive "VERSION" substring.
	for _, re := range []string{`a\s+b`, `foo[^;]*;`, `(?i)version`, `(?i:version)`} {
		p := Pattern{Text: re}
		if slices.Equal(raw(re), engineHits(t, s, p)) {
			t.Errorf("%q: verbatim engine regexp already matches rg; the rewrite may be obsolete", re)
		}
		if !slices.Equal(engineHits(t, s, p), lineOracle(t, p)) {
			t.Errorf("%q: rewritten query differs from the line oracle", re)
		}
	}
}

// rg skips binary files and the engine skips oversized ones; neither may
// surface through the engine's "NOT-INDEXED: <reason>" placeholder body.
func TestSkippedDocumentsNeverMatch(t *testing.T) {
	b, err := index.NewShardBuilder(&zoekt.Repository{Name: "org/repo"})
	if err != nil {
		t.Fatal(err)
	}
	docs := []index.Document{
		{Name: "logo.png", Content: []byte("\x89PNG\x00 contains binary content")},
		{Name: "huge.md", Content: []byte("too many trigrams in here"), SkipReason: index.SkipReasonTooManyTrigrams},
		{Name: "big.json", Content: []byte("{}"), SkipReason: index.SkipReasonTooLarge},
		{Name: "notes.md", Content: []byte("rg skips binary content\nNOT-INDEXED: contains binary content\n")},
	}
	for _, d := range docs {
		if err := b.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := b.Write(&buf); err != nil {
		t.Fatal(err)
	}
	s, err := index.NewSearcher(&memIndexFile{buf.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		p    Pattern
		want []string
	}{
		{Pattern{Text: "binary"}, []string{"notes.md:1", "notes.md:2"}},
		{Pattern{Text: "NOT-INDEXED", FixedStrings: true}, []string{"notes.md:2"}},
		{Pattern{Text: "^NOT-INDEXED: contains binary content$"}, []string{"notes.md:2"}},
		{Pattern{Text: "trigrams|size limit", IgnoreCase: true}, []string{}},
		{Pattern{Text: "contains", Globs: []string{"*.png", "*.md"}}, []string{"notes.md:2"}},
	} {
		if got := engineHits(t, s, tc.p); !slices.Equal(got, tc.want) {
			t.Errorf("%+v: got %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestEquivalenceCasesFindSomething(t *testing.T) {
	// Guards against a corpus edit turning every comparison into empty == empty.
	s := equivalenceSearcher(t)
	for _, tc := range equivalenceCases {
		if len(engineHits(t, s, tc.p)) == 0 {
			t.Errorf("%s has no hits; the corpus no longer exercises it", tc.name)
		}
	}
}

// Wildcards now follow globset UTF-8 byte semantics.
func TestUnicodeGlobQuestionBoundary(t *testing.T) {
	s := equivalenceSearcher(t)
	p := Pattern{Text: "marker", Globs: []string{"extra/??.ts"}}
	if got := engineHits(t, s, p); len(got) != 0 {
		t.Fatal(got)
	}
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("rg not installed")
	}
	if got := toolHits(t, writeCorpus(t), rg, rgArgs(p)...); len(got) != 0 {
		t.Fatalf("rg Unicode glob behavior changed: %v", got)
	}
}
