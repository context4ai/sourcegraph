package service

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/wasmplugin"
)

func TestRecursivePluginInheritance(t *testing.T) {
	enabled := defaultPluginFixture(t, true, "read", "read_many")
	s, _, _, sha := readManyFixture(t, map[string]string{
		"fixture.sourcegraph.wasm": enabled, "root.md": "root\n",
		"a/fixture.sourcegraph.wasm": enabled, "a/doc.md": "child\n", "a/fixture.json": "42",
		"a/deep/doc.md": "deep\n", "b/doc.md": "sibling\n",
		"a/off/fixture.sourcegraph.wasm": defaultPluginFixture(t, false, "read", "read_many"), "a/off/doc.md": "off\n",
		"a/other.sourcegraph.wasm": enabled,
	})
	setupPlugins(t, s)
	ctx := context.Background()
	cat, err := s.DiscoverPlugins(ctx, "org/repo", sha)
	if err != nil || len(cat.Plugins) != 4 {
		t.Fatalf("catalog %+v %v", cat, err)
	}
	files := []contract.ReadItemRequest{}
	for _, p := range []string{"root.md", "a/doc.md", "a/deep/doc.md", "b/doc.md", "a/off/doc.md"} {
		files = append(files, contract.ReadItemRequest{Path: p, StartLine: 1, EndLine: 1})
	}
	res, err := s.ReadManyWithPlugins(ctx, "org/repo", sha, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Scopes []struct {
			Root string
			Data struct {
				Root  string
				Files []struct{ Path string }
			}
		}
	}
	if err = json.Unmarshal(res.Extensions["fixture"], &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Scopes) != 2 || result.Scopes[0].Root != "" || len(result.Scopes[0].Data.Files) != 2 || result.Scopes[1].Root != "a" || len(result.Scopes[1].Data.Files) != 2 {
		t.Fatalf("inheritance: %s", res.Extensions["fixture"])
	}
	if result.Scopes[1].Data.Files[1].Path != "deep/doc.md" {
		t.Fatalf("relative paths: %+v", result)
	}
	if !strings.Contains(string(res.Extensions["other"]), "off/doc.md") {
		t.Fatal("different name should still inherit")
	}
	explicit, err := s.ReadWithPlugins(ctx, "org/repo", contract.ReadRequest{Revision: sha, Path: "a/off/doc.md", StartLine: 1, EndLine: 1}, []wasmplugin.Request{{Name: "fixture"}})
	if err != nil || !strings.Contains(string(explicit.Extensions["fixture"]), `"root":"a/off"`) {
		t.Fatalf("explicit nearest: %+v %v", explicit, err)
	}
	read, err := s.ReadWithPlugins(ctx, "org/repo", contract.ReadRequest{Revision: sha, Path: "a/deep/doc.md", StartLine: 1, EndLine: 1}, []wasmplugin.Request{{Name: "fixture", Args: map[string]any{"action": "read"}}})
	if err != nil || !strings.Contains(string(read.Extensions["fixture"]), `"data":42`) {
		t.Fatalf("host root: %+v %v", read, err)
	}
}

func TestPluginCatalogCacheCoalescesAndCopies(t *testing.T) {
	s, db, _, sha := readManyFixture(t, map[string]string{"nested/fixture.sourcegraph.wasm": pluginFixture(t), "nested/doc.md": "hello\n"})
	setupPlugins(t, s)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan *pluginSnapshot, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.pluginCatalog(ctx, "org/repo", sha, db.repo)
			if e != nil {
				t.Error(e)
				return
			}
			results <- v
		}()
	}
	wg.Wait()
	close(results)
	var first *pluginSnapshot
	for v := range results {
		if first == nil {
			first = v
		}
		if v != first {
			t.Fatal("concurrent callers did not share discovery")
		}
	}
	a, err := s.DiscoverPlugins(ctx, "org/repo", sha)
	if err != nil {
		t.Fatal(err)
	}
	a.Plugins[0].Name = "changed"
	b, err := s.DiscoverPlugins(ctx, "org/repo", sha)
	if err != nil || b.Plugins[0].Name != "fixture" {
		t.Fatal("caller mutated shared catalog")
	}
}

func TestPluginEmptyCatalogCached(t *testing.T) {
	s, db, _, sha := readManyFixture(t, map[string]string{"doc.md": "hello\n"})
	setupPlugins(t, s)
	a, e := s.pluginCatalog(context.Background(), "org/repo", sha, db.repo)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.pluginCatalog(context.Background(), "org/repo", sha, db.repo)
	if e != nil || a != b || len(b.catalog.Plugins) != 0 {
		t.Fatal("empty discovery not cached")
	}
}

func TestPluginCatalogVersionRemovalAndEligibility(t *testing.T) {
	s, db, bare, sha := readManyFixture(t, map[string]string{"fixture.sourcegraph.wasm": defaultPluginFixture(t, true, "read"), "doc.md": "old\n"})
	setupPlugins(t, s)
	ctx := context.Background()
	old, err := s.DiscoverPlugins(ctx, "org/repo", sha)
	if err != nil || len(old.Plugins) != 1 {
		t.Fatal(old, err)
	}
	tree := readManyTree(t, bare, map[string]string{"doc.md": "new\n"})
	next := readManyGit(t, bare, "", "commit-tree", tree, "-p", sha, "-m", "remove plugin")
	readManyGit(t, bare, "", "update-ref", "refs/heads/main", next)
	db.repo.Versions = append(db.repo.Versions, control.Version{Commit: next})
	db.repo.Heads["main"] = next
	fresh, err := s.DiscoverPlugins(ctx, "org/repo", next)
	if err != nil || len(fresh.Plugins) != 0 {
		t.Fatal(fresh, err)
	}
	old, err = s.DiscoverPlugins(ctx, "org/repo", sha)
	if err != nil || len(old.Plugins) != 1 {
		t.Fatal("old version lost its plugin", err)
	}
	db.repo.Versions = db.repo.Versions[1:]
	if _, err = s.DiscoverPlugins(ctx, "org/repo", sha); err == nil {
		t.Fatal("cached catalog bypassed retention")
	}
}

type pluginWarmStore struct{ *readManyStore }

func (db pluginWarmStore) List(context.Context, string, int) ([]control.Repository, error) {
	return []control.Repository{db.repo}, nil
}

func TestPluginBackgroundStartupAndStop(t *testing.T) {
	s, db, _, _ := readManyFixture(t, map[string]string{"fixture.sourcegraph.wasm": defaultPluginFixture(t, true, "read"), "doc.md": "hello\n"})
	setupPlugins(t, s)
	s.DB = pluginWarmStore{db}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.runPlugins(ctx) }()
	deadline := time.After(5 * time.Second)
	for {
		s.pluginCache.mu.Lock()
		ready := len(s.pluginCache.entries) > 0
		s.pluginCache.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("startup did not prepare the observed head")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("plugin background worker did not stop")
	}
}
