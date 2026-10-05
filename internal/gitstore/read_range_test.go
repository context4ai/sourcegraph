package gitstore

import (
	"strings"
	"testing"
)

func TestReadRangeDiagnostics(t *testing.T) {
	for _, q := range []struct {
		start, end int
		parts      []string
	}{
		{0, 10, []string{"0..10", "positive"}},
		{10, 9, []string{"10..9", ">= start_line"}},
	} {
		err := ValidateReadRange("a.ts", q.start, q.end)
		if err == nil {
			t.Fatal("invalid range accepted")
		}
		for _, part := range q.parts {
			if !strings.Contains(err.Error(), part) {
				t.Fatalf("missing %q: %v", part, err)
			}
		}
	}
	for _, q := range [][2]int{{1, 620}, {410, 1030}, {1, 500}, {410, 909}, {1, 1}} {
		if err := ValidateReadRange("a.ts", q[0], q[1]); err != nil {
			t.Fatal(err)
		}
	}
}
