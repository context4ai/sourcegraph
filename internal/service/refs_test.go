package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/identity"
)

// refService serves a fixture remote as https://fixture.example/org/repo.git.
// Remote: main (monitored, older), feat/x (newer), annotated tag v1 on main,
// lightweight tag light on feat/x, branch and tag both named dup, and a tag
// pointing at a tree.
func refService(t *testing.T) (*Service, *managementStore, map[string]string) {
	t.Helper()
	work := t.TempDir()
	at := "1700000000 +0000"
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", work}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com", "GIT_COMMITTER_NAME=Bob", "GIT_COMMITTER_EMAIL=bob@example.com", "GIT_AUTHOR_DATE="+at, "GIT_COMMITTER_DATE="+at)
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
	run("commit", "-qm", "initial import")
	ids := map[string]string{"main": run("rev-parse", "HEAD")}
	at = "1700001000 +0000"
	run("checkout", "-qb", "feat/x")
	run("commit", "-q", "--allow-empty", "-m", "feature work")
	ids["feat/x"] = run("rev-parse", "HEAD")
	run("branch", "dup", ids["main"])
	run("tag", "dup", ids["feat/x"])
	run("tag", "-a", "v1", "-m", "release one", ids["main"])
	run("tag", "light", ids["feat/x"])
	run("tag", "tree-tag", run("rev-parse", "HEAD^{tree}"))
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nexec '" + realGit + "' -c protocol.file.allow=always -c 'url.file://" + work + ".insteadOf=https://fixture.example/org/repo.git' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	git, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	db := &managementStore{repo: control.Repository{Name: "org/repo", Enabled: true, Revision: 1, CodeHost: "https://fixture.example", Policy: control.Policy{Branches: []string{"main"}, Count: 10},
		Heads: map[string]string{"main": ids["main"]}, Versions: []control.Version{{Commit: ids["main"], Indexed: true, Generation: 1}}}}
	s := &Service{DB: db, Root: root, Git: git, Helper: "/bin/false", MinFree: 0}
	t.Cleanup(s.refState.close)
	return s, db, ids
}

func code(err error) string {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestResolveAnyBranchOrTagFromRemote(t *testing.T) {
	s, _, ids := refService(t)
	ctx := context.Background()
	got, err := s.Resolve(ctx, "org/repo", "feat/x")
	if err != nil || got.Commit != ids["feat/x"] || got.Kind != "branch" || got.Source != "remote" || got.Monitored || got.Indexed || got.Title != "feature work" || got.Author != "Alice <alice@example.com>" || got.CreatedAt == nil || got.Hint == "" {
		t.Fatalf("unmonitored branch: %+v %v", got, err)
	}
	if got, err = s.Resolve(ctx, "org/repo", "v1"); err != nil || got.Commit != ids["main"] || got.Kind != "tag" || got.Title != "release one" || !got.Indexed {
		t.Fatalf("annotated tag: %+v %v", got, err)
	}
	if got, err = s.Resolve(ctx, "org/repo", "refs/tags/light"); err != nil || got.Commit != ids["feat/x"] || got.Ref != "refs/tags/light" {
		t.Fatalf("lightweight tag: %+v %v", got, err)
	}
	if _, err = s.Resolve(ctx, "org/repo", "dup"); code(err) != "REF_AMBIGUOUS" || !strings.Contains(err.Error(), "refs/tags/dup") {
		t.Fatalf("ambiguous: %v", err)
	}
	if got, err = s.Resolve(ctx, "org/repo", "refs/heads/dup"); err != nil || got.Commit != ids["main"] {
		t.Fatalf("explicit branch: %+v %v", got, err)
	}
	if _, err = s.Resolve(ctx, "org/repo", "tree-tag"); code(err) != "REF_NOT_COMMIT" {
		t.Fatalf("tree tag: %v", err)
	}
	if _, err = s.Resolve(ctx, "org/repo", "missing-branch"); code(err) != "REF_NOT_FOUND" {
		t.Fatalf("missing: %v", err)
	}
	// Monitored branches keep the synchronized head and never ask the remote.
	if got, err = s.Resolve(ctx, "org/repo", "main"); err != nil || got.Source != "observed" || !got.Monitored || got.Commit != ids["main"] {
		t.Fatalf("monitored: %+v %v", got, err)
	}
	if got, err = s.Resolve(ctx, "org/repo", "refs/heads/main"); err != nil || got.Source != "observed" {
		t.Fatalf("full monitored form keeps the synchronized head: %+v %v", got, err)
	}
	if _, err = s.Resolve(ctx, "org/repo", "abc123"); code(err) != "SHORT_SHA_UNSUPPORTED" {
		t.Fatalf("short sha: %v", err)
	}
	entries, err := os.ReadDir(refsRoot(s.Root))
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch directories left behind: %v %v", entries, err)
	}
}

