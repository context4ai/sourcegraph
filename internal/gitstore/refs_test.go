package gitstore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// refFixture builds a remote with branches, an annotated tag, a nested tag, a
// lightweight tag and a tag pointing at a tree, served through file:// as
// https://fixture.example/org/repo.git.
func refFixture(t *testing.T) (*Store, FetchOptions, map[string]string) {
	t.Helper()
	work := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", work}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com", "GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.com", "GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700000000 +0000")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "uploadpack.allowFilter", "true")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "first commit\n\nbody")
	ids := map[string]string{"main": run("rev-parse", "HEAD")}
	run("checkout", "-qb", "feat/x")
	run("commit", "-q", "--allow-empty", "-m", "feature work")
	ids["feat/x"] = run("rev-parse", "HEAD")
	run("tag", "-a", "v1", "-m", "release one", ids["main"])
	run("tag", "-a", "v1-nested", "-m", "nested", "v1")
	run("tag", "light", ids["feat/x"])
	run("tag", "tree-tag", run("rev-parse", "HEAD^{tree}"))
	ids["v1"] = run("rev-parse", "v1")
	ids["tree"] = run("rev-parse", "HEAD^{tree}")
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nexec '" + realGit + "' -c protocol.file.allow=always -c 'url.file://" + work + ".insteadOf=https://fixture.example/org/repo.git' \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.git = wrapper
	return s, FetchOptions{Host: "https://fixture.example", Anonymous: true, CredentialHelper: "/bin/false"}, ids
}

func TestRemoteRefsListFetchAndRead(t *testing.T) {
	s, o, ids := refFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := s.InitScratch(ctx, dir); err != nil {
		t.Fatal(err)
	}
	refs, more, err := s.ListRemoteRefs(ctx, dir, "org/repo", o)
	if err != nil || more || len(refs) != 6 {
		t.Fatalf("list: %v %v %+v", more, err, refs)
	}
	byName := map[string]RemoteRef{}
	for _, ref := range refs {
		byName[ref.Name] = ref
	}
	if byName["refs/tags/v1"].Peeled != ids["main"] || byName["refs/heads/feat/x"].Object != ids["feat/x"] {
		t.Fatalf("advertisement: %+v", byName)
	}
	exact, _, err := s.ListRemoteRefs(ctx, dir, "org/repo", o, "refs/heads/x", "refs/tags/x", "refs/heads/feat/x")
	if err != nil || len(exact) != 1 || exact[0].Name != "refs/heads/feat/x" {
		t.Fatalf("exact: %v %+v", err, exact)
	}
	calls := 0
	guard := func() error { calls++; return nil }
	// A tag of a tree cannot be delivered by a tree-less transfer; Git names it.
	var missing *MissingTipError
	if err = s.FetchRefs(ctx, dir, "org/repo", o, []string{"+refs/tags/*:refs/tags/*"}, guard); !errors.As(err, &missing) || missing.SHA != ids["tree"] {
		t.Fatalf("tree tag: %v", err)
	}
	if err = s.FetchRefs(ctx, dir, "org/repo", o, []string{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*", "^refs/tags/tree-tag"}, guard); err != nil || calls == 0 {
		t.Fatalf("fetch: %v (guard calls %d)", err, calls)
	}
	shas := []string{}
	for _, ref := range refs {
		shas = append(shas, ref.Object)
	}
	objects, err := s.ReadRefObjects(ctx, dir, append(shas, ids["tree"]))
	if err != nil {
		t.Fatal(err)
	}
	commit := objects[ids["main"]]
	if commit.Type != "commit" || commit.Author != "Alice <alice@example.com>" || commit.Title != "first commit" || commit.Time.Unix() != 1700000000 {
		t.Fatalf("commit: %+v", commit)
	}
	tag := objects[ids["v1"]]
	if tag.Type != "tag" || tag.Target != ids["main"] || tag.TargetType != "commit" || tag.Title != "release one" {
		t.Fatalf("tag: %+v", tag)
	}
	// Trees are filtered out and are reported missing, never guessed.
	if objects[ids["tree"]].Type != "missing" {
		t.Fatalf("tree object: %+v", objects[ids["tree"]])
	}
}

func TestFetchRefsGuardAbortsDownload(t *testing.T) {
	s, o, _ := refFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := s.InitScratch(ctx, dir); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("over limit")
	if err := s.FetchRefs(ctx, dir, "org/repo", o, []string{"+refs/heads/*:refs/heads/*"}, func() error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("guard ignored: %v", err)
	}
}

func TestRetentionPendingReadsMarkerOnly(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	pending := func(commits ...string) bool {
		t.Helper()
		ok, err := s.RetentionPending("org/repo", commits)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if pending(a) {
		t.Fatal("repository without objects reported pending")
	}
	p, err := s.repositoryDir("org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
	if !pending(a) || pending() {
		t.Fatal("missing marker must be pending while versions exist")
	}
	if err = os.WriteFile(filepath.Join(p, "snapshot-retention"), []byte(a+"\n"+b+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !pending(a) || pending(a, b) {
		t.Fatal("pending detection")
	}
	// Marker {A}; B was fetched, then revoked before collection: the set is {A}
	// again, so only the invalidated marker reveals B's objects.
	if err = os.WriteFile(filepath.Join(p, "snapshot-retention"), []byte(a+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.InvalidateRetention("org/repo"); err != nil || !pending(a) {
		t.Fatalf("invalidated marker: %v", err)
	}
	// An inaccessible repository is "cannot check", not "nothing to collect".
	parent := filepath.Dir(p)
	if err = os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	if _, statErr := os.Lstat(p); statErr == nil {
		t.Skip("running with privileges that ignore directory permissions")
	}
	if ok, err := s.RetentionPending("org/repo", []string{a}); err == nil || ok {
		t.Fatalf("inaccessible repository: %v %v", ok, err)
	}
}

func TestInvalidateRetentionFailsClosed(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.InvalidateRetention("org/repo"); err != nil {
		t.Fatalf("missing repository must be idempotent: %v", err)
	}
	p, err := s.repositoryDir("org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p, "snapshot-retention"), []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(p)
	if err = os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	if _, statErr := os.Lstat(p); statErr == nil {
		t.Skip("running with privileges that ignore directory permissions")
	}
	if err = s.InvalidateRetention("org/repo"); err == nil {
		t.Fatal("inaccessible repository treated as having no marker")
	}
}
