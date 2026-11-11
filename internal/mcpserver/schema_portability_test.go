package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAllToolSchemasPortable(t *testing.T) {
	session, ctx := connectTest(t, New(nil))
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 10 {
		t.Fatalf("audit new tools: got %d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			data, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var raw any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			var walk func(any)
			walk = func(v any) {
				switch v := v.(type) {
				case map[string]any:
					if _, ok := v["type"].([]any); ok {
						t.Errorf("array type in %s", data)
					}
					for _, child := range v {
						walk(child)
					}
				case []any:
					for _, child := range v {
						walk(child)
					}
				}
			}
			walk(raw)
			var schema jsonschema.Schema
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			if _, err := schema.Resolve(nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPortableSchemaValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema *jsonschema.Schema
		raw    string
		valid  bool
	}{
		{"read omitted", inputSchema[readInput]("read"), `{"repo":"org/repo","path":"a"}`, true},
		{"read null", inputSchema[readInput]("read"), `{"repo":"org/repo","path":"a","start_line":null,"end_line":null,"plugins":null}`, true},
		{"read zero", inputSchema[readInput]("read"), `{"repo":"org/repo","path":"a","start_line":0}`, false},
		{"read wrong type", inputSchema[readInput]("read"), `{"repo":"org/repo","path":"a","end_line":"2"}`, false},
		{"plugins empty", inputSchema[readInput]("read"), `{"repo":"org/repo","path":"a","plugins":[]}`, true},
		{"search null", inputSchema[searchInput]("search"), `{"repo":"org/repo","pattern":"x","paths":null,"glob":null}`, true},
		{"search bad items", inputSchema[searchInput]("search"), `{"repo":"org/repo","pattern":"x","paths":[1]}`, false},
		{"diff null", inputSchema[diffInput]("diff"), `{"repo":"org/repo","base":"a","head":"b","first":1,"paths":null}`, true},
		{"diff bound", inputSchema[diffInput]("diff"), `{"repo":"org/repo","base":"a","head":"b","first":101}`, false},
		{"batch missing", inputSchema[readManyInput]("read_many"), `{"repo":"org/repo"}`, false},
		{"batch null", inputSchema[readManyInput]("read_many"), `{"repo":"org/repo","files":null}`, false},
		{"batch empty", inputSchema[readManyInput]("read_many"), `{"repo":"org/repo","files":[]}`, false},
		{"batch nested null", inputSchema[readManyInput]("read_many"), `{"repo":"org/repo","files":[{"path":"a","start_line":null}]}`, true},
		{"batch per item validation", inputSchema[readManyInput]("read_many"), `{"repo":"org/repo","files":[{"path":"a","start_line":0},{"path":"b"}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := tc.schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			var args any
			if err := json.Unmarshal([]byte(tc.raw), &args); err != nil {
				t.Fatal(err)
			}
			err = resolved.Validate(args)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestPortableSchemaWireNullAndEmpty(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	var received []readInput
	register(server, "read", "test", func(_ context.Context, in readInput) (any, error) {
		received = append(received, in)
		return map[string]any{}, nil
	})
	batchCalls := 0
	register(server, "read_many", "test", func(_ context.Context, in readManyInput) (any, error) { batchCalls++; return map[string]any{}, nil })
	session, ctx := connectTest(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	for _, raw := range []string{
		`{"repo":"org/repo","path":"a"}`,
		`{"repo":"org/repo","path":"a","plugins":null,"start_line":null}`,
		`{"repo":"org/repo","path":"a","plugins":[],"start_line":2}`,
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: json.RawMessage(raw)})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("unexpected tool error: %+v", result)
		}
	}
	if len(received) != 3 || received[0].Plugins != nil || received[1].Plugins != nil || received[2].Plugins == nil || received[1].StartLine != nil || *received[2].StartLine != 2 {
		t.Fatalf("changed defaults: %+v", received)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_many", Arguments: json.RawMessage(`{"repo":"org/repo","files":null}`)})
	if err != nil || result == nil || !result.IsError || batchCalls != 0 {
		t.Fatalf("null batch reached handler: calls=%d err=%v", batchCalls, err)
	}
}