func TestRefsCatalogListsFiltersAndPages(t *testing.T) {
	s, _, ids := refService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	page, err := s.Refs(ctx, "org/repo", RefsQuery{})
	if err != nil || page.Catalog.State != "ready" || page.Sort != "created" || page.Catalog.Total != 7 {
		t.Fatalf("catalog: %+v %v", page, err)
	}
	if len(page.Refs) != 3 || page.Refs[0].Name != "feat/x" || page.Refs[len(page.Refs)-1].Name != "main" || !page.Refs[len(page.Refs)-1].Monitored {
		t.Fatalf("branches newest first: %+v", page.Refs)
	}
	tags, err := s.Refs(ctx, "org/repo", RefsQuery{Kind: "tag", Sort: "name"})
	if err != nil || len(tags.Refs) != 4 || tags.Refs[0].Name != "dup" || tags.Refs[3].Name != "v1" || !tags.Refs[3].Annotated || tags.Refs[3].Commit != ids["main"] {
		t.Fatalf("tags: %+v %v", tags.Refs, err)
	}
	if tags.Refs[2].Name != "tree-tag" || tags.Refs[2].Commit != "" || tags.Refs[2].TargetType == "" {
		t.Fatalf("tree tag must not claim a commit: %+v", tags.Refs[2])
	}
	filtered, err := s.Refs(ctx, "org/repo", RefsQuery{Kind: "all", Title: "FEATURE", Author: "alice"})
	if err != nil || len(filtered.Refs) != 3 {
		t.Fatalf("title/author filter: %+v %v", filtered.Refs, err)
	}
	first, err := s.Refs(ctx, "org/repo", RefsQuery{Kind: "all", Query: "u", First: 1})
	if err != nil || len(first.Refs) != 1 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("page one: %+v %v", first, err)
	}
	second, err := s.Refs(ctx, "org/repo", RefsQuery{Kind: "all", Query: "u", First: 1, After: first.NextCursor})
	if err != nil || len(second.Refs) != 1 || second.Refs[0].Ref == first.Refs[0].Ref {
		t.Fatalf("page two: %+v %v", second, err)
	}
	if _, err = s.Refs(ctx, "org/repo", RefsQuery{Since: "yesterday"}); code(err) != "INVALID_TIME" {
		t.Fatalf("time validation: %v", err)
	}
	// A complete fresh catalog answers resolve without asking the remote.
	got, err := s.Resolve(ctx, "org/repo", "feat/x")
	if err != nil || got.Source != "catalog" || got.Commit != ids["feat/x"] {
		t.Fatalf("catalog resolve: %+v %v", got, err)
	}
}

func TestRefsNamesOnlyAndTruncatedCatalogs(t *testing.T) {
	s, _, ids := refService(t)
	ctx := context.Background()
	s.refState.mu.Lock()
	s.refState.init()
	names := []RefEntry{{Name: "main", Ref: "refs/heads/main", Kind: "branch", Object: ids["main"]}}
	s.refState.catalogs["org/repo"] = newRefCatalog(names, "names_only", "ref_limit", true, time.Now())
	s.refState.mu.Unlock()
	if _, err := s.Refs(ctx, "org/repo", RefsQuery{Author: "alice"}); code(err) != "REFS_FILTER_UNAVAILABLE" {
		t.Fatalf("names-only filter: %v", err)
	}
	page, err := s.Refs(ctx, "org/repo", RefsQuery{})
	if err != nil || page.Sort != "name" || !page.Catalog.Truncated || page.Hint == "" {
		t.Fatalf("names-only default sort: %+v %v", page, err)
	}
	// A truncated catalog never proves absence: resolve asks the remote.
	got, err := s.Resolve(ctx, "org/repo", "feat/x")
	if err != nil || got.Source != "remote" {
		t.Fatalf("truncated catalog resolve: %+v %v", got, err)
	}
}

func TestRequestWaitLeavesResponseMargin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if requestWait(ctx) != 0 {
		t.Fatal("short deadline must answer retry immediately")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if wait := requestWait(ctx2); wait <= 7*time.Second || wait > 8*time.Second {
		t.Fatalf("wait %v", wait)
	}
}

func TestRefsAndResolveHidePrivateRepositories(t *testing.T) {
	s := visibilityFixture()
	ctx := identity.WithActor(context.Background(), "sso:bob")
	if _, err := s.Refs(ctx, "org/private", RefsQuery{}); code(err) != "REPOSITORY_NOT_FOUND" {
		t.Fatalf("refs: %v", err)
	}
	if _, err := s.Resolve(ctx, "org/private", "feature"); code(err) != "REPOSITORY_NOT_FOUND" {
		t.Fatalf("resolve: %v", err)
	}
}

