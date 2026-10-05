package zoektclient

import (
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sourcegraph/zoekt/query"
)

// Pattern is an rg-style content search: Text is an RE2 regexp (or a literal
// with FixedStrings) matched line by line; Globs follow rg -g.
type Pattern struct {
	Text         string
	FixedStrings bool
	IgnoreCase   bool
	Globs        []string
}

const (
	maxGlobs     = 64
	maxGlobBytes = 1024
	// Like rg's default regex: ^ and $ anchor lines, while . and negated
	// classes exclude newlines. The pinned engine parses with ClassNL, so
	// the serialized expression must spell out every newline exclusion.
	patternFlags = syntax.PerlX | syntax.UnicodeGroups
)

// The pinned engine indexes a skipped document (binary, too large, too many
// trigrams) as the single line "NOT-INDEXED: <reason>", so a plain search for
// "binary" would hit every image. rg never matches those files; excluding the
// exact placeholder body keeps real files that quote it searchable.
var skippedPlaceholder = "-( case:yes content:" + quoteQ(`\ANOT-INDEXED: (?:exceeds the maximum size limit|contains too few trigrams|contains binary content|contains too many trigrams|object missing from repository|unknown skip reason)\z`) + " )"

var errMultiline = fail("INVALID_PATTERN", "pattern cannot match a newline: search is line-oriented, like rg without -U. Search for each line separately.", 400, false)

// PatternQuery builds the engine query for an rg-style search. Callers never
// write engine syntax, so quoting and grouping cannot leak into their input.
func PatternQuery(p Pattern) (string, error) {
	text := p.Text
	if text == "" || len(text) > MaxQueryBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "", fail("INVALID_PATTERN", "pattern must be non-empty valid UTF-8, at most 8192 bytes.", 400, false)
	}
	re, err := parsePattern(p)
	if err != nil {
		return "", fail("INVALID_PATTERN", "pattern is not a valid RE2 regular expression: "+err.Error()+". Lookaround and backreferences are unsupported; set fixed_strings to match literal text.", 400, false)
	}
	re, err = lineRegexp(re)
	if err != nil {
		return "", err
	}
	serialized := re.String()
	if compiled, err := regexp.Compile(serialized); err != nil || compiled.MatchString("") {
		return "", fail("INVALID_PATTERN", "pattern matches the empty string and would match every line; make it match at least one character.", 400, false)
	}
	if hasBoundary(re) {
		serialized = withoutBoundary(re).String()
		if compiled, err := regexp.Compile(serialized); err != nil || compiled.MatchString("") {
			return "", fail("INVALID_PATTERN", "pattern must consume at least one character; zero-width-only or nullable searches are not supported.", 400, false)
		}
	}
	mode := "yes"
	q := "( case:" + mode + " content:" + quoteQ(serialized) + " ) " + skippedPlaceholder
	// Validate before broadening; invalid user globs must not become valid.
	globs, err := globFilter(p.Globs)
	if err == nil && needsByteGlob(p.Globs) {
		// Includes can be broadened safely. Excludes are checked exactly later.
		includes := []string{}
		for _, g := range p.Globs {
			if !strings.HasPrefix(g, "!") {
				includes = append(includes, coarseGlob(g))
			}
		}
		globs, err = globFilter(includes)
	}
	if err != nil {
		return "", err
	}
	if globs != "" {
		q += " " + globs
	}
	if _, err := query.Parse(q); err != nil {
		return "", fail("INTERNAL_ERROR", "Generated search could not be parsed by the engine.", 500, false)
	}
	return q, nil
}

// lineRegexp removes every way to match a newline and rewrites inline (?i)
// literals into explicit classes: the pinned engine turns a fold-case literal
// into a case-sensitive substring of its upper-case runes.
func lineRegexp(re *syntax.Regexp) (*syntax.Regexp, error) {
	switch re.Op {
	case syntax.OpLiteral:
		if slices.Contains(re.Rune, '\n') {
			return nil, errMultiline
		}
		if re.Flags&syntax.FoldCase != 0 {
			re.Flags &^= syntax.FoldCase
			return foldLiteral(re), nil
		}
	case syntax.OpCharClass:
		re.Rune = withoutNewline(re.Rune)
		if len(re.Rune) == 0 {
			return nil, errMultiline
		}
		re.Flags &^= syntax.FoldCase
	case syntax.OpAnyChar:
		re.Op = syntax.OpAnyCharNotNL
	}
	for i, sub := range re.Sub {
		next, err := lineRegexp(sub)
		if err != nil {
			return nil, err
		}
		re.Sub[i] = next
	}
	return re, nil
}

func foldLiteral(re *syntax.Regexp) *syntax.Regexp {
	parts := make([]*syntax.Regexp, 0, len(re.Rune))
	for _, r := range re.Rune {
		orbit := []rune{r}
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			orbit = append(orbit, f)
		}
		if len(orbit) == 1 {
			parts = append(parts, &syntax.Regexp{Op: syntax.OpLiteral, Flags: re.Flags, Rune: orbit})
			continue
		}
		slices.Sort(orbit)
		class := []rune{}
		for _, f := range orbit {
			if n := len(class); n > 0 && class[n-1]+1 == f {
				class[n-1] = f
			} else {
				class = append(class, f, f)
			}
		}
		parts = append(parts, &syntax.Regexp{Op: syntax.OpCharClass, Rune: class})
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return &syntax.Regexp{Op: syntax.OpConcat, Sub: parts}
}

