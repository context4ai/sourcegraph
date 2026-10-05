package indexer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
)

var minified = "var a=1;" + strings.Repeat("function f(x){return x+1};", 20) + "marker\n"

func TestGenerated(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{"dist/app.min.js", minified, true},
		{"dist/app.js", minified, true},
		{"dist/app.mjs", strings.TrimSuffix(minified, "\n"), true},
		{"dist/app.CSS", strings.Repeat(".a{color:red}", 20), true},
		{"dist/app.js.map", "{}", true},
		{"dist/app.css.map", "{}", true},
		{"maps/routes.map", " {\"version\":3,\"sources\":[]}", true},
		{"src/app.js", "const a = 1;\nexport function f(x) {\n  return x + 1;\n}\n", false},
		{"src/empty.js", "", false},
		{"maps/world.map", "lat,lng\n1,2\n", false},
		{"bun.lock", strings.Repeat("\"lodash\": [\"lodash@4.17.21\", \"\", {}, \"sha512-v2kDEe57lecTulaDIuNTPy3Ry4gLGJ6Z1O3vE1krgXZNrsQ+LFTGHVxVjcXPs17LhbZVGedAJv8XZ1tvj5FvSg==\"],", 3), false},
		{"README.md", strings.Repeat("长段落", 200), false},
		{"api.pb.go", strings.Repeat("x", 500), false},
	} {
		if got := Generated(tc.name, []byte(tc.content)); got != tc.want {
			t.Errorf("Generated(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func fixtureGit(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"--git-dir=" + dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
	c.Stdin = strings.NewReader(input)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func fixtureTree(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	nested := map[string]map[string]string{}
	rows := []string{}
	for name, content := range files {
		if parent, child, ok := strings.Cut(name, "/"); ok {
			if nested[parent] == nil {
				nested[parent] = map[string]string{}
			}
			nested[parent][child] = content
			continue
		}
		rows = append(rows, "100644 blob "+fixtureGit(t, dir, content, "hash-object", "-w", "--stdin")+"\t"+name+"\n")
	}
	for name, children := range nested {
		rows = append(rows, "040000 tree "+fixtureTree(t, dir, children)+"\t"+name+"\n")
	}
	sort.Strings(rows)
	return fixtureGit(t, dir, strings.Join(rows, ""), "mktree")
}

func indexedNames(t *testing.T, shard string) []string {
	t.Helper()
	f, err := os.Open(shard)
	if err != nil {
		t.Fatal(err)
	}
	file, err := index.NewIndexFile(f)
	if err != nil {
		t.Fatal(err)
	}
	s, err := index.NewSearcher(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Search(context.Background(), &query.Substring{Pattern: "marker", Content: true}, &zoekt.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, m := range res.Files {
		names = append(names, m.FileName)
	}
	slices.Sort(names)
	return names
}

func TestBuildSkipsGeneratedFilesWhenEnabled(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := gitstore.RepositoryDir(root, "org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "", "init", "--bare")
	tree := fixtureTree(t, dir, map[string]string{
		"src/app.js":      "export const marker = 1;\n",
		"dist/app.min.js": minified,
		"dist/app.js.map": `{"version":3,"names":["marker"]}`,
		"bun.lock":        "{\n  \"marker\": \"1.0.0\"\n}\n",
		"README.md":       "marker\n",
	})
	commit := fixtureGit(t, dir, "", "commit-tree", tree, "-m", "fixture")
	b := Builder{Root: root}
	for id, tc := range map[uint32]struct {
		opts Options
		want []string
	}{
		7: {Options{SkipGenerated: true}, []string{"README.md", "bun.lock", "src/app.js"}},
		8: {Options{}, []string{"README.md", "bun.lock", "dist/app.js.map", "dist/app.min.js", "src/app.js"}},
	} {
		a, err := b.Build(context.Background(), zoektclient.Target{Repo: "org/repo", Commit: commit, RepositoryID: id}, tc.opts)
		if err != nil {
			t.Fatalf("Build %+v: %v", tc.opts, err)
		}
		if len(a.Shards) != 1 {
			t.Fatalf("shards = %v", a.Shards)
		}
		if got := indexedNames(t, filepath.Join(root, "index", a.Shards[0])); !slices.Equal(got, tc.want) {
			t.Errorf("%+v indexed %v, want %v", tc.opts, got, tc.want)
		}
	}
}

func TestBuildLinkSnapshotAndGeneratedAliases(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := gitstore.RepositoryDir(root, "org/repo")
	os.MkdirAll(dir, 0700)
	fixtureGit(t, dir, "", "init", "--bare")
	docs := fixtureTree(t, dir, map[string]string{"a.md": "marker\n", "b.md": "marker\n"})
	bundle := fixtureGit(t, dir, minified, "hash-object", "-w", "--stdin")
	target := fixtureGit(t, dir, "../docs", "hash-object", "-w", "--stdin")
	disguised := fixtureGit(t, dir, "../bundle.js", "hash-object", "-w", "--stdin")
	contextTree := fixtureGit(t, dir, "120000 blob "+target+"\tview\n120000 blob "+disguised+"\tbundle.md\n", "mktree")
	tree := fixtureGit(t, dir, "040000 tree "+docs+"\tdocs\n040000 tree "+contextTree+"\tcontext\n100644 blob "+bundle+"\tbundle.js\n", "mktree")
	sha := fixtureGit(t, dir, "", "commit-tree", tree, "-m", "links")
	for _, follow := range []bool{true, false} {
		id := uint32(81)
		if !follow {
			id++
		}
		b := Builder{Root: root}
		artifact, err := b.Build(context.Background(), zoektclient.Target{Repo: "org/repo", Commit: sha, RepositoryID: id}, Options{SkipGenerated: true, NoFollowLinks: !follow})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"docs/a.md", "docs/b.md"}
		if follow {
			want = []string{"context/view/a.md", "context/view/b.md", "docs/a.md", "docs/b.md"}
		}
		got := indexedNames(t, filepath.Join(root, "index", artifact.Shards[0]))
		if !slices.Equal(got, want) {
			t.Fatalf("follow=%v got=%v want=%v", follow, got, want)
		}
		if artifact.Links == nil || artifact.Links.Followed != follow {
			t.Fatal("missing immutable setting")
		}
		if follow && artifact.Links.Links["context/view/a.md"].Target != "docs/a.md" {
			t.Fatal("alias mapping missing")
		}
	}
	// A scoped build must publish both real and alias paths, without other files.
	groupDir, _ := gitstore.GroupRepositoryDir(root, "org/repo", "knowledge")
	os.MkdirAll(filepath.Dir(groupDir), 0700)
	cmd := exec.Command("git", "clone", "--bare", "--no-hardlinks", dir, groupDir)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("%s %v", out, e)
	}
	a, err := (Builder{Root: root}).BuildPaths(context.Background(), zoektclient.Target{Repo: "org/repo", Commit: sha, RepositoryID: 83, Group: "knowledge"}, []string{"context"}, Options{SkipGenerated: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(a.Links.Paths, "docs") {
		t.Fatalf("effective coverage missing: %+v", a.Links)
	}
	got := indexedNames(t, filepath.Join(root, "index", a.Shards[0]))
	if !slices.Equal(got, []string{"context/view/a.md", "context/view/b.md", "docs/a.md", "docs/b.md"}) {
		t.Fatal(got)
	}
	compareLinkGlobsWithRG(t, dir, sha, filepath.Join(root, "index", a.Shards[0]))

}

func compareLinkGlobsWithRG(t *testing.T, dir, sha, shard string) {
	t.Helper()
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Fatal("rg is required for symlink equivalence tests")
	}
	work := t.TempDir()
	checkout := exec.Command("git", "--git-dir="+dir, "--work-tree="+work, "checkout", sha, "--", ".")
	if out, err := checkout.CombinedOutput(); err != nil {
		t.Fatalf("checkout: %s %v", out, err)
	}
	f, err := os.Open(shard)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.NewIndexFile(f)
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	engine, err := index.NewSearcher(idx)
	if err != nil {
		idx.Close()
		t.Fatal(err)
	}
	defer engine.Close()
	for _, globs := range [][]string{{"context/view/**"}, {"context/view/**", "!**/b.md"}, {"docs/**"}} {
		pattern, err := zoektclient.PatternQuery(zoektclient.Pattern{Text: "marker", Globs: globs})
		if err != nil {
			t.Fatal(err)
		}
		q, err := query.Parse(pattern)
		if err != nil {
			t.Fatal(err)
		}
		result, err := engine.Search(context.Background(), q, &zoekt.SearchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, file := range result.Files {
			got = append(got, file.FileName)
		}
		sort.Strings(got)
		args := []string{"-l", "-L", "--hidden", "--no-ignore"}
		for _, g := range globs {
			args = append(args, "-g", g)
		}
		args = append(args, "--", "marker", ".")
		cmd := exec.Command(rg, args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("rg: %s %v", out, err)
		}
		want := []string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			want = append(want, strings.TrimPrefix(line, "./"))
		}
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Fatalf("globs=%v engine=%v rg=%v", globs, got, want)
		}
	}
}
