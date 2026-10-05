package service

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/identity"
	"github.com/context4ai/sourcegraph/internal/wasmplugin"
)

func pluginFixture(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("../wasmplugin/testdata/fixture.wasm")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func setupPlugins(t *testing.T, s *Service) {
	t.Helper()
	s.Plugins = wasmplugin.New(wasmplugin.DefaultConfig())
	t.Cleanup(s.Plugins.Close)
}
func TestPluginReadAndBatch(t *testing.T) {
	s, _, _, sha := readManyFixture(t, map[string]string{"fixture.sourcegraph.wasm": pluginFixture(t), "doc.md": "hello\nworld\n", "fixture.json": `{"evidence":true}`})
	setupPlugins(t, s)
	q := contract.ReadRequest{Path: "doc.md", StartLine: 1, EndLine: 1}
	p := []wasmplugin.Request{{Name: "fixture"}}
	r, e := s.ReadWithPlugins(context.Background(), "org/repo", q, p)
	if e != nil {
		t.Fatal(e)
	}
	var input map[string]any
	if e = json.Unmarshal(r.Extensions["fixture"], &input); e != nil {
		t.Fatal(e)
	}
	if input["commit"] != sha || input["files"].([]any)[0].(map[string]any)["content"] != "hello\n" {
		t.Fatal(input)
	}
	batch, e := s.ReadManyWithPlugins(context.Background(), "org/repo", sha, []contract.ReadItemRequest{{Path: "doc.md", StartLine: 1, EndLine: 1}, {Path: "missing", StartLine: 1, EndLine: 1}}, p)
	if e != nil {
		t.Fatal(e)
	}
	_ = json.Unmarshal(batch.Extensions["fixture"], &input)
	if len(input["files"].([]any)) != 1 || input["operation"] != "read_many" {
		t.Fatal(input)
	}
	if batch.Result.(ReadManyResult).Items[1].Error == nil {
		t.Fatal("lost base error")
	}
	catalog, e := s.DiscoverPlugins(context.Background(), "org/repo", sha)
	if e != nil || len(catalog.Plugins) != 1 {
		t.Fatalf("%+v %v", catalog, e)
	}
	for _, name := range []string{"missing", "fixture"} {
		p := []wasmplugin.Request{{Name: name, Args: map[string]any{"action": "invalid"}}}
		r, e := s.ReadWithPlugins(context.Background(), "org/repo", q, p)
		if e != nil || r.Result.(gitstore.File).Content != "hello\n" || !strings.Contains(string(r.Extensions[name]), "issues") {
			t.Fatalf("lost base read: %+v %v", r, e)
		}
	}
	if _, e = s.ReadWithPlugins(context.Background(), "org/repo", q, []wasmplugin.Request{{Name: "fixture"}, {Name: "fixture"}}); e == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestPluginMonorepoRoots(t *testing.T) {
	files := map[string]string{"a/fixture.sourcegraph.wasm": defaultPluginFixture(t, true, "read", "read_many"), "a/doc.md": "a\n", "a/fixture.json": "42", "b/fixture.sourcegraph.wasm": pluginFixture(t), "b/doc.md": "b\n", "b/fixture.json": "43", "secret.json": "true"}
	files["outside.sourcegraph.wasm"] = pluginFixture(t)
	files["a/nested/fixture.sourcegraph.wasm"] = defaultPluginFixture(t, true, "read")
	files["a/nested/doc.md"] = "nested\n"
	s, db, bare, sha := readManyFixture(t, files)
	setupPlugins(t, s)
	db.repo.IndexMode = control.IndexModeMonorepo
	db.repo.PathGroups = []control.PathGroup{{Name: "business", Paths: []string{"a", "b"}}}
	// Construct an independently hydrated group using real Git objects.
	groupPath, e := gitstore.GroupRepositoryDir(s.Root, "org/repo", "business")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(groupPath), 0750); e != nil {
		t.Fatal(e)
	}
	readManyGit(t, groupPath, "", "clone", "--bare", bare, groupPath)
	scoped := db.repo.Scope("business")
	scoped.Versions = db.repo.Versions
	scoped.Heads = db.repo.Heads
	db.repo.SetScope("business", scoped)
	auto, err := s.ReadManyWithPlugins(context.Background(), "org/repo", sha, []contract.ReadItemRequest{{Path: "a/doc.md", StartLine: 1, EndLine: 1}, {Path: "b/doc.md", StartLine: 1, EndLine: 1}}, nil)
	if err != nil || !strings.Contains(string(auto.Extensions["fixture"]), `"root":"a"`) || strings.Contains(string(auto.Extensions["fixture"]), `"root":"b"`) {
		t.Fatalf("scoped defaults: %+v %v", auto, err)
	}
	q := contract.ReadRequest{Path: "a/doc.md", StartLine: 1, EndLine: 1}
	r, e := s.ReadWithPlugins(context.Background(), "org/repo", q, []wasmplugin.Request{{Name: "fixture", Args: map[string]any{"action": "read"}}})
	if e != nil || !strings.Contains(string(r.Extensions["fixture"]), `"root":"a","data":42`) {
		t.Fatalf("relative root: %+v %v", r, e)
	}
	r, e = s.ReadWithPlugins(context.Background(), "org/repo", q, []wasmplugin.Request{{Name: "fixture", Args: map[string]any{"action": "escape"}}})
	if e != nil || !strings.Contains(string(r.Extensions["fixture"]), `"denied":true`) {
		t.Fatalf("escape: %+v %v", r, e)
	}
	batch, e := s.ReadManyWithPlugins(context.Background(), "org/repo", sha, []contract.ReadItemRequest{{Path: "a/doc.md", StartLine: 1, EndLine: 1}, {Path: "b/doc.md", StartLine: 1, EndLine: 1}}, []wasmplugin.Request{{Name: "fixture", Args: map[string]any{"action": "read"}}})
	if e != nil || !strings.Contains(string(batch.Extensions["fixture"]), `"root":"a","data":42`) || !strings.Contains(string(batch.Extensions["fixture"]), `"root":"b","data":43`) {
		t.Fatalf("scopes: %+v %v", batch, e)
	}
	catalog, e := s.DiscoverPlugins(context.Background(), "org/repo", sha)
	if e != nil || len(catalog.Plugins) != 3 {
		t.Fatalf("catalog %+v %v", catalog, e)
	}
}
func TestPluginReadRechecksAnonymousPolicy(t *testing.T) {
	s, db, _, _ := readManyFixture(t, map[string]string{"fixture.sourcegraph.wasm": pluginFixture(t), "doc.md": "hello"})
	setupPlugins(t, s)
	public := true
	db.repo.PublicRead = &public
	ctx := identity.WithAnonymous(context.Background(), "test")
	q := contract.ReadRequest{Path: "doc.md", StartLine: 1, EndLine: 1}
	p := []wasmplugin.Request{{Name: "fixture"}}
	if _, e := s.ReadWithPlugins(ctx, "org/repo", q, p); e != nil {
		t.Fatal(e)
	}
	public = false
	if _, e := s.ReadWithPlugins(ctx, "org/repo", q, p); e == nil {
		t.Fatal("anonymous private read accepted")
	}
}

// Optional sibling artifact verification: core tests don't depend on Context.
// The integration owner supplies its freshly built Wasm through this variable.
func TestExternalPluginContract(t *testing.T) {
	artifact := os.Getenv("SOURCEGRAPH_TEST_PLUGIN_WASM")
	if artifact == "" {
		t.Skip("set SOURCEGRAPH_TEST_PLUGIN_WASM for external plugin integration")
	}
	b, e := os.ReadFile(artifact)
	if e != nil {
		t.Fatal(e)
	}
	files := map[string]string{
		"context-evidence.sourcegraph.wasm": string(b),
		"knowledge/example.md":              "<!-- context:section id=\"example\" -->\nDocumented behavior\n<!-- /context:section -->\n",
		"knowledge/structure.yaml":          "schema_version: context.approved-structure.v1\narticles:\n  - path: example.md\n    sections:\n      - id: example\n        references:\n          - source_ref: repo:org/source\n            locator:\n              path: example.go\n              start_line: 2\n              end_line: 4\n",
		"sources/repo/index.yaml":           "sources:\n  - name: org\n    modules:\n      - name: source\n        subpath: src\n        git:\n          remote: https://example.org/org/source.git\n          ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
	}
	s, _, _, sha := readManyFixture(t, files)
	setupPlugins(t, s)
	for _, selection := range [][]wasmplugin.Request{nil, {{Name: "context-evidence"}}} {
		out, e := s.ReadWithPlugins(context.Background(), "org/repo", contract.ReadRequest{Revision: sha, Path: "knowledge/example.md", StartLine: 2, EndLine: 2}, selection)
		if e != nil {
			t.Fatal(e)
		}
		data, _ := json.Marshal(out.Attachments)
		text := string(data)
		if len(out.Issues) != 0 || len(out.Attachments) != 1 || out.Attachments[0].ItemID != "read-0" || !strings.Contains(text, `https://example.org/org/source/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/src/example.go#L2-L4`) {
			t.Fatalf("external contract: %s", text)
		}
	}
}

// A scoped plugin sees the same virtual root as the full-repository fixture.
func TestExternalPluginMonorepoContract(t *testing.T) {
	artifact := os.Getenv("SOURCEGRAPH_TEST_PLUGIN_WASM")
	if artifact == "" {
		t.Skip("external plugin not supplied")
	}
	b, e := os.ReadFile(artifact)
	if e != nil {
		t.Fatal(e)
	}
	files := map[string]string{
		"docs/context-evidence.sourcegraph.wasm": string(b),
		"docs/knowledge/example.md":              "<!-- context:section id=\"example\" -->\nScoped article\n<!-- /context:section -->\n",
		"docs/knowledge/structure.yaml":          "schema_version: context.approved-structure.v1\narticles:\n  - path: example.md\n    sections:\n      - id: example\n        references: []\n",
	}
	s, db, bare, sha := readManyFixture(t, files)
	setupPlugins(t, s)
	db.repo.IndexMode = control.IndexModeMonorepo
	db.repo.PathGroups = []control.PathGroup{{Name: "docs", Paths: []string{"docs"}}}
	group, e := gitstore.GroupRepositoryDir(s.Root, "org/repo", "docs")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(group), 0750); e != nil {
		t.Fatal(e)
	}
	readManyGit(t, group, "", "clone", "--bare", bare, group)
	scope := db.repo.Scope("docs")
	scope.Versions = db.repo.Versions
	scope.Heads = db.repo.Heads
	db.repo.SetScope("docs", scope)
	for _, selection := range [][]wasmplugin.Request{nil, {{Name: "context-evidence"}}} {
		out, e := s.ReadManyWithPlugins(context.Background(), "org/repo", sha, []contract.ReadItemRequest{{Path: "docs/knowledge/example.md", StartLine: 2, EndLine: 2}}, selection)
		if e != nil {
			t.Fatal(e)
		}
		data, _ := json.Marshal(out.Attachments)
		text := string(data)
		if len(out.Issues) != 0 || len(out.Attachments) != 0 {
			t.Fatalf("scoped contract: %s", text)
		}
	}
}

