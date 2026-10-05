package service

import (
	"context"
	"encoding/json"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/indexer"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestCacheReuseReplaceRemove(t *testing.T) {
	name := filepath.Join(t.TempDir(), "manifest.json")
	write := func(follow bool) {
		t.Helper()
		b, _ := json.Marshal(indexer.Artifact{Links: &gitstore.LinkSnapshot{Format: 1, Followed: follow}})
		tmp := name + ".new"
		if err := os.WriteFile(tmp, b, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, name); err != nil {
			t.Fatal(err)
		}
	}
	var cache linkManifestCache
	write(true)
	a, err := cache.load(name)
	if err != nil {
		t.Fatal(err)
	}
	b, err := cache.load(name)
	if err != nil || a != b {
		t.Fatal("immutable manifest reparsed", err)
	}
	write(false)
	c, err := cache.load(name)
	if err != nil || c == a || c.Links.Followed {
		t.Fatal("replacement not detected", err)
	}
	os.Remove(name)
	if _, err := cache.load(name); err == nil {
		t.Fatal("removed manifest still served")
	}
}
func TestFullReadIndependentOfManifest(t *testing.T) {
	s, db, _, sha := readManyFixture(t, map[string]string{"docs/a.md": "hello\n"})
	db.repo.Versions = []control.Version{{Commit: sha, Generation: 999, LinkFormat: 1}}
	ctx := context.Background()
	if _, err := s.Read(ctx, "org/repo", contract.ReadRequest{Revision: sha, Path: "docs/a.md", StartLine: 1, EndLine: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(ctx, "org/repo", contract.ListRequest{Revision: sha, Path: "docs", First: 10}); err != nil {
		t.Fatal(err)
	}
	b, err := s.ReadMany(ctx, "org/repo", sha, []contract.ReadItemRequest{{Path: "docs/a.md", StartLine: 1, EndLine: 1}})
	if err != nil || b.Items[0].Error != nil {
		t.Fatalf("%+v %v", b, err)
	}
}
func TestCanonicalFilesOnlyCounts(t *testing.T) {
	target := zoektclient.Target{Repo: "org/repo", RepositoryID: 7}
	m := &gitstore.LinkSnapshot{Links: map[string]gitstore.LinkInfo{"alias": {Target: "real", TargetKind: "file", BlobOID: "oid"}}}
	result := map[string]any{"Files": []any{map[string]any{"FileName": "alias"}, map[string]any{"FileName": "real"}}, "MatchCount": 12, "FileCount": 2}
	if err := canonicalSearchFiles(result, []zoektclient.Target{target}, []selectedScope{{version: control.Version{Links: m}}}); err != nil {
		t.Fatal(err)
	}
	if result["FileCount"] != 1 {
		t.Fatal(result)
	}
	if _, exists := result["MatchCount"]; exists {
		t.Fatal("cannot count deduplicated occurrences from paths alone", result)
	}
	result = map[string]any{"Files": []any{map[string]any{"FileName": "normal"}}, "MatchCount": 12, "FileCount": 1}
	canonicalSearchFiles(result, []zoektclient.Target{target}, []selectedScope{{}})
	if result["MatchCount"] != 12 {
		t.Fatal("native statistics changed", result)
	}
}

func TestSearchReportsLegacyAndMixedLinkCoverage(t *testing.T) {
	for _, mode := range []string{"not_followed", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			sha := strings.Repeat("a", 40)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/list" {
					repos := []any{}
					var query struct{ Q string }
					json.NewDecoder(r.Body).Decode(&query)
					for _, id := range []int{7, 8} {
						name := "snapshot/7"
						if id == 8 {
							name = "snapshot/8"
						}
						if !strings.Contains(query.Q, name+"$") {
							continue
						}
						repos = append(repos, map[string]any{"Repository": map[string]any{"ID": id, "Name": name, "Branches": []any{map[string]any{"Name": "snapshot", "Version": sha}}}, "Stats": map[string]any{"Shards": 1, "Documents": 1}})
					}
					json.NewEncoder(w).Encode(map[string]any{"List": map[string]any{"Repos": repos}})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"Result": map[string]any{"Files": []any{}, "FileCount": 0, "MatchCount": 0}})
			}))
			defer server.Close()
			engine, err := zoektclient.New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			r := control.Repository{Name: "org/repo", Enabled: true, Versions: []control.Version{{Commit: sha, Generation: 7, Shards: 1, Files: 1}}}
			root := t.TempDir()
			if mode == "mixed" {
				r.IndexMode = control.IndexModeMonorepo
				r.PathGroups = []control.PathGroup{{Name: "a", Paths: []string{"a"}}, {Name: "b", Paths: []string{"b"}}}
				r.Scopes = map[string]control.ScopeState{"a": {Versions: []control.Version{{Commit: sha, Generation: 7, Shards: 1, Files: 1, LinkFormat: 1}}}, "b": {Versions: []control.Version{{Commit: sha, Generation: 8, Shards: 1, Files: 1}}}}
				os.MkdirAll(filepath.Join(root, "manifests"), 0700)
				a := indexer.Artifact{Target: zoektclient.Target{Repo: r.Name, Commit: sha, Group: "a", RepositoryID: 7}, Links: &gitstore.LinkSnapshot{Format: 1, Followed: true, Paths: []string{"a"}}}
				b, _ := json.Marshal(a)
				os.WriteFile(filepath.Join(root, "manifests", "7.json"), b, 0600)
			}
			s := &Service{Root: root, Engine: engine, DB: searchBudgetStore{repo: r}}
			value, err := s.Search(context.Background(), r.Name, contract.SearchRequest{Revision: sha, Pattern: "hello"})
			if err != nil {
				t.Fatal(err)
			}
			coverage := value.(map[string]any)["Meta"].(map[string]any)["Coverage"].(map[string]any)
			if coverage["Symlinks"] != mode || coverage["SymlinkExpansionComplete"] != false {
				t.Fatal(coverage)
			}
		})
	}
}

func TestCanonicalObjectConflictIndependentOfResultOrder(t *testing.T) {
	targets := []zoektclient.Target{{Repo: "org/repo", RepositoryID: 7}, {Repo: "org/repo", RepositoryID: 8}}
	scopes := []selectedScope{{version: control.Version{Links: &gitstore.LinkSnapshot{Links: map[string]gitstore.LinkInfo{"a": {Target: "real", TargetKind: "file", BlobOID: "one"}}}}}, {version: control.Version{Links: &gitstore.LinkSnapshot{Links: map[string]gitstore.LinkInfo{"b": {Target: "real", TargetKind: "file", BlobOID: "two"}}}}}}
	for _, reverse := range []bool{false, true} {
		files := []any{map[string]any{"Repository": targets[0].EngineName(), "FileName": "real"}, map[string]any{"Repository": targets[1].EngineName(), "FileName": "b"}}
		if reverse {
			files[0], files[1] = files[1], files[0]
		}
		if err := canonicalSearchFiles(map[string]any{"Files": files}, targets, scopes); err == nil {
			t.Fatal("conflicting canonical/alias objects merged")
		}
	}
}
