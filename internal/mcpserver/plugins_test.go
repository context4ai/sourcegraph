package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPluginWireAppendsTextOnce(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	register(server, "read", "test", func(_ context.Context, in readInput) (any, error) {
		if len(in.Plugins) != 1 || in.Plugins[0].Args["arbitrary"] != "value" {
			t.Fatalf("input lost: %+v", in)
		}
		return service.EnhancedRead{Result: gitstore.File{Content: "unique-original-body", ReturnedStartLine: ptr(1), ReturnedEndLine: ptr(1)}, Attachments: []service.PluginAttachment{{ItemID: "read-0", Text: "references: unique-evidence"}}}, nil
	})
	session, ctx := connectTest(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: map[string]any{"repo": "org/repo", "path": "doc", "start_line": 1, "end_line": 2, "plugins": []any{map[string]any{"name": "fixture", "args": map[string]any{"arbitrary": "value"}}}}})
	if err != nil || result.IsError {
		t.Fatalf("call: %+v %v", result, err)
	}
	b, _ := json.Marshal(result)
	if strings.Count(string(b), "unique-original-body") != 1 || strings.Count(string(b), "unique-evidence") != 1 || result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("projection: %s", b)
	}
}

func TestPluginTextProximityAndBatchOrdering(t *testing.T) {
	blocks := []string{"first body\n", "failed item\n", "duplicate-path body\n"}
	got := appendPluginText(blocks, service.EnhancedRead{Attachments: []service.PluginAttachment{{ItemID: "read-2", Text: "second-range references"}, {Text: "batch supplement"}, {ItemID: "read-0", Text: "first references"}, {ItemID: "read-0", Text: "more references"}}})
	if len(got) != 4 || got[0] != "first body\n\nfirst references\n\nmore references" || got[1] != "failed item\n" || got[2] != "duplicate-path body\n\nsecond-range references" || got[3] != "batch supplement" {
		t.Fatalf("%q", got)
	}
}
