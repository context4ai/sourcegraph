package indexer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
)

// Build real Git trees using NUL separators: ordinary line-based fixtures
// cannot represent the filenames that used to abort repository indexing.
func TestBuildWithUnsupportedPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := gitstore.RepositoryDir(root, "org/repo")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "", "init", "--bare")
	blob := fixtureGit(t, dir, "marker\n", "hash-object", "-w", "--stdin")
	rows := ""
	for _, name := range []string{"ok.md", "中文.md", "bad\nname.md", "tab\tname.md", "back\\slash.md", "bad\xff.md"} {
		rows += "100644 blob " + blob + "\t" + name + "\x00"
	}
	docs := fixtureGit(t, dir, rows, "mktree", "-z")
	link := fixtureGit(t, dir, "docs", "hash-object", "-w", "--stdin")
	tree := fixtureGit(t, dir, "040000 tree "+docs+"\tdocs\x00"+"120000 blob "+link+"\tview\x00", "mktree", "-z")
	sha := fixtureGit(t, dir, "", "commit-tree", tree, "-m", "path fixture")
	store, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	listed := []string{}
	after := ""
	for {
		page, err := store.List(ctx, "org/repo", sha, "docs", 1, after)
		if err != nil || !page.UnsupportedPathsSkipped {
			t.Fatalf("list=%+v err=%v", page, err)
		}
		for _, e := range page.Entries {
			listed = append(listed, e.Path)
		}
		if !page.HasMoreResults {
			break
		}
		if page.NextCursor == "" || page.NextCursor == after {
			t.Fatal("cursor did not advance")
		}
		after = page.NextCursor
	}
	if !slices.Equal(listed, []string{"docs/ok.md", "docs/中文.md"}) {
		t.Fatal(listed)
	}
	file, err := store.Read(ctx, "org/repo", sha, "view/ok.md", 1, 10)
	if err != nil || !strings.Contains(file.Content, "marker") {
		t.Fatalf("read=%+v err=%v", file, err)
	}
	if _, err := store.Read(ctx, "org/repo", sha, "docs/bad\nname.md", 1, 10); err == nil {
		t.Fatal("request validation weakened")
	}
	for id, follow := range map[uint32]bool{201: true, 202: false} {
		a, err := (Builder{Root: root}).Build(ctx, zoektclient.Target{Repo: "org/repo", Commit: sha, RepositoryID: id}, Options{NoFollowLinks: !follow})
		if err != nil {
			t.Fatal(err)
		}
		if a.Links == nil || !a.Links.UnsupportedPathsSkipped {
			t.Fatalf("missing coverage: %+v", a.Links)
		}
		want := []string{"docs/ok.md", "docs/中文.md"}
		if follow {
			want = append(want, "view/ok.md", "view/中文.md")
		}
		if got := indexedNames(t, filepath.Join(root, "index", a.Shards[0])); !slices.Equal(got, want) {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
	group, _ := gitstore.GroupRepositoryDir(root, "org/repo", "docs")
	if err := os.MkdirAll(filepath.Dir(group), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "clone", "--bare", "--no-hardlinks", dir, group).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	// Only the alias is selected: unsupported target siblings must not abort
	// expansion, and its coverage warning must survive publication.
	a, err := (Builder{Root: root}).BuildPaths(ctx, zoektclient.Target{Repo: "org/repo", Commit: sha, Group: "docs", RepositoryID: 203}, []string{"view"}, Options{})
	if err != nil || a.Links == nil || !a.Links.UnsupportedPathsSkipped {
		t.Fatalf("scoped build=%+v err=%v", a, err)
	}
	if got := indexedNames(t, filepath.Join(root, "index", a.Shards[0])); !slices.Equal(got, []string{"docs/ok.md", "docs/中文.md", "view/ok.md", "view/中文.md"}) {
		t.Fatal(got)
	}
}
