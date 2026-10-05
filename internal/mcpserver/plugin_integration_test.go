package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/context4ai/sourcegraph/internal/wasmplugin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type pluginWireStore struct {
	service.Store
	repo control.Repository
}

func (db pluginWireStore) Get(context.Context, string) (control.Repository, error) {
	return db.repo, nil
}

// Real Git + external Wasm + Service + HTTP MCP SDK, with no database or remote
// repository mutation. The optional artifact belongs to the plugin owner.
func TestExternalPluginContentOnlyHTTP(t *testing.T) {
	artifact := os.Getenv("SOURCEGRAPH_TEST_PLUGIN_WASM")
	if artifact == "" {
		t.Skip("external ABI 2 artifact not supplied")
	}
	wasm, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bare, err := gitstore.RepositoryDir(root, "org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(bare, 0750); err != nil {
		t.Fatal(err)
	}
	git := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"--git-dir=" + bare}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
		cmd.Stdin = strings.NewReader(input)
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("", "init", "--bare")
	files := map[string]string{
		"long.txt":                          strings.Repeat("bounded-line\n", 600),
		"context-evidence.sourcegraph.wasm": string(wasm),
		"knowledge/example.md":              "<!-- context:section id=\"example\" -->\nUnique body evidence\n<!-- /context:section -->\n",
		"knowledge/structure.yaml":          "schema_version: context.approved-structure.v1\narticles:\n  - path: example.md\n    sections:\n      - id: example\n        references:\n          - source_ref: repo:org/source\n            locator:\n              path: example.go\n              start_line: 2\n              end_line: 4\n",
		"sources/repo/index.yaml":           "sources:\n  - name: org\n    modules:\n      - name: source\n        subpath: src\n        git:\n          remote: https://example.org/org/source.git\n          ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
	}
	for path, data := range files {
		oid := git(data, "hash-object", "-w", "--stdin")
		git("", "update-index", "--add", "--cacheinfo", "100644", oid, path)
	}
	tree := git("", "write-tree")
	sha := git("", "commit-tree", tree, "-m", "fixture")
	git("", "update-ref", "refs/heads/main", sha)
	store, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	host := wasmplugin.New(wasmplugin.DefaultConfig())
	t.Cleanup(host.Close)
	s := &service.Service{Root: root, Git: store, Plugins: host, DB: pluginWireStore{repo: control.Repository{Name: "org/repo", Enabled: true, Policy: control.Policy{Branches: []string{"main"}}, Heads: map[string]string{"main": sha}, Versions: []control.Version{{Commit: sha, Generation: 1}}}}}
	session, ctx := connectTest(t, New(s))
	for _, mode := range []string{"auto", "explicit", "off", "default-range"} {
		args := map[string]any{"repo": "org/repo", "revision": sha, "files": []any{map[string]any{"path": "knowledge/example.md", "start_line": 2, "end_line": 2}, map[string]any{"path": "missing", "start_line": 1, "end_line": 1}, map[string]any{"path": "knowledge/example.md", "start_line": 2, "end_line": 2}}}
		if mode == "default-range" {
			for _, item := range args["files"].([]any) {
				m := item.(map[string]any)
				delete(m, "start_line")
				delete(m, "end_line")
			}
		}
		if mode == "off" {
			args["plugins"] = []any{}
		} else if mode == "explicit" {
			args["plugins"] = []any{map[string]any{"name": "context-evidence"}}
		}
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_many", Arguments: args})
		if err != nil || res.IsError || res.StructuredContent != nil || len(res.Content) != 3 {
			t.Fatalf("%s: %v %+v", mode, err, res)
		}
		for _, i := range []int{0, 2} {
			text := res.Content[i].(*mcp.TextContent).Text
			if strings.Count(text, "Unique body evidence") != 1 || strings.Contains(text, "section_id") || strings.Contains(text, "item_id") || !strings.Contains(text, sha) {
				t.Fatal(text)
			}
			count := strings.Count(text, "https://example.org/org/source/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/src/example.go#L2-L4")
			want := 1
			if mode == "off" {
				want = 0
			}
			if count != want {
				t.Fatalf("%s evidence: %s", mode, text)
			}
		}
		if !strings.Contains(res.Content[1].(*mcp.TextContent).Text, "FILE_NOT_FOUND") {
			t.Fatal("missing failed item")
		}
	}
	for _, bounds := range []map[string]any{{}, {"start_line": 501}, {"end_line": 3}, {"end_line": 620}, {"start_line": 0}} {
		args := map[string]any{"repo": "org/repo", "path": "long.txt", "plugins": []any{}}
		for k, v := range bounds {
			args[k] = v
		}
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if bounds["start_line"] == 0 {
			if !res.IsError {
				t.Fatal("explicit zero defaulted")
			}
			continue
		}
		if res.IsError {
			t.Fatal(res)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		want := 500
		if bounds["start_line"] == 501 {
			want = 100
		}
		if bounds["end_line"] == 3 {
			want = 3
		}
		if strings.Count(text, "bounded-line") != want {
			t.Fatalf("default range count %d", strings.Count(text, "bounded-line"))
		}
		if want == 500 && !strings.Contains(text, "NextStartLine: 501") {
			t.Fatal("missing continuation")
		}
	}

}
