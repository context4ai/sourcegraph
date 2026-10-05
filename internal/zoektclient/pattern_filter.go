package zoektclient

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/sourcegraph/zoekt"
)

type byteGlobRule struct {
	exclude   bool
	file, dir *regexp.Regexp
}
type patternFilter struct {
	boundary *boundaryProgram
	globs    []byteGlobRule
}

// Map each byte to the same-valued rune so Go's rune regexp executes the
// globset byte alphabet, including a wildcard covering part of a UTF-8 rune.
func byteAlphabet(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		b.WriteRune(rune(s[i]))
	}
	return b.String()
}
func needsByteGlob(globs []string) bool {
	for _, g := range globs {
		if strings.ContainsAny(g, "?[") {
			return true
		}
	}
	return false
}
func coarseGlob(g string) string {
	var b strings.Builder
	for i := 0; i < len(g); i++ {
		switch g[i] {
		case '\\':
			b.WriteByte(g[i])
			if i+1 < len(g) {
				i++
				b.WriteByte(g[i])
			}
		case '?':
			b.WriteByte('*')
		case '[':
			j := i + 1
			if j < len(g) && (g[j] == '!' || g[j] == '^') {
				j++
			}
			if j < len(g) && g[j] == ']' {
				j++
			}
			for j < len(g) && g[j] != ']' {
				j++
			}
			if j < len(g) {
				i = j
				b.WriteByte('*')
			} else {
				b.WriteByte(g[i])
			}
		default:
			b.WriteByte(g[i])
		}
	}
	return b.String()
}

func newPatternFilter(p Pattern) (*patternFilter, error) {
	f := &patternFilter{}
	re, e := parsePattern(p)
	if e != nil {
		return nil, e
	}
	if hasBoundary(re) {
		f.boundary, e = newBoundaryProgram(re)
		if e != nil {
			return nil, e
		}
	}
	if needsByteGlob(p.Globs) {
		for _, g := range p.Globs {
			r, e := compileGlobAlphabet(g, true)
			if e != nil {
				return nil, e
			}
			rule := byteGlobRule{exclude: r.exclude, dir: regexp.MustCompile(r.dir)}
			if r.file != "" {
				rule.file = regexp.MustCompile(r.file)
			}
			f.globs = append(f.globs, rule)
		}
	}
	return f, nil
}
func (f *patternFilter) active() bool { return f != nil && (f.boundary != nil || len(f.globs) > 0) }
func (f *patternFilter) allows(path string) bool {
	if len(f.globs) == 0 {
		return true
	}
	path = byteAlphabet(path)
	allowed := true
	for _, r := range f.globs {
		if !r.exclude {
			allowed = false
		}
	}
	for _, r := range f.globs {
		if r.exclude && r.dir.MatchString(path) {
			return false
		}
		if r.file != nil && r.file.MatchString(path) {
			allowed = !r.exclude
		}
	}
	return allowed
}
func (f *patternFilter) apply(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var result zoekt.SearchResult
	if e := json.Unmarshal(raw, &result); e != nil {
		return nil, e
	}
	files := result.Files[:0]
	candidateLines := 0
	keptLines := 0
	truncated := false
	for _, file := range result.Files {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		candidateLines += len(file.LineMatches)
		if !f.allows(file.FileName) {
			continue
		}
		if f.boundary != nil {
			lines := file.LineMatches[:0]
			for _, line := range file.LineMatches {
				if e := ctx.Err(); e != nil {
					return nil, e
				}
				spans, err := f.boundary.ranges(ctx, strings.TrimSuffix(string(line.Line), "\n"))
				if err != nil {
					return nil, err
				}
				if len(spans) == 0 {
					continue
				}
				if len(spans) > 1000 {
					spans = spans[:1000]
					truncated = true
				}
				line.LineFragments = nil
				for _, span := range spans {
					line.LineFragments = append(line.LineFragments, zoekt.LineFragmentMatch{LineOffset: span[0], Offset: uint32(line.LineStart + span[0]), MatchLength: span[1] - span[0]})
				}
				lines = append(lines, line)
			}
			file.LineMatches = lines
			if len(lines) == 0 {
				continue
			}
		}
		keptLines += len(file.LineMatches)
		files = append(files, file)
	}
	// Preserve candidate counters when the engine omitted results: downstream
	// must still disclose truncation even when all displayed candidates fail.
	if result.FileCount <= len(result.Files) && result.MatchCount <= candidateLines {
		result.FileCount = len(files)
		result.MatchCount = keptLines
	}
	result.Files = files
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if truncated {
		var fields map[string]json.RawMessage
		json.Unmarshal(raw, &fields)
		fields["ResponseTruncated"] = json.RawMessage("true")
		raw, err = json.Marshal(fields)
	}
	if len(raw) > MaxResponseBytes {
		return nil, fail("ENGINE_RESPONSE_TOO_LARGE", "Filtered search exceeded the response budget.", 502, false)
	}
	return raw, err
}
