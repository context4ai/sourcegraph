package service

import (
	"context"
	"encoding/json"
	"github.com/context4ai/sourcegraph/internal/contract"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/indexer"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
)

func TestCanonicalSearchPreservesPathsAndAliases(t *testing.T) {
	target := zoektclient.Target{Repo: "org/repo", Commit: "1111111111111111111111111111111111111111", RepositoryID: 1}
	m := &gitstore.LinkSnapshot{Format: 1, Followed: true, Links: map[string]gitstore.LinkInfo{"context/a": {Target: "docs/a", TargetKind: "file", BlobOID: "oid"}, "context/b": {Target: "docs/a", TargetKind: "file", BlobOID: "oid"}}}
	scopes := []selectedScope{{version: control.Version{Links: m}}}
	line := map[string]any{"LineNumber": float64(1), "LineFragments": []any{map[string]any{"LineOffset": float64(0), "MatchLength": float64(3)}}}
	files := []any{}
	for _, p := range []string{"context/a", "docs/a", "context/b", "docs/same-content"} {
		files = append(files, map[string]any{"FileName": p, "Repository": target.EngineName(), "LineMatches": []any{line}})
	}
	native := map[string]any{"Files": files}
	if err := canonicalSearchFiles(native, []zoektclient.Target{target}, scopes); err != nil {
		t.Fatal(err)
	}
	out := native["Files"].([]any)
	if len(out) != 2 || native["MatchCount"] != 2 {
		t.Fatalf("%+v", native)
	}
	f := out[0].(map[string]any)
	if f["FileName"] != "docs/a" || !reflect.DeepEqual(f["Via"], []string{"context/a", "context/b"}) {
		t.Fatalf("%+v", f)
	}
	cov := linkCoverage(scopes)
	m.UnsupportedPathsSkipped = true
	if linkCoverage(scopes)["UnsupportedPathsSkipped"] != true {
		t.Fatal("unsupported path coverage hidden")
	}
	if cov["Symlinks"] != "followed" || cov["SymlinkExpansionComplete"] != true {
		t.Fatal(cov)
	}
	m.Skipped = map[string]int{"LINK_LOOP": 1}
	if linkCoverage(scopes)["SymlinkExpansionComplete"] != false {
		t.Fatal("partial expansion reported complete")
	}
	scopes = append(scopes, selectedScope{})
	if linkCoverage(scopes)["Symlinks"] != "mixed" {
		t.Fatal("mixed coverage hidden")
	}
}
func TestLinkManifestRecoveryAndOverlap(t *testing.T) {
	sha := "1111111111111111111111111111111111111111"
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "manifests"), 0700)
	m := &gitstore.LinkSnapshot{Format: 1, Followed: true, Paths: []string{"context", "docs"}, Links: map[string]gitstore.LinkInfo{}}
	artifact := indexer.Artifact{Target: zoektclient.Target{Repo: "org/repo", Commit: sha, RepositoryID: 7, Group: "context"}, Links: m}
	data, _ := json.Marshal(artifact)
	os.WriteFile(filepath.Join(root, "manifests", "7.json"), data, 0600)
	r := control.Repository{Name: "org/repo", IndexMode: control.IndexModeMonorepo, PathGroups: []control.PathGroup{{Name: "context", Paths: []string{"context"}}, {Name: "docs", Paths: []string{"docs"}}}, Scopes: map[string]control.ScopeState{"context": {Versions: []control.Version{{Commit: sha, Generation: 7, LinkFormat: 1}}}, "docs": {Versions: []control.Version{{Commit: sha, Generation: 8}}}}}
	s := &Service{Root: root}
	view, err := s.contentRepository(r, sha)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := readScope(view, sha, "docs/a.md")
	if err != nil || scope.name != "docs" {
		t.Fatalf("prefer explicit: %+v %v", scope, err)
	}
	delete(view.Scopes, "docs")
	scope, err = readScope(view, sha, "docs/a.md")
	if err != nil || scope.name != "context" {
		t.Fatalf("ready expansion: %+v %v", scope, err)
	}
	if r.Scopes["context"].Versions[0].Links != nil {
		t.Fatal("request altered durable repository")
	}
	os.Remove(filepath.Join(root, "manifests", "7.json"))
	broken, err := s.contentRepository(r, sha)
	if err != nil {
		t.Fatal(err)
	}
	if !broken.Scopes["context"].Versions[0].LinkUnavailable {
		t.Fatal("missing new manifest accepted")
	}
	scope, err = readScope(broken, sha, "docs/a.md")
	if err != nil || scope.name != "docs" {
		t.Fatalf("unrelated broken scope blocked ready read: %+v %v", scope, err)
	}
	if _, err = readScope(broken, sha, "context/a.md"); err == nil {
		t.Fatal("broken snapshot still readable")
	}
}