func TestPluginLeaseAndFixedSHA(t *testing.T) {
	s, _, bare, sha := readManyFixture(t, map[string]string{"doc.md": "old", "fixture.sourcegraph.wasm": pluginFixture(t), "fixture.json": "42"})
	setupPlugins(t, s)
	tree := readManyTree(t, bare, map[string]string{"doc.md": "new", "fixture.json": "43"})
	next := readManyGit(t, bare, "", "commit-tree", tree, "-p", sha, "-m", "next")
	readManyGit(t, bare, "", "update-ref", "refs/heads/main", next)
	out, e := s.ReadWithPlugins(context.Background(), "org/repo", contract.ReadRequest{Path: "doc.md", StartLine: 1, EndLine: 1}, []wasmplugin.Request{{Name: "fixture", Args: map[string]any{"action": "read"}}})
	if e != nil || string(out.Extensions["fixture"]) != "42" || out.Result.(gitstore.File).Content != "old" {
		t.Fatalf("mixed SHA: %+v %v", out, e)
	}
	release, e := s.pluginLease(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.preparation.TryLock() {
		s.preparation.Unlock()
		t.Fatal("snapshot was not pinned")
	}
	if _, e = s.Maintenance(context.Background(), "org/repo", "delete", "", 1); e == nil {
		t.Fatal("delete allowed during plugin lease")
	}
	release()
	if !s.preparation.TryLock() {
		t.Fatal("lease leaked")
	}
	s.preparation.Unlock()
}

// Append an advisory custom section without recompiling the real test guest.
func defaultPluginFixture(t *testing.T, enabled bool, operations ...string) string {
	b := []byte(pluginFixture(t))
	metadata, _ := json.Marshal(map[string]any{"abi_version": 2, "default_enabled": enabled, "operations": operations})
	section := binary.AppendUvarint(nil, uint64(len("sourcegraph.plugin.v1")))
	section = append(section, []byte("sourcegraph.plugin.v1")...)
	section = append(section, metadata...)
	b = append(b, 0)
	b = binary.AppendUvarint(b, uint64(len(section)))
	return string(append(b, section...))
}

func TestPluginAutomaticSelection(t *testing.T) {
	s, _, _, sha := readManyFixture(t, map[string]string{
		"fixture.sourcegraph.wasm":  defaultPluginFixture(t, true, "read", "read_many"),
		"disabled.sourcegraph.wasm": defaultPluginFixture(t, false, "read"),
		"other-op.sourcegraph.wasm": defaultPluginFixture(t, true, "search"),
		"doc.md":                    "hello\n",
	})
	setupPlugins(t, s)
	q := contract.ReadRequest{Revision: sha, Path: "doc.md", StartLine: 1, EndLine: 1}
	for _, tc := range []struct {
		name    string
		plugins []wasmplugin.Request
		want    string
	}{
		{"auto", nil, "fixture"}, {"off", []wasmplugin.Request{}, ""}, {"explicit", []wasmplugin.Request{{Name: "disabled"}}, "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, e := s.ReadWithPlugins(context.Background(), "org/repo", q, tc.plugins)
			if e != nil {
				t.Fatal(e)
			}
			if tc.want == "" {
				if len(r.Extensions) != 0 {
					t.Fatal(r.Extensions)
				}
			} else if len(r.Extensions) != 1 || r.Extensions[tc.want] == nil {
				t.Fatal(r.Extensions)
			}
		})
	}
	batch, e := s.ReadManyWithPlugins(context.Background(), "org/repo", sha, []contract.ReadItemRequest{{Path: "doc.md", StartLine: 1, EndLine: 1}, {Path: "missing", StartLine: 1, EndLine: 1}}, nil)
	if e != nil || len(batch.Extensions) != 1 || batch.Extensions["fixture"] == nil {
		t.Fatalf("%+v %v", batch, e)
	}
	if batch.Result.(ReadManyResult).Items[1].Error == nil {
		t.Fatal("lost base failure")
	}
}
