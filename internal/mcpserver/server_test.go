package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectTest(t *testing.T, h http.Handler) (*mcp.ClientSession, context.Context) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, ctx
}

func TestToolDiscoveryAndReadOnlyBoundary(t *testing.T) {
	for _, writable := range []bool{false, true} {
		t.Run(map[bool]string{false: "readonly", true: "writable"}[writable], func(t *testing.T) {
			h := NewReadOnly(nil)
			if writable {
				h = New(nil)
			}
			session, ctx := connectTest(t, h)
			list, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			seen := false
			batchSeen := false
			for _, tool := range list.Tools {
				batchSeen = batchSeen || tool.Name == "read_many"
				seen = seen || tool.Name == "prepare"
				if tool.OutputSchema != nil {
					t.Fatalf("unexpected output schema: %v", tool.OutputSchema)
				}
			}
			if !batchSeen {
				t.Fatal("batch reading missing from tool discovery")
			}
			if seen != writable {
				t.Fatalf("prepare listed=%v, writable=%v", seen, writable)
			}
			if !writable {
				_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "prepare", Arguments: map[string]any{"repo": "org/repo", "idempotency_key": "test-readonly"}})
				if err == nil || !strings.Contains(err.Error(), `unknown tool "prepare"`) {
					t.Fatalf("expected unknown prepare tool, got %v", err)
				}
			}
		})
	}
}

func TestSearchWireMetadataDoesNotDuplicateCode(t *testing.T) {
	code := "const uniqueCode = 1;"
	encoded := base64.StdEncoding.EncodeToString([]byte(code))
	original := map[string]any{
		"Meta":   map[string]any{"Commit": "abc", "Partial": false, "Truncated": false, "Coverage": map[string]any{"CompleteRepository": false}},
		"Result": map[string]any{"MatchCount": 1, "Files": []any{map[string]any{"FileName": "src/a.ts", "Content": encoded, "LineMatches": []any{map[string]any{"Line": encoded, "Before": encoded, "After": encoded, "LineNumber": 7}}}}},
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	register(server, "search", "test", func(context.Context, searchInput) (any, error) { return original, nil })
	session, ctx := connectTest(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"repo": "org/repo", "pattern": "uniqueCode"}})
	if err != nil || result.IsError {
		t.Fatalf("search failed: %v %#v", err, result)
	}
	if result.StructuredContent != nil {
		t.Fatal("unexpected duplicate result")
	}

	if len(result.Content) != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "7: "+code) {
		t.Fatal("missing readable code")
	}
	data, _ := json.Marshal(original)
	if !strings.Contains(string(data), encoded) {
		t.Fatal("adapter mutated original HTTP response")
	}
}

func TestSearchTextBudget(t *testing.T) {
	value := map[string]any{"Meta": map[string]any{}, "Result": map[string]any{"Files": []any{map[string]any{"FileName": "large", "LineMatches": []any{map[string]any{"LineNumber": 1, "Line": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", searchTextLimit*2)))}}}}}}
	text := searchText(value)
	if len(text) > searchTextLimit || !strings.Contains(text, "LineTruncated: true") {
		t.Fatal("text budget not enforced")
	}
}

func TestPrepareStillChecksAuthorization(t *testing.T) {
	calls := 0
	session, ctx := connectTest(t, NewWithPrepareAuthorization(nil, func(context.Context) error {
		calls++
		return contract.Fail("ACCESS_DENIED", "Preparation denied.", 403)
	}))
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "prepare", Arguments: map[string]any{"repo": "org/repo", "idempotency_key": "guard-check"}})
	if err != nil || result == nil || !result.IsError || calls != 1 {
		t.Fatalf("guard failed: %v %#v calls=%d", err, result, calls)
	}
	if !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "ACCESS_DENIED") {
		t.Fatal("error contract changed")
	}
}