func TestPreparedMonorepoLinkReadAndList(t *testing.T) {
	s, db, dir, sha := readManyFixture(t, map[string]string{"context/README.md": "workspace\n", "docs/a.md": "first\n", "docs/b.md": "second\n"})
	oldTree := readManyGit(t, dir, "", "rev-parse", sha+"^{tree}")
	ctxTree := readManyGit(t, dir, "", "rev-parse", sha+":context")
	rows := readManyGit(t, dir, "", "ls-tree", ctxTree)
	blob := readManyGit(t, dir, "../docs", "hash-object", "-w", "--stdin")
	newCtx := readManyGit(t, dir, rows+"\n120000 blob "+blob+"\tview\n", "mktree")
	top := strings.Replace(readManyGit(t, dir, "", "ls-tree", oldTree), ctxTree, newCtx, 1)
	tree := readManyGit(t, dir, top+"\n", "mktree")
	next := readManyGit(t, dir, "", "commit-tree", tree, "-p", sha, "-m", "linked")
	groupDir, _ := gitstore.GroupRepositoryDir(s.Root, "org/repo", "knowledge")
	os.MkdirAll(filepath.Dir(groupDir), 0700)
	command := exec.Command("git", "clone", "--bare", "--no-hardlinks", dir, groupDir)
	if b, e := command.CombinedOutput(); e != nil {
		t.Fatalf("%s %v", b, e)
	}
	a, err := (indexer.Builder{Root: s.Root}).BuildPaths(context.Background(), zoektclient.Target{Repo: "org/repo", Commit: next, Group: "knowledge", RepositoryID: 9}, []string{"context"}, indexer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	db.repo.IndexMode = control.IndexModeMonorepo
	db.repo.PathGroups = []control.PathGroup{{Name: "knowledge", Paths: []string{"context"}}}
	db.repo.Scopes = map[string]control.ScopeState{"knowledge": {Heads: map[string]string{"main": next}, Versions: []control.Version{{Commit: next, Generation: 9, LinkFormat: 1, Files: a.Documents, Shards: len(a.Shards)}}}}
	f, err := s.Read(context.Background(), "org/repo", contract.ReadRequest{Revision: next, Path: "context/view/a.md", StartLine: 1, EndLine: 1})
	if err != nil || f.Path != "docs/a.md" || f.Content != "first\n" {
		t.Fatalf("%+v %v", f, err)
	}
	page, err := s.List(context.Background(), "org/repo", contract.ListRequest{Revision: next, Path: "context/view", First: 1})
	if err != nil || page.Via != "context/view" || page.Entries[0].Path != "docs/a.md" || !page.HasMoreResults {
		t.Fatalf("%+v %v", page, err)
	}
	second, err := s.List(context.Background(), "org/repo", contract.ListRequest{Revision: next, Path: "context/view", First: 1, After: page.NextCursor})
	if err != nil || second.Entries[0].Path != "docs/b.md" {
		t.Fatalf("%+v %v", second, err)
	}
	batch, err := s.ReadMany(context.Background(), "org/repo", next, []contract.ReadItemRequest{{Path: "context/view/a.md", StartLine: 1, EndLine: 1}, {Path: "docs/b.md", StartLine: 1, EndLine: 1}})
	if err != nil || batch.Items[0].Error != nil || batch.Items[1].Error != nil {
		t.Fatalf("%+v %v", batch, err)
	}
	// Diff still uses only explicitly registered paths.
	if _, err = selectScopes(db.repo, next, []string{"docs"}); err == nil {
		t.Fatal("persisted policy unexpectedly widened")
	}
}
