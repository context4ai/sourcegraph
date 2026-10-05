package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/context4ai/sourcegraph/internal/zoektclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSearchArgumentsSurviveTransportAndErrorsAreToolErrors(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "search-test", Version: "1"}, nil)
	var received []searchInput
	register(server, "search", "test", func(_ context.Context, in searchInput) (any, error) {
		received = append(received, in)
		_, err := zoektclient.PatternQuery(zoektclient.Pattern{Text: in.Pattern, FixedStrings: in.FixedStrings, IgnoreCase: in.IgnoreCase, Globs: in.Glob})
		return map[string]any{"Meta": map[string]any{}, "Result": map[string]any{"Files": []any{}}}, err
	})
	session, ctx := connectTest(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))

	want := searchInput{Repo: "org/repo", Pattern: `say "hi" \\ createStore\(`, IgnoreCase: true, Glob: []string{"*.ts", "!**/*_test.ts"}, Paths: []string{"src"}}
	args := map[string]any{"repo": want.Repo, "pattern": want.Pattern, "ignore_case": true, "glob": want.Glob, "paths": want.Paths}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: args})
	if err != nil || res.IsError || !reflect.DeepEqual(received[0], want) {
		t.Fatalf("arguments changed in transport: %+v %v %+v", res, err, received)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"repo": "org/repo", "pattern": "foo(?=bar)"}})
	if err != nil || !res.IsError {
		t.Fatalf("invalid pattern not reported: %+v %v", res, err)
	}
	var body map[string]string
	if err = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body); err != nil || body["code"] != "INVALID_PATTERN" || len(body) != 2 {
		t.Fatal(body, err)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"repo": "org/repo", "q": "content:foo"}})
	if err == nil && !res.IsError {
		t.Fatal("removed q parameter was accepted")
	}
}