func TestRefsFirstListingAnswersPreparingThenServes(t *testing.T) {
	s, _, _ := refService(t)
	short, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := s.Refs(short, "org/repo", RefsQuery{})
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != "REFS_CATALOG_PREPARING" || ce.RetryAfterSeconds <= 0 {
		t.Fatalf("preparing: %v", err)
	}
	ctx, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	if page, err := s.Refs(ctx, "org/repo", RefsQuery{}); err != nil || page.Catalog.State != "ready" {
		t.Fatalf("after background refresh: %+v %v", page.Catalog, err)
	}
}

func TestRefsFailedRefreshKeepsLastCatalog(t *testing.T) {
	s, db, ids := refService(t)
	s.refState.mu.Lock()
	s.refState.init()
	old := newRefCatalog([]RefEntry{{Name: "main", Ref: "refs/heads/main", Kind: "branch", Commit: ids["main"]}}, "ready", "", false, time.Now())
	old.refreshedAt = time.Now().Add(-time.Hour)
	s.refState.catalogs["org/repo"] = old
	s.refState.mu.Unlock()
	db.repo.CodeHost = "https://unreachable.invalid"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	page, err := s.Refs(ctx, "org/repo", RefsQuery{})
	if err != nil || page.Catalog.State != "stale" || len(page.Refs) != 1 {
		t.Fatalf("stale: %+v %v", page, err)
	}
	// Wait for the failed refresh; the previous catalog stays and the repository backs off.
	s.refState.mu.Lock()
	done := s.refState.refreshes["org/repo"]
	s.refState.mu.Unlock()
	if done != nil {
		<-done
	}
	s.refState.mu.Lock()
	kept, failure := s.refState.catalogs["org/repo"], s.refState.failures["org/repo"]
	s.refState.mu.Unlock()
	if kept != old || failure.count != 1 || !failure.until.After(time.Now()) {
		t.Fatalf("failure handling: kept=%v failure=%+v", kept == old, failure)
	}
}

func TestDownloadGuardBoundsSizeAndFreeSpace(t *testing.T) {
	s := &Service{Root: t.TempDir()}
	s.refState.init()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pack"), make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.downloadGuard(dir, 1024)(); code(err) != "REF_DOWNLOAD_LIMIT" {
		t.Fatalf("size: %v", err)
	}
	s.MinFree = 1 << 62
	if err := s.downloadGuard(dir, 1<<20)(); code(err) != "INSUFFICIENT_STORAGE" {
		t.Fatalf("free space: %v", err)
	}
}

func TestFetchRefTipsReadsStoredRefsNotTheListing(t *testing.T) {
	s, db, ids := refService(t)
	ctx := context.Background()
	dir, cleanup, err := s.refScratch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	o := s.fetchOptions(db.repo, "")
	o.CheckGrowth = nil
	// The listing saw the branch at main; it moved to feat/x before the fetch.
	listed := []gitstore.RemoteRef{{Name: "refs/heads/feat/x", Object: ids["main"]}}
	stored, excluded, err := s.fetchRefTips(ctx, dir, "org/repo", o, listed, []string{"+refs/heads/feat/x:refs/heads/feat/x"}, func() error { return nil })
	if err != nil || len(excluded) != 0 || len(stored) != 1 || stored[0].Object != ids["feat/x"] {
		t.Fatalf("stored refs: %+v %v %v", stored, excluded, err)
	}
	unknown := refEntries([]gitstore.RemoteRef{{Name: "refs/heads/gone", Object: strings.Repeat("e", 40)}}, map[string]gitstore.RefObject{}, nil)
	if _, err = s.refResult(ctx, db.repo, []string{"refs/heads/gone"}, unknown, "remote"); code(err) != "REMOTE_UNAVAILABLE" {
		t.Fatalf("unverified object must be retryable: %v", err)
	}
}

func TestPendingRemoteLookupsAreBounded(t *testing.T) {
	s, db, _ := refService(t)
	s.refState.mu.Lock()
	s.refState.init()
	for i := 0; i < refLookupPending; i++ {
		s.refState.lookups[strings.Repeat("k", i+1)] = &refLookup{done: make(chan struct{})}
	}
	s.refState.mu.Unlock()
	if _, err := s.lookupRefs(context.Background(), db.repo, []string{"refs/heads/new"}); code(err) != "REMOTE_UNAVAILABLE" {
		t.Fatalf("queue bound: %v", err)
	}
}
