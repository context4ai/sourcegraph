package zoektclient

import (
	"context"
	"fmt"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"unicode"
)

// Unicode shorthand definitions follow regex/UNICODE.md. Keep explicit ASCII
// POSIX classes unchanged. Go's Unicode tables are also used for case folding.
var wordClass = `\pL\pM\p{Nl}\p{Nd}\p{Pc}\x{200c}\x{200d}` + propertyRanges(unicode.Other_Alphabetic)

func propertyRanges(table *unicode.RangeTable) string {
	var b strings.Builder
	add := func(lo, hi, stride uint32) {
		if stride == 1 {
			fmt.Fprintf(&b, `\x{%x}-\x{%x}`, lo, hi)
			return
		}
		for r := lo; r <= hi; r += stride {
			fmt.Fprintf(&b, `\x{%x}`, r)
		}
	}
	for _, r := range table.R16 {
		add(uint32(r.Lo), uint32(r.Hi), uint32(r.Stride))
	}
	for _, r := range table.R32 {
		add(r.Lo, r.Hi, r.Stride)
	}
	return b.String()
}

func unicodePattern(expr string) (string, error) {
	var out strings.Builder
	inClass := false
	classFirst := false
	for i := 0; i < len(expr); i++ {
		if out.Len() > maxEngineQueryBytes {
			return "", fmt.Errorf("expanded pattern exceeds the compilation budget")
		}
		c := expr[i]
		if c == '[' && inClass && strings.HasPrefix(expr[i:], "[:") {
			if end := strings.Index(expr[i+2:], ":]"); end >= 0 {
				end += i + 4
				out.WriteString(expr[i:end])
				i = end - 1
				classFirst = false
				continue
			}
		}
		if c == '\\' && i+1 < len(expr) {
			i++
			e := expr[i]
			if e == 'Q' {
				end := strings.Index(expr[i+1:], `\E`)
				if end < 0 {
					out.WriteString(expr[i-1:])
					break
				}
				end += i + 3
				out.WriteString(expr[i-1 : end])
				i = end - 1
				classFirst = false
				continue
			}
			classFirst = false
			class := ""
			switch e {
			case 'w', 'W':
				class = wordClass
			case 'd', 'D':
				class = `\p{Nd}`
			case 's', 'S':
				class = `\x09-\x0d\x20\x85\xa0\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
			}
			if class != "" {
				if e >= 'A' && e <= 'Z' {
					// Negated shorthand inside a class needs a real union of ranges, not
					// a nested [^...] which Go's parser treats as literal punctuation.
					parsed, _ := syntax.Parse("[^"+class+"]", syntax.Perl)
					serialized := parsed.String()
					if inClass {
						for j := 0; j < len(parsed.Rune); j += 2 {
							fmt.Fprintf(&out, `\x{%x}-\x{%x}`, parsed.Rune[j], parsed.Rune[j+1])
						}
					} else {
						out.WriteString(serialized)
					}
				} else if inClass {
					out.WriteString(class)
				} else {
					out.WriteString("[" + class + "]")
				}
			} else {
				out.WriteByte('\\')
				out.WriteByte(e)
			}
			continue
		}
		if c == '[' && !inClass {
			inClass = true
			classFirst = true
		} else if inClass {
			if c == ']' && !classFirst {
				inClass = false
			}
			if c != '^' || !classFirst {
				classFirst = false
			}
		}
		out.WriteByte(c)
	}
	return out.String(), nil
}

func unicodeWord(r rune) bool {
	return r >= 0 && (unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) || unicode.IsMark(r) || unicode.Is(unicode.Nd, r) || unicode.Is(unicode.Pc, r) || unicode.Is(unicode.Other_Alphabetic, r) || r == 0x200c || r == 0x200d)
}

// boundaryProgram evaluates zero-width word assertions with Unicode semantics.
// The candidate engine expression omits those assertions; it cannot exclude a
// true match. This Thompson VM keeps at most one thread per instruction.
type boundaryProgram struct{ prog *syntax.Prog }
type matchThread struct {
	pc    uint32
	start int
}

func newBoundaryProgram(re *syntax.Regexp) (*boundaryProgram, error) {
	p, e := syntax.Compile(re.Simplify())
	if e != nil {
		return nil, e
	}
	return &boundaryProgram{p}, nil
}
func hasBoundary(re *syntax.Regexp) bool {
	if re.Op == syntax.OpWordBoundary || re.Op == syntax.OpNoWordBoundary {
		return true
	}
	for _, s := range re.Sub {
		if hasBoundary(s) {
			return true
		}
	}
	return false
}
func withoutBoundary(re *syntax.Regexp) *syntax.Regexp {
	r := *re
	r.Sub = nil
	if r.Op == syntax.OpWordBoundary || r.Op == syntax.OpNoWordBoundary {
		r.Op = syntax.OpEmptyMatch
	}
	for _, s := range re.Sub {
		r.Sub = append(r.Sub, withoutBoundary(s))
	}
	return &r
}

type matchPoint struct {
	pos int
	r   rune
}

func (p *boundaryProgram) find(ctx context.Context, points []matchPoint, offset int) ([]int, error) {
	current := []matchThread{}
	best := []int(nil)
	seen := make([]uint32, len(p.prog.Inst))
	generation := uint32(0)
	ready := make([]matchThread, 0, len(p.prog.Inst))
	for n := sort.Search(len(points), func(i int) bool { return points[i].pos >= offset }); n < len(points); n++ {
		if n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		pt := points[n]
		generation++
		ready = ready[:0]
		prev := rune(-1)
		if n > 0 {
			prev = points[n-1].r
		}
		empty := syntax.EmptyOpContext(prev, pt.r)
		empty &^= syntax.EmptyWordBoundary | syntax.EmptyNoWordBoundary
		if unicodeWord(prev) != unicodeWord(pt.r) {
			empty |= syntax.EmptyWordBoundary
		} else {
			empty |= syntax.EmptyNoWordBoundary
		}
		var add func(uint32, int)
		add = func(pc uint32, start int) {
			if seen[pc] == generation {
				return
			}
			seen[pc] = generation
			inst := p.prog.Inst[pc]
			switch inst.Op {
			case syntax.InstAlt, syntax.InstAltMatch:
				add(inst.Out, start)
				add(inst.Arg, start)
			case syntax.InstCapture, syntax.InstNop:
				add(inst.Out, start)
			case syntax.InstEmptyWidth:
				if syntax.EmptyOp(inst.Arg)&empty == syntax.EmptyOp(inst.Arg) {
					add(inst.Out, start)
				}
			case syntax.InstFail:
			default:
				ready = append(ready, matchThread{pc, start})
			}
		}
		for _, th := range current {
			add(th.pc, th.start)
		}
		if best == nil {
			add(uint32(p.prog.Start), pt.pos)
		}
		current = current[:0]
		for _, th := range ready {
			inst := p.prog.Inst[th.pc]
			if inst.Op == syntax.InstMatch {
				best = []int{th.start, pt.pos}
				break
			}
			matches := false
			switch inst.Op {
			case syntax.InstRune, syntax.InstRune1:
				matches = inst.MatchRune(pt.r)
			case syntax.InstRuneAny:
				matches = pt.r >= 0
			case syntax.InstRuneAnyNotNL:
				matches = pt.r >= 0 && pt.r != '\n'
			}
			if matches && pt.r >= 0 {
				current = append(current, matchThread{inst.Out, th.start})
			}
		}
		if best != nil && len(current) == 0 {
			return best, nil
		}
	}
	return best, nil
}
func (p *boundaryProgram) ranges(ctx context.Context, line string) ([][2]int, error) {
	points := make([]matchPoint, 0, len(line)+1)
	for pos, r := range line {
		if pos%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		points = append(points, matchPoint{pos, r})
	}
	points = append(points, matchPoint{len(line), -1})
	out := [][2]int{}
	offset := 0
	for offset <= len(line) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, err := p.find(ctx, points, offset)
		if err != nil {
			return nil, err
		}
		if m == nil {
			break
		}
		if m[1] <= m[0] { // Suppress empty-only results while retaining later non-empty matches.
			i := sort.Search(len(points), func(i int) bool { return points[i].pos > m[1] })
			if i == len(points) {
				break
			}
			offset = points[i].pos
			continue
		}
		out = append(out, [2]int{m[0], m[1]})
		if len(out) > 1000 {
			break
		}
		offset = m[1]
	}
	return out, nil
}

func parsePattern(p Pattern) (*syntax.Regexp, error) {
	expr := p.Text
	if p.FixedStrings {
		expr = regexp.QuoteMeta(expr)
	} else {
		var err error
		expr, err = unicodePattern(expr)
		if err != nil {
			return nil, err
		}
	}
	flags := patternFlags
	if p.IgnoreCase {
		flags |= syntax.FoldCase
	}
	re, e := syntax.Parse(expr, flags)
	if e != nil {
		return nil, fmt.Errorf("%w", e)
	}
	return lineRegexp(re)
}
