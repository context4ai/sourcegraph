package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/wasmplugin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPluginConnectionWire(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		value       any
		supplied    bool
		want        string
	}{
		{"auto omitted", "", nil, false, "auto"},
		{"explicit empty", "", []any{}, true, "off"},
		{"connection default", "?plugins=fixture", nil, false, "fixture"},
		{"call overrides", "?plugins=fixture", []any{map[string]any{"name": "other"}}, true, "other"},
		{"call disables", "?plugins=fixture", []any{}, true, "off"},
		{"off wins", "?plugins=off", []any{map[string]any{"name": "other"}}, true, "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
			register(server, "read", "test", func(ctx context.Context, in readInput) (any, error) {
				got := connectionPlugins(ctx, in.Plugins)
				label := "auto"
				if got != nil {
					label = "off"
					if len(got) > 0 {
						label = got[0].Name
					}
				}
				if label != tc.want {
					t.Errorf("got %s want %s", label, tc.want)
				}
				return map[string]string{"selection": label}, nil
			})
			handler := pluginConnectionHandler(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
			session, ctx := connectTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.URL.RawQuery = strings.TrimPrefix(tc.query, "?")
				handler.ServeHTTP(w, r)
			}))
			args := map[string]any{"repo": "org/repo", "path": "doc", "start_line": 1, "end_line": 1}
			if tc.supplied {
				args["plugins"] = tc.value
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: args})
			if err != nil || result.IsError {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

func TestPluginConnectionValidationAndIsolation(t *testing.T) {
	for _, query := range []string{"plugins=", "plugins=foo,foo", "plugins=../foo", "plugins=auto&plugins=off", "plugins=%zz"} {
		w := httptest.NewRecorder()
		pluginConnectionHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid reached handler") })).ServeHTTP(w, httptest.NewRequest("POST", "/mcp?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("%s: %d", query, w.Code)
		}
	}
	if got := connectionPlugins(context.Background(), nil); got != nil {
		t.Fatal("request policy leaked")
	}
	if got := connectionPlugins(context.Background(), []wasmplugin.Request{}); got == nil {
		t.Fatal("empty lost")
	}
}