func withoutNewline(ranges []rune) []rune {
	out := make([]rune, 0, len(ranges)+2)
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		if lo > '\n' || hi < '\n' {
			out = append(out, lo, hi)
			continue
		}
		if lo < '\n' {
			out = append(out, lo, '\n'-1)
		}
		if hi > '\n' {
			out = append(out, '\n'+1, hi)
		}
	}
	return out
}

type globRule struct {
	exclude bool
	file    string // matches the file path; empty for directory-only globs
	dir     string // matches any ancestor directory of the file
}

// globFilter follows rg -g: a later matching glob wins between includes and
// excludes, files must match some include when any is given, and excluded
// directories prune everything beneath them.
func globFilter(globs []string) (string, error) {
	if len(globs) == 0 {
		return "", nil
	}
	if len(globs) > maxGlobs {
		return "", fail("INVALID_GLOB", "Use at most 64 globs.", 400, false)
	}
	rules := make([]globRule, 0, len(globs))
	hasInclude := false
	for _, g := range globs {
		rule, err := compileGlob(g)
		if err != nil {
			return "", err
		}
		hasInclude = hasInclude || !rule.exclude
		rules = append(rules, rule)
	}
	file := func(re string) string { return "file:" + quoteQ(re) }
	parts := []string{}
	for _, r := range rules {
		if !r.exclude {
			continue
		}
		parts = append(parts, "-"+file(r.dir))
		// With includes, file-level excludes are applied by precedence below.
		if !hasInclude && r.file != "" {
			parts = append(parts, "-"+file(r.file))
		}
	}
	if hasInclude {
		alternatives := []string{}
		for i, r := range rules {
			if r.exclude {
				continue
			}
			term := file(r.file)
			for _, later := range rules[i+1:] {
				if later.exclude && later.file != "" {
					term += " -" + file(later.file)
				}
			}
			alternatives = append(alternatives, "( "+term+" )")
		}
		parts = append(parts, "( "+strings.Join(alternatives, " or ")+" )")
	}
	return "( case:yes " + strings.Join(parts, " ") + " )", nil
}

func compileGlob(raw string) (globRule, error) { return compileGlobAlphabet(raw, false) }
func compileGlobAlphabet(raw string, bytes bool) (globRule, error) {
	invalid := func(reason string) (globRule, error) {
		return globRule{}, fail("INVALID_GLOB", "Invalid glob "+quoteQ(raw)+": "+reason+".", 400, false)
	}
	g, exclude := strings.CutPrefix(raw, "!")
	if g == "" || len(raw) > maxGlobBytes || !utf8.ValidString(g) || strings.ContainsAny(g, "\x00\n") {
		return invalid("use a non-empty pattern of at most 1024 bytes")
	}
	if bytes {
		g = byteAlphabet(g)
	}
	dirOnly := strings.HasSuffix(g, "/")
	g = strings.TrimRight(g, "/")
	if dirOnly && !exclude {
		return invalid("include globs match files; use paths for a directory or append /** to it")
	}
	if g == "" {
		return invalid("the pattern is only slashes")
	}
	// As in gitignore: a slash anywhere but the end anchors to the repository root.
	prefix := "(?:^|/)"
	if strings.Contains(g, "/") {
		prefix = "^"
		g = strings.TrimPrefix(g, "/")
	}
	body, reason := globBody(g)
	if reason != "" {
		return invalid(reason)
	}
	rule := globRule{exclude: exclude, dir: prefix + body + "/"}
	if !dirOnly {
		rule.file = prefix + body + "$"
	}
	return rule, nil
}

func globBody(g string) (string, string) {
	rs := []rune(g)
	var b strings.Builder
	inBrace := false
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '*' && i+1 < len(rs) && rs[i+1] == '*' && (i == 0 || rs[i-1] == '/') && (i+2 == len(rs) || rs[i+2] == '/'):
			if i+2 == len(rs) {
				b.WriteString(".*")
			} else {
				b.WriteString("(?:.*/)?")
			}
			i += 2
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			j := i + 1
			negated := j < len(rs) && (rs[j] == '!' || rs[j] == '^')
			if negated {
				j++
			}
			start := j
			if j < len(rs) && rs[j] == ']' {
				j++
			}
			for j < len(rs) && rs[j] != ']' {
				j++
			}
			if j >= len(rs) {
				return "", "unclosed character class"
			}
			b.WriteString("[")
			if negated {
				b.WriteString("^/")
			}
			for _, r := range rs[start:j] {
				if r == '-' {
					b.WriteRune(r)
				} else {
					b.WriteString(regexp.QuoteMeta(string(r)))
				}
			}
			b.WriteString("]")
			i = j
		case c == '{':
			if inBrace {
				return "", "nested {} groups are not supported"
			}
			inBrace = true
			b.WriteString("(?:")
		case c == '}' && inBrace:
			inBrace = false
			b.WriteString(")")
		case c == ',' && inBrace:
			b.WriteString("|")
		case c == '\\' && i+1 < len(rs):
			i++
			b.WriteString(regexp.QuoteMeta(string(rs[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if inBrace {
		return "", "unclosed {} group"
	}
	if _, err := syntax.Parse(b.String(), syntax.Perl); err != nil {
		return "", "unsupported character class"
	}
	return b.String(), ""
}
