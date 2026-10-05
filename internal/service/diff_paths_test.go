package service

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"os"
	"path/filepath"
	"testing"
)

func TestDiffPathsDistinguishAbsentUnchangedAndAdded(t *testing.T) {
	s, db, bare, base := readManyFixture(t, map[string]string{"same": "same\n", "deleted": "old\n"})
	tree := readManyTree(t, bare, map[string]string{"same": "same\n", "added": "new\n"})
	head := readManyGit(t, bare, "", "commit-tree", tree, "-p", base, "-m", "next")
	db.repo.Versions = append(db.repo.Versions, control.Version{Commit: head})
	for _, tc := range []struct {
		path string
		a, b bool
		n    int
	}{{"same", true, true, 0}, {"missing", false, false, 0}, {"added", false, true, 1}, {"deleted", true, false, 1}} {
		out, err := s.Diff(context.Background(), "org/repo", contract.DiffRequest{Base: base, Head: head, First: 10, Paths: []string{tc.path}})
		if err != nil || len(out.PathStatus) != 1 || len(out.Changes) != tc.n {
			t.Fatalf("%s %+v %v", tc.path, out, err)
		}
		status := out.PathStatus[0]
		if status.BaseExists != tc.a || status.HeadExists != tc.b {
			t.Fatal(status)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Diff(ctx, "org/repo", contract.DiffRequest{Base: base, Head: head, First: 10, Paths: []string{"missing"}}); err == nil {
		t.Fatal("cancelled operation reported missing path")
	}
}

func TestDiffPathPresenceStaysInMonorepoCoverage(t *testing.T) {
	s, db, bare, sha := readManyFixture(t, map[string]string{"a/doc": "a", "b/doc": "b", "outside": "private"})
	db.repo.IndexMode = control.IndexModeMonorepo
	db.repo.PathGroups = []control.PathGroup{{Name: "a", Paths: []string{"a"}}, {Name: "b", Paths: []string{"b"}}}
	for _, name := range []string{"a", "b"} {
		path, err := gitstore.GroupRepositoryDir(s.Root, "org/repo", name)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		readManyGit(t, path, "", "clone", "--bare", bare, path)
		scope := db.repo.Scope(name)
		scope.Versions = db.repo.Versions
		scope.Heads = db.repo.Heads
		db.repo.SetScope(name, scope)
	}
	out, err := s.Diff(context.Background(), "org/repo", contract.DiffRequest{Base: sha, Head: sha, First: 10, Paths: []string{"a/doc", "a/missing"}})
	if err != nil || len(out.PathStatus) != 2 || !out.PathStatus[0].BaseExists || out.PathStatus[1].BaseExists {
		t.Fatal(out, err)
	}
	if _, err = s.Diff(context.Background(), "org/repo", contract.DiffRequest{Base: sha, Head: sha, First: 10, Paths: []string{"outside"}}); err == nil {
		t.Fatal("scope escaped")
	}
}
