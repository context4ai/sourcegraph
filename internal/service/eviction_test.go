package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/indexer"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
)

func evictionRepo(now time.Time) control.Repository {
	v := func(c string, age time.Duration) control.Version {
		return control.Version{Commit: strings.Repeat(c, 40), ObservedAt: now.Add(-age)}
	}
	return control.Repository{Name: "org/repo", Enabled: true, Revision: 1, Policy: control.Policy{Branches: []string{"main"}, Count: 10},
		Heads:         map[string]string{"main": strings.Repeat("h", 40)},
		PinnedCommits: []string{strings.Repeat("p", 40)},
		Jobs:          []control.Job{{State: "running", Commit: strings.Repeat("j", 40)}},
		Versions:      []control.Version{v("p", 100*time.Hour), v("j", 90*time.Hour), v("a", 80*time.Hour), v("b", 70*time.Hour), v("c", 60*time.Hour), v("h", time.Hour)}}
}

func commits(versions []control.Version) string {
	out := []string{}
	for _, v := range versions {
		out = append(out, v.Commit[:1])
	}
	return strings.Join(out, "")
}

func TestTrimSnapshotsDropsOldestUnprotected(t *testing.T) {
	r := evictionRepo(time.Now())
	out, ok := trimSnapshots(r.Versions, protectedCommits(r), 4)
	if !ok || commits(out) != "pjch" {
		t.Fatalf("trim: %s %v", commits(out), ok)
	}
	if _, ok = trimSnapshots(r.Versions, protectedCommits(r), 2); ok {
		t.Fatal("protected versions alone exceed the limit")
	}
}

func TestEvictSnapshotsIsFIFOAndKeepsProtected(t *testing.T) {
	db := &managementStore{repo: evictionRepo(time.Now())}
	s := &Service{DB: db, Root: t.TempDir()}
	n, err := s.evictSnapshots(context.Background(), "org/repo", 2)
	if err != nil || n != 2 || commits(db.repo.Versions) != "pjch" {
		t.Fatalf("evict: %d %v %s", n, err, commits(db.repo.Versions))
	}
	n, err = s.evictSnapshots(context.Background(), "org/repo", 64)
	if err != nil || n != 1 || commits(db.repo.Versions) != "pjh" {
		t.Fatalf("second batch: %d %v %s", n, err, commits(db.repo.Versions))
	}
	if n, _ = s.evictSnapshots(context.Background(), "org/repo", 64); n != 0 {
		t.Fatal("protected snapshots evicted")
	}
}

