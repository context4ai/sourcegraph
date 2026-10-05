package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
)

func linkFixture(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	work := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = work
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=f@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=f@example.test")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	run("init", "-q")
	for name, body := range map[string]string{"docs/a.md": "审核 needle\nsecond\n", "docs/介绍 文档.md": "中文内容\n", "docs/b.md": "same\n", "docs/c.md": "same\n", "a/b/file": "deep\n", "a/x": "correct\n", "x": "wrong\n", "..notes": "valid\n", "context/knowledge.md": "knowledge\n", "docs/data.bin": "\x00binary"} {
		p := filepath.Join(work, name)
		os.MkdirAll(filepath.Dir(p), 0700)
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	links := map[string]string{"context/view": "../docs", "context/second": "../docs", "alias": "docs/a.md", "dir-link": "a/b", "complex": "dir-link/../x", "valid": "..notes", "bad": "missing", "outside": "../../etc/passwd", "absolute": "/etc/passwd", "gitmeta": ".git/config", "loop-a": "loop-b", "loop-b": "loop-a", "docs/ancestor": "..", "docs/broken": "missing", "docs/module-link": "../module/file", "中文链接": "docs/介绍 文档.md", "submodule-link": "module/file"}
	for i := 0; i < 9; i++ {
		target := "docs/a.md"
		if i < 8 {
			target = fmt.Sprintf("chain%d", i+1)
		}
		links[fmt.Sprintf("chain%d", i)] = target
	}
	for name, target := range links {
		if e := os.Symlink(target, filepath.Join(work, name)); e != nil {
			t.Fatal(e)
		}
	}
	run("add", ".")
	run("commit", "-qm", "fixture")
	sha := run("rev-parse", "HEAD")
	run("update-index", "--add", "--cacheinfo", "160000,"+sha+",module")
	run("commit", "-qm", "gitlink")
	sha = run("rev-parse", "HEAD")
	bare, _ := RepositoryDir(root, "org/repo")
	os.MkdirAll(filepath.Dir(bare), 0700)
	run("clone", "--bare", "-q", work, bare)
	s, e := New(root)
	if e != nil {
		t.Fatal(e)
	}
	return s, sha
}
func problemCode(err error) string {
	var p *contract.Error
	if errors.As(err, &p) {
		return p.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}
func TestResolveLinksAndRead(t *testing.T) {
	s, sha := linkFixture(t)
	ctx := context.Background()
	for _, tc := range []struct{ path, want, code string }{{"alias", "docs/a.md", ""}, {"中文链接", "docs/介绍 文档.md", ""}, {"submodule-link", "", "LINK_TO_SUBMODULE"}, {"context/view/a.md", "docs/a.md", ""}, {"complex", "a/x", ""}, {"valid", "..notes", ""}, {"chain1", "docs/a.md", ""}, {"chain0", "", "LINK_DEPTH_EXCEEDED"}, {"missing", "", "FILE_NOT_FOUND"}, {"bad", "", "LINK_DANGLING"}, {"outside", "", "LINK_OUTSIDE_REPOSITORY"}, {"absolute", "", "LINK_OUTSIDE_REPOSITORY"}, {"gitmeta", "", "LINK_OUTSIDE_REPOSITORY"}, {"loop-a", "", "LINK_LOOP"}} {
		t.Run(tc.path, func(t *testing.T) {
			e, err := s.ResolvePath(ctx, "org/repo", sha, tc.path)
			if problemCode(err) != tc.code || e.Path != tc.want {
				t.Fatalf("got %+v %v; want %s %s", e, err, tc.want, tc.code)
			}
		})
	}
	f, err := s.Read(ctx, "org/repo", sha, "context/view/a.md", 1, 500)
	if err != nil || f.Path != "docs/a.md" || f.Via != "context/view/a.md" || !strings.Contains(f.Content, "needle") {
		t.Fatalf("%+v %v", f, err)
	}
	f, err = s.Read(ctx, "org/repo", sha, "bad", 1, 500)
	if err != nil || f.Kind != "symlink" || f.FollowError != "LINK_DANGLING" || f.Content != "missing" {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err = s.Read(ctx, "org/repo", sha, "bad/a.md", 1, 500); problemCode(err) != "LINK_DANGLING" {
		t.Fatal(err)
	}
	if _, err = s.ReadBlobPath(ctx, "org/repo", sha, "alias", 4096); problemCode(err) != "NOT_A_FILE" {
		t.Fatal("plugin followed link", err)
	}
	scoped, _ := s.WithPaths([]string{"context"})
	if _, err = scoped.Read(ctx, "org/repo", sha, "context/view/a.md", 1, 500); problemCode(err) != "PATH_NOT_INDEXED" {
		t.Fatal("scope widened", err)
	}
	tree, err := s.List(ctx, "org/repo", sha, "context/view", 1, "")
	if err != nil || tree.Via != "context/view" || !tree.HasMoreResults {
		t.Fatalf("%+v %v", tree, err)
	}
	if _, err = s.List(ctx, "org/repo", sha, "docs", 1, tree.NextCursor); problemCode(err) != "INVALID_CURSOR" {
		t.Fatal("alias cursor reused", err)
	}
}
func TestExpansionAdmissionAndCoverage(t *testing.T) {
	s, sha := linkFixture(t)
	ctx := context.Background()
	entries, err := s.SnapshotEntries(ctx, "org/repo", sha, []string{"context"})
	if err != nil {
		t.Fatal(err)
	}
	// A bad descendant is skipped; both directory entrances retain their files.
	out, m, err := s.ExpandLinks(ctx, "org/repo", sha, entries, []string{"context"}, DefaultLinkLimits(), nil, func(string, []byte) bool { return true })
	if err != nil || len(out) <= 1 || m.Skipped["LINK_LOOP"] != 2 || m.Skipped["LINK_DANGLING"] != 2 || m.Skipped["LINK_TO_SUBMODULE"] != 2 || m.Links["context/view/a.md"].Target != "docs/a.md" || m.Links["context/view/ancestor"].FollowError != "LINK_LOOP" {
		t.Fatalf("entries=%+v manifest=%+v err=%v", out, m, err)
	}
	selected := []Entry{}
	for _, name := range []string{"alias", "valid"} {
		e, eerr := s.entry(ctx, mustRepoPath(t, s), sha, name)
		if eerr != nil {
			t.Fatal(eerr)
		}
		selected = append(selected, e)
	}
	out, m, err = s.ExpandLinks(ctx, "org/repo", sha, selected, []string{"alias", "valid"}, DefaultLinkLimits(), nil, func(string, []byte) bool { return true })
	if err != nil || len(out) != 4 || len(m.Skipped) != 0 || m.Links["alias"].Target != "docs/a.md" {
		t.Fatalf("%+v %+v %v", out, m, err)
	}
	limits := DefaultLinkLimits()
	limits.ContentBytes = 1
	out, m, err = s.ExpandLinks(ctx, "org/repo", sha, selected, []string{"alias", "valid"}, limits, nil, func(string, []byte) bool { return true })
	if err != nil || len(out) != 0 || m.Skipped["LINK_TARGET_TOO_LARGE"] != 2 {
		t.Fatalf("%+v %+v %v", out, m, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err = s.ExpandLinks(canceled, "org/repo", sha, selected, nil, limits, nil, func(string, []byte) bool { return true }); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func mustRepoPath(t *testing.T, s *Store) string {
	t.Helper()
	p, e := s.repoPath("org/repo")
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestLinkExpansionHydratesMetadataOnlyClone(t *testing.T) {
	s, sha := linkFixture(t)
	dir := mustRepoPath(t, s)
	ctx := context.Background()
	alias, err := s.treeEntry(ctx, dir, sha, "alias")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.treeEntry(ctx, dir, sha, "docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string][]byte{}
	for _, e := range []Entry{alias, target} {
		name := filepath.Join(dir, "objects", e.BlobOID[:2], e.BlobOID[2:])
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		objects[e.BlobOID] = data
		if err = os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.ResolvePath(ctx, "org/repo", sha, "alias"); problemCode(err) == "LINK_DANGLING" || err == nil {
		t.Fatalf("missing blob misclassified: %v", err)
	}
	// Listing only needs target tree metadata, not the target file object.
	aliasObject := filepath.Join(dir, "objects", alias.BlobOID[:2], alias.BlobOID[2:])
	if err = os.WriteFile(aliasObject, objects[alias.BlobOID], 0600); err != nil {
		t.Fatal(err)
	}
	listing, err := s.List(ctx, "org/repo", sha, "", 1000, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range listing.Entries {
		if e.Path == "alias" {
			found = e.Target == "docs/a.md" && e.TargetKind == "file"
		}
	}
	if !found {
		t.Fatal("missing target blob prevented metadata listing")
	}
	if _, err = s.Read(ctx, "org/repo", sha, "alias", 1, 1); problemCode(err) != "GIT_OBJECT_NOT_HYDRATED" {
		t.Fatalf("missing target body: %v", err)
	}
	hydrationCalls := 0
	hydrate := func(entries []Entry) error {
		hydrationCalls++
		for _, e := range entries {
			if data, ok := objects[e.BlobOID]; ok {
				if err := os.WriteFile(filepath.Join(dir, "objects", e.BlobOID[:2], e.BlobOID[2:]), data, 0600); err != nil {
					return err
				}
			}
		}
		return nil
	}
	out, m, err := s.ExpandLinks(ctx, "org/repo", sha, []Entry{alias}, []string{"alias"}, DefaultLinkLimits(), hydrate, func(string, []byte) bool { return true })
	if err != nil || len(out) != 2 || hydrationCalls < 2 || m.Links["alias"].Target != "docs/a.md" {
		t.Fatalf("%+v %+v calls=%d err=%v", out, m, hydrationCalls, err)
	}
	guard, err := s.ObjectGrowthGuard("org/repo", 1)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "objects", "growth-test"), []byte("abc"), 0600)
	if problemCode(guard()) != "GIT_HYDRATION_BUDGET_EXCEEDED" {
		t.Fatal("growth budget not enforced")
	}
}

func TestLinkExpansionBudgetBoundaries(t *testing.T) {
	s, sha := linkFixture(t)
	ctx := context.Background()
	e, err := s.treeEntry(ctx, mustRepoPath(t, s), sha, "alias")
	if err != nil {
		t.Fatal(err)
	}
	n := int64(len("审核 needle\nsecond\n")) * 2
	for _, tc := range []struct {
		name   string
		limits LinkLimits
		ok     bool
	}{
		{"exact", LinkLimits{Links: 1, Files: 1, Scanned: 2, ContentBytes: n}, true},
		{"links", LinkLimits{Links: 0, Files: 1, Scanned: 2, ContentBytes: n}, false},
		{"files", LinkLimits{Links: 1, Files: 0, Scanned: 2, ContentBytes: n}, false},
		{"scan", LinkLimits{Links: 1, Files: 1, Scanned: 1, ContentBytes: n}, false},
		{"bytes", LinkLimits{Links: 1, Files: 1, Scanned: 2, ContentBytes: n - 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, m, err := s.ExpandLinks(ctx, "org/repo", sha, []Entry{e}, []string{"alias"}, tc.limits, nil, func(string, []byte) bool { return true })
			if err != nil {
				t.Fatal(err)
			}
			if tc.ok {
				if len(out) != 2 || len(m.Skipped) != 0 {
					t.Fatalf("%+v %+v", out, m)
				}
			} else if len(out) != 0 || m.Skipped["LINK_TARGET_TOO_LARGE"] != 1 {
				t.Fatalf("%+v %+v", out, m)
			}
		})
	}
}
