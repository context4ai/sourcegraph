package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCompactCatalogCursorAndFields(t *testing.T) {
	rows := make([]service.Summary, 100)
	for i := range rows {
		rows[i] = service.Summary{ID: "internal-id", Name: "org/repo", Enabled: true, DefaultBranch: "main", Heads: map[string]string{"main": "sha"}, DiskUsage: &control.DiskUsage{Bytes: 123}}
	}
	out := compactCatalog(rows)
	if out["next_cursor"] != "internal-id" {
		t.Fatal("lost pagination")
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "disk_usage") || strings.Contains(string(b), "repo_id") {
		t.Fatal(string(b))
	}
	if _, ok := compactCatalog(rows[:1])["next_cursor"]; ok {
		t.Fatal("spurious next page")
	}
}
func TestCompactAvailabilityPollingAndGroups(t *testing.T) {
	now := time.Now().UTC()
	r := control.Repository{Name: "org/repo", Enabled: true, IndexMode: "monorepo", PathGroups: []control.PathGroup{{Name: "a", Paths: []string{"a"}}, {Name: "b", Paths: []string{"b"}}}, Scopes: map[string]control.ScopeState{"a": {Versions: []control.Version{{Commit: "sha", Indexed: true}}}, "b": {Versions: []control.Version{{Commit: "sha", Indexed: false}}}}, Jobs: []control.Job{{ID: "old-failure", State: "failed", Error: "FAILED", Updated: now.Add(-time.Hour)}, {ID: "success", State: "succeeded", Updated: now}, {ID: "active", State: "running", Group: "a", Stage: "indexing", Updated: now}}, SyncChecks: map[string]control.SyncCheck{"": {CheckedAt: now.Add(time.Minute)}}}
	out := compactAvailability(r, "")
	if len(out["jobs"].([]any)) != 2 {
		t.Fatal(out)
	}
	groups := out["groups"].([]any)
	if groups[0].(map[string]any)["versions"].([]any)[0].(map[string]any)["indexed"] != true || groups[1].(map[string]any)["versions"].([]any)[0].(map[string]any)["indexed"] != false {
		t.Fatal("lost scope readiness")
	}
	exact := compactAvailability(r, "old-failure")["jobs"].([]any)
	if len(exact) != 1 || exact[0].(map[string]any)["error"] != "FAILED" {
		t.Fatal("lost terminal result")
	}
	if compactAvailability(r, "expired")["job_lookup"] == nil {
		t.Fatal("missing task silently treated as success")
	}
	if r.Jobs[0].ID != "old-failure" {
		t.Fatal("mutated source")
	}
	r.Jobs = r.Jobs[:1]
	if len(compactAvailability(r, "")["jobs"].([]any)) != 0 {
		t.Fatal("recovered failure reappears")
	}
}
func TestCompactListDiffPreserveContinuation(t *testing.T) {
	tree := gitstore.Tree{Commit: "sha", TreeOID: "internal-tree", Entries: []gitstore.Entry{{Name: "a", Path: "src/a", Kind: "file", BlobOID: "internal-blob", Size: ptr(int64(0))}}, HasMoreResults: true, NextCursor: "cursor"}
	out := compactList(tree)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "internal-") || out["next_cursor"] != "cursor" || out["has_more"] != true {
		t.Fatal(string(b))
	}
	diff := compactDiff(gitstore.Diff{BaseCommit: "base", HeadCommit: "head", Changes: []gitstore.Change{{Path: "a", OldOID: "old-object", NewOID: "new-object", ChangeKind: "M", Patch: "@@ -1 +1 @@\n-old\n+new", PatchTruncated: true}, {Path: "b", Binary: true}}, HasMoreResults: true, NextCursor: "diff-cursor"})
	b, _ = json.Marshal(diff)
	if strings.Contains(string(b), "old-object") || !strings.Contains(string(b), `"patch_truncated":true`) || !strings.Contains(string(b), `"binary":true`) || diff["next_cursor"] != "diff-cursor" {
		t.Fatal(string(b))
	}
}
func TestCompactResolveWire(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	register(server, "resolve", "test", func(context.Context, resolveInput) (any, error) {
		return control.Version{Commit: strings.Repeat("a", 40), Indexed: false, Files: 100, Shards: 4}, nil
	})
	session, ctx := connectTest(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "resolve", Arguments: map[string]any{"repo": "org/repo"}})
	if err != nil || res.IsError || res.StructuredContent != nil {
		t.Fatal(res, err)
	}
	var out map[string]any
	if err = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out["indexed"] != false || out["commit"] != strings.Repeat("a", 40) {
		t.Fatal(out)
	}
}