func TestEnsureBudgetEvictsThenNamesTheRealCause(t *testing.T) {
	root := t.TempDir()
	git, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	r := evictionRepo(time.Now())
	r.MaxStorageBytes = 1024
	db := &managementStore{repo: r}
	s := &Service{DB: db, Root: root, Git: git}
	dir, err := gitstore.RepositoryDir(root, "org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "objects.pack"), make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "snapshot-retention")
	if err = os.WriteFile(marker, []byte(strings.Repeat("h", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Eviction commits first; collection then fails on this fake repository and
	// is reported as a reclaim failure, not as a full budget.
	err = s.ensureRepositoryBudget(context.Background(), "org/repo", 0)
	if code(err) != "RECLAIM_FAILED" || strings.Contains(commits(db.repo.Versions), "a") {
		t.Fatalf("first round: %v %s", err, commits(db.repo.Versions))
	}
	// The revocation invalidated the marker, so the unfinished collection stays visible.
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) || !s.gitReclaimPending(db.repo) {
		t.Fatalf("marker not invalidated: %v", statErr)
	}
	db.repo.Versions = []control.Version{{Commit: strings.Repeat("h", 40)}, {Commit: strings.Repeat("p", 40)}}
	if err = s.ensureRepositoryBudget(context.Background(), "org/repo", 0); code(err) != "REPOSITORY_STORAGE_LIMIT" {
		t.Fatalf("protected only: %v", err)
	}
	// Under budget, an estimate that does not fit is not a failure.
	db.repo.MaxStorageBytes = 1 << 20
	if err = s.ensureRepositoryBudget(context.Background(), "org/repo", 2<<20); err != nil {
		t.Fatalf("estimate only: %v", err)
	}
}

func TestApplyRetentionAfterPrepareKeepsNewestOrdinary(t *testing.T) {
	root := t.TempDir()
	git, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	r := evictionRepo(time.Now())
	r.Policy.Count = 1
	r.Jobs = nil
	db := &managementStore{repo: r}
	s := &Service{DB: db, Root: root, Git: git}
	s.applyRetention(context.Background(), "org/repo")
	// Head and pin are protected; count 1 keeps only the newest ordinary snapshot.
	if got := commits(db.repo.Versions); got != "hcp" {
		t.Fatalf("retention: %s", got)
	}
}

func TestSyncReleasesSnapshotsBeforeFetching(t *testing.T) {
	root := t.TempDir()
	git, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	r := evictionRepo(time.Now())
	r.MaxStorageBytes = 1024
	r.Jobs = append(r.Jobs, control.Job{ID: "sync-1", Kind: "sync", State: "running", Owner: "w", LeaseUntil: time.Now().Add(time.Hour)})
	db := &managementStore{repo: r}
	s := &Service{DB: db, Root: root, Git: git, Helper: "/bin/false"}
	dir, _ := gitstore.RepositoryDir(root, "org/repo")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Usage is far above the 120% in-flight ceiling, which used to fail the fetch
	// before anything could be released.
	if err = os.WriteFile(filepath.Join(dir, "objects.pack"), make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	err = s.sync(context.Background(), db.repo, db.repo.Jobs[len(db.repo.Jobs)-1])
	if code(err) == "REPOSITORY_STORAGE_LIMIT" || strings.ContainsAny(commits(db.repo.Versions), "abc") {
		t.Fatalf("sync did not release snapshots before fetching: %v %s", err, commits(db.repo.Versions))
	}
}

// gitRemote is a file:// remote served as https://fixture.example/org/repo.git.
type gitRemote struct {
	t    *testing.T
	work string
}

func newGitRemote(t *testing.T) *gitRemote {
	t.Helper()
	g := &gitRemote{t: t, work: t.TempDir()}
	g.run("init", "-q", "-b", "main")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nexec '" + realGit + "' -c protocol.file.allow=always -c 'url.file://" + g.work + ".insteadOf=https://fixture.example/org/repo.git' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return g
}

func (g *gitRemote) run(args ...string) string {
	g.t.Helper()
	c := exec.Command("git", append([]string{"-C", g.work}, args...)...)
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com")
	out, err := c.CombinedOutput()
	if err != nil {
		g.t.Fatalf("%v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *gitRemote) commit(message string) string {
	g.run("commit", "-q", "--allow-empty", "-m", message)
	return g.run("rev-parse", "HEAD")
}

func TestSyncContinuesAfterSuccessfulEviction(t *testing.T) {
	remote := newGitRemote(t)
	c1 := remote.commit("one")
	root := t.TempDir()
	git, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	job := control.Job{ID: "sync-1", Kind: "sync", State: "running", Owner: "w", LeaseUntil: time.Now().Add(time.Hour)}
	db := &managementStore{repo: control.Repository{Name: "org/repo", Enabled: true, CodeHost: "https://fixture.example", Policy: control.Policy{Branches: []string{"main"}, Count: 10}, Jobs: []control.Job{job}}}
	s := &Service{DB: db, Root: root, Git: git, Helper: "/bin/false", Token: "fixture-token"}
	ctx := context.Background()
	sync := func() {
		t.Helper()
		if err := s.sync(ctx, db.repo, job); err != nil {
			t.Fatal(err)
		}
		if err := s.collectRepositorySnapshots(ctx, "org/repo"); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	c2 := remote.commit("two")
	sync()
	// c1 is now an ordinary snapshot with a published index of 1 MiB.
	for i, v := range db.repo.Versions {
		if v.Commit == c1 {
			db.repo.Versions[i].Generation = 101
			db.repo.Versions[i].ObservedAt = time.Now().Add(-time.Hour)
		}
	}
	shard := "snapshot-101_a.zoekt"
	manifest, _ := json.Marshal(indexer.Artifact{Target: zoektclient.Target{Repo: "org/repo", Commit: c1, RepositoryID: 101}, Shards: []string{shard}})
	for path, data := range map[string][]byte{filepath.Join(root, "index", shard): make([]byte, 1<<20), filepath.Join(root, "manifests", "101.json"): manifest} {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		if err = os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	used, err := s.repositoryUsage(ctx, db.repo)
	if err != nil {
		t.Fatal(err)
	}
	db.repo.MaxStorageBytes = used - 512<<10
	c3 := remote.commit("three")
	sync()
	_ = c2
	for _, v := range db.repo.Versions {
		if v.Commit == c1 || v.Generation == 101 {
			t.Fatalf("evicted version written back: %+v", db.repo.Versions)
		}
	}
	if _, err = os.Stat(filepath.Join(root, "index", shard)); !os.IsNotExist(err) {
		t.Fatalf("evicted index kept: %v", err)
	}
	if _, err = git.Snapshot(ctx, "org/repo", c3); err != nil || len(db.repo.Versions) != 2 {
		t.Fatalf("sync after eviction: %v %+v", err, db.repo.Versions)
	}
}

func TestEvictionAbortsWhenMarkerCannotBeInvalidated(t *testing.T) {
	root := t.TempDir()
	git, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	db := &managementStore{repo: evictionRepo(time.Now())}
	s := &Service{DB: db, Root: root, Git: git}
	dir, _ := gitstore.RepositoryDir(root, "org/repo")
	// A non-empty directory where the marker file belongs cannot be removed.
	if err = os.MkdirAll(filepath.Join(dir, "snapshot-retention", "x"), 0700); err != nil {
		t.Fatal(err)
	}
	before := commits(db.repo.Versions)
	if _, err = s.evictSnapshots(context.Background(), "org/repo", 1); code(err) != "RECLAIM_FAILED" || commits(db.repo.Versions) != before {
		t.Fatalf("revocation committed without invalidation: %v %s", err, commits(db.repo.Versions))
	}
}

func TestEvictionInvalidatesOnlyChangedGroups(t *testing.T) {
	root := t.TempDir()
	git, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := control.Version{Commit: strings.Repeat("a", 40), ObservedAt: now.Add(-time.Hour)}
	head := control.Version{Commit: strings.Repeat("h", 40), ObservedAt: now}
	r := control.Repository{Name: "org/repo", Enabled: true, Revision: 1, IndexMode: control.IndexModeMonorepo,
		PathGroups: []control.PathGroup{{Name: "a", Paths: []string{"a"}}, {Name: "b", Paths: []string{"b"}}},
		Scopes: map[string]control.ScopeState{
			"a": {Heads: map[string]string{"main": head.Commit}, Versions: []control.Version{old, head}},
			"b": {Heads: map[string]string{"main": head.Commit}, Versions: []control.Version{head}},
		}}
	db := &managementStore{repo: r}
	s := &Service{DB: db, Root: root, Git: git}
	markers := map[string]string{}
	for _, group := range []string{"a", "b"} {
		dir, err := gitstore.GroupRepositoryDir(root, "org/repo", group)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		markers[group] = filepath.Join(dir, "snapshot-retention")
		if err = os.WriteFile(markers[group], []byte("x\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.evictSnapshots(context.Background(), "org/repo", 1); err != nil || n != 1 {
		t.Fatalf("evict: %d %v", n, err)
	}
	if _, err = os.Stat(markers["a"]); !os.IsNotExist(err) {
		t.Fatal("changed group marker kept")
	}
	if _, err = os.Stat(markers["b"]); err != nil {
		t.Fatal("unchanged group marker removed")
	}
}

func TestEvictionAbortsWhenRepositoryIsInaccessible(t *testing.T) {
	root := t.TempDir()
	git, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	db := &managementStore{repo: evictionRepo(time.Now())}
	s := &Service{DB: db, Root: root, Git: git}
	dir, _ := gitstore.RepositoryDir(root, "org/repo")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "snapshot-retention")
	if err = os.WriteFile(marker, []byte(strings.Repeat("h", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(dir)
	if err = os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Lstat(dir); statErr == nil {
		os.Chmod(parent, 0700)
		t.Skip("running with privileges that ignore directory permissions")
	}
	before := commits(db.repo.Versions)
	_, err = s.evictSnapshots(context.Background(), "org/repo", 1)
	if chmodErr := os.Chmod(parent, 0700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if code(err) != "RECLAIM_FAILED" || commits(db.repo.Versions) != before {
		t.Fatalf("revocation committed while the repository was inaccessible: %v %s", err, commits(db.repo.Versions))
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("marker should be untouched")
	}
	// The background check logs that it could not inspect the repository
	// instead of silently reporting nothing to collect.
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	if err = os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	pending := s.gitReclaimPending(db.repo)
	if err = os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if pending || !strings.Contains(logs.String(), "git_reclaim_check") || !strings.Contains(logs.String(), "permission_denied") || strings.Contains(logs.String(), root) {
		t.Fatalf("check failure not reported safely: %v %q", pending, logs.String())
	}
}
