package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestReadDefaults(t *testing.T) {
	for _, tc := range []struct {
		raw        string
		start, end int
	}{
		{`{}`, 1, 500}, {`{"start_line":50}`, 50, 549}, {`{"end_line":30}`, 1, 30}, {`{"start_line":2,"end_line":3}`, 2, 3}, {`{"start_line":0}`, 0, 0}, {`{"end_line":0}`, 1, 0},
	} {
		var in readInput
		if err := json.Unmarshal([]byte(tc.raw), &in); err != nil {
			t.Fatal(err)
		}
		a, b := defaultReadRange(in.StartLine, in.EndLine)
		if a != tc.start || b != tc.end {
			t.Fatalf("%s: %d-%d", tc.raw, a, b)
		}
	}
	a, b := defaultReadRange(ptr(int(^uint(0)>>1)), nil)
	if b >= a {
		t.Fatal("overflow")
	}
	for _, schema := range []any{inputSchema[readInput]("read"), inputSchema[readManyInput]("read_many")} {
		data, _ := json.Marshal(schema)
		var raw map[string]any
		json.Unmarshal(data, &raw)
		if raw["properties"].(map[string]any)["files"] != nil {
			raw = raw["properties"].(map[string]any)["files"].(map[string]any)["items"].(map[string]any)
		}
		for _, v := range raw["required"].([]any) {
			if v == "start_line" || v == "end_line" {
				t.Fatal("bounds still required")
			}
		}
	}
}
