package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/identity"
)

type readManyStore struct {
	Store
	repo  control.Repository
	calls int
}

func (db *readManyStore) Get(context.Context, string) (control.Repository, error) {
	db.calls++
	return db.repo, nil
}

func readManyGit(t *testing.T, path, input string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"--git-dir=" + path}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
	c.Stdin = strings.NewReader(input)
	data, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func readManyTree(t *testing.T, path string, files map[string]string) string {
	t.Helper()
	groups := map[string]map[string]string{}
	rows := []string{}
	for name, content := range files {
		parent, child, nested := strings.Cut(name, "/")
		if nested {
			if groups[parent] == nil {
				groups[parent] = map[string]string{}
			}
			groups[parent][child] = content
		} else {
			oid := readManyGit(t, path, content, "hash-object", "-w", "--stdin")
			rows = append(rows, "100644 blob "+oid+"\t"+name+"\n")
		}
	}
	for name, children := range groups {
		oid := readManyTree(t, path, children)
		rows = append(rows, "040000 tree "+oid+"\t"+name+"\n")
	}
	sort.Strings(rows)
	return readManyGit(t, path, strings.Join(rows, ""), "mktree")
}

func readManyFixture(t *testing.T, files map[string]string) (*Service, *readManyStore, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := gitstore.RepositoryDir(root, "org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(path, 0750); err != nil {
		t.Fatal(err)
	}
	readManyGit(t, path, "", "init", "--bare")
	tree := readManyTree(t, path, files)
	sha := readManyGit(t, path, "", "commit-tree", tree, "-m", "fixture")
	readManyGit(t, path, "", "update-ref", "refs/heads/main", sha)
	g, err := gitstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	db := &readManyStore{repo: control.Repository{Name: "org/repo", Enabled: true, Policy: control.Policy{Branches: []string{"main"}}, Heads: map[string]string{"main": sha}, Versions: []control.Version{{Commit: sha, Generation: 1}}}}
	s := &Service{Root: root, Git: g, DB: db, metrics: Metrics{queue: make(chan observation, 16)}}
	return s, db, path, sha
}

func readManyAssertError(t *testing.T, item ReadManyItem, code string) {
	t.Helper()
	if item.Error == nil || item.Error.Code != code {
		t.Fatalf("expected %s, got %+v", code, item.Error)
	}
}

func TestReadManyPinsCommitAndIsolatesFailures(t *testing.T) {
	s, db, path, sha := readManyFixture(t, map[string]string{"good": "one\n二\n", "dir/child": "nested\n", "binary": "a\x00b", "badutf8": "\xff\n"})
	// The physical branch has moved beyond the observed repository snapshot.
	newTree := readManyTree(t, path, map[string]string{"good": "changed\n"})
	newSHA := readManyGit(t, path, "", "commit-tree", newTree, "-p", sha, "-m", "later")
	readManyGit(t, path, "", "update-ref", "refs/heads/main", newSHA)
	requests := []contract.ReadItemRequest{{Path: "good", StartLine: 1, EndLine: 2}, {Path: "missing", StartLine: 1, EndLine: 1}, {Path: "dir", StartLine: 1, EndLine: 1}, {Path: "good", StartLine: 7, EndLine: 8}, {Path: "../good", StartLine: 1, EndLine: 1}, {Path: "good", StartLine: 0, EndLine: 1}, {Path: "binary", StartLine: 1, EndLine: 1}, {Path: "badutf8", StartLine: 1, EndLine: 1}, {Path: "dir/child", StartLine: 1, EndLine: 2}}
	out, err := s.ReadMany(context.Background(), "org/repo", "", requests)
	if err != nil || out.Commit != sha || db.calls != 1 || len(out.Items) != len(requests) {
		t.Fatalf("snapshot: %+v, calls=%d, err=%v", out, db.calls, err)
	}
	for i, code := range []string{"FILE_NOT_FOUND", "NOT_A_FILE", "LINE_RANGE_NOT_SATISFIABLE", "INVALID_PATH", "INVALID_LINE_RANGE", "BINARY_FILE", "UNSUPPORTED_TEXT_ENCODING"} {
		readManyAssertError(t, out.Items[i+1], code)
	}
	if out.Items[0].File.Content != "one\n二\n" || out.Items[8].File.Content != "nested\n" || out.Items[8].File.Commit != sha {
		t.Fatal("mixed batch lost content or changed its commit")
	}
	if out.ReturnedLines != 3 || out.ReturnedBytes != len("one\n二\nnested\n") {
		t.Fatal("failed items spent content budget")
	}
	observation := <-s.metrics.queue
	if observation.op != "read_many" || !observation.failed || len(s.metrics.queue) != 0 {
		t.Fatal("batch should have one failed operation observation")
	}
}

func TestReadManyByteBudgetPreservesLinesAndContinues(t *testing.T) {
	fill := strings.Repeat("x", MaxReadManyBytes-9) + "\n"
	s, _, _, _ := readManyFixture(t, map[string]string{"fill": fill, "unicode": "好\n后续\n", "small": "ok\n", "one": "x"})
	requests := []contract.ReadItemRequest{{Path: "fill", StartLine: 1, EndLine: 1}, {Path: "unicode", StartLine: 1, EndLine: 2}, {Path: "small", StartLine: 1, EndLine: 1}, {Path: "small", StartLine: 1, EndLine: 1}, {Path: "one", StartLine: 1, EndLine: 1}, {Path: "one", StartLine: 1, EndLine: 1}}
	out, err := s.ReadMany(context.Background(), "org/repo", "main", requests)
	if err != nil || out.ReturnedBytes != MaxReadManyBytes || out.ReturnedLines != 4 {
		t.Fatalf("budget: bytes=%d lines=%d err=%v", out.ReturnedBytes, out.ReturnedLines, err)
	}
	partial := out.Items[1]
	readManyAssertError(t, partial, "READ_BUDGET_EXCEEDED")
	if partial.File == nil || partial.File.Content != "好\n" || !utf8.ValidString(partial.File.Content) || *partial.File.ReturnedEndLine != 1 || *partial.File.NextStartLine != 2 || !*partial.File.HasMore || !partial.File.Truncated {
		t.Fatalf("invalid partial: %+v", partial.File)
	}
	if out.Items[2].Error != nil || out.Items[2].File.Content != "ok\n" || out.Items[4].File.Content != "x" {
		t.Fatal("later smaller ranges should still fit")
	}
	readManyAssertError(t, out.Items[3], "READ_BUDGET_EXCEEDED")
	if out.Items[3].File.ReturnedStartLine != nil || *out.Items[3].File.NextStartLine != 1 {
		t.Fatal("zero-line partial fabricated returned lines")
	}
	readManyAssertError(t, out.Items[5], "READ_BUDGET_EXCEEDED")
	if out.Items[5].File != nil {
		t.Fatal("exhausted batch should skip later Git reads")
	}
}

func TestReadManyLineBudgetAndEOF(t *testing.T) {
	s, _, _, _ := readManyFixture(t, map[string]string{"lines": strings.Repeat("x\n", 500), "empty": "", "short": "last"})
	out, err := s.ReadMany(context.Background(), "org/repo", "main", []contract.ReadItemRequest{{Path: "empty", StartLine: 1, EndLine: 500}, {Path: "short", StartLine: 1, EndLine: 500}, {Path: "lines", StartLine: 1, EndLine: 400}, {Path: "lines", StartLine: 1, EndLine: 400}, {Path: "lines", StartLine: 1, EndLine: 400}, {Path: "lines", StartLine: 1, EndLine: 1}})
	if err != nil || out.ReturnedLines != MaxReadManyLines {
		t.Fatalf("lines=%d err=%v", out.ReturnedLines, err)
	}
	if *out.Items[0].File.HasMore || out.Items[0].File.ReturnedStartLine != nil || *out.Items[1].File.HasMore {
		t.Fatal("empty and short EOF metadata changed")
	}
	partial := out.Items[4]
	readManyAssertError(t, partial, "READ_BUDGET_EXCEEDED")
	if *partial.File.ReturnedEndLine != 199 || *partial.File.NextStartLine != 200 || !partial.File.Truncated || !*partial.File.HasMore {
		t.Fatalf("line truncation: %+v", partial.File)
	}
	readManyAssertError(t, out.Items[5], "READ_BUDGET_EXCEEDED")
}

func TestReadManyScopePreparationIsIsolated(t *testing.T) {
	s, db, path, sha := readManyFixture(t, map[string]string{"ready/a": "yes\n", "waiting/b": "not ready\n"})
	db.repo.IndexMode = control.IndexModeMonorepo
	db.repo.PathGroups = []control.PathGroup{{Name: "ready", Paths: []string{"ready"}}, {Name: "waiting", Paths: []string{"waiting"}}}
	db.repo.Scopes = map[string]control.ScopeState{"ready": {Heads: db.repo.Heads, Versions: db.repo.Versions}, "waiting": {Heads: db.repo.Heads, Versions: []control.Version{{Commit: sha}}}}
	groupPath, err := gitstore.GroupRepositoryDir(s.Root, "org/repo", "ready")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(groupPath), 0750); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, groupPath); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReadMany(context.Background(), "org/repo", sha, []contract.ReadItemRequest{{Path: "waiting/b", StartLine: 1, EndLine: 1}, {Path: "outside/a", StartLine: 1, EndLine: 1}, {Path: "ready/a", StartLine: 1, EndLine: 1}})
	if err != nil {
		t.Fatal(err)
	}
	readManyAssertError(t, out.Items[0], "CONTENT_NOT_READY")
	readManyAssertError(t, out.Items[1], "PATH_NOT_REGISTERED")
	if out.Items[2].File == nil || out.Items[2].File.Content != "yes\n" {
		t.Fatalf("prepared group failed: %+v", out.Items[2])
	}
}

func TestReadManyValidationAndCancellation(t *testing.T) {
	s := &Service{}
	for _, count := range []int{0, 11} {
		_, err := s.ReadMany(context.Background(), "org/repo", "", make([]contract.ReadItemRequest, count))
		var problem *contract.Error
		if !errors.As(err, &problem) || problem.Code != "INVALID_READ_ITEMS" {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
	q := []contract.ReadItemRequest{{Path: "file", StartLine: 1, EndLine: 1}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadMany(ctx, "org/repo", "", q); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if _, err := s.ReadMany(ctx, "org/repo", "", q); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait exceeded context: %v", err)
	}
}

func TestReadManyCancellationStopsLaterGitReads(t *testing.T) {
	s, _, _, _ := readManyFixture(t, map[string]string{"file": "one\n"})
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrappers, logPath := t.TempDir(), filepath.Join(t.TempDir(), "reads")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\ncase \"$*\" in *'cat-file blob'*) printf 'read\\n' >> " + quote(logPath) + "; while :; do sleep 1; done;; esac\nexec " + quote(realGit) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(wrappers, "git"), []byte(script), 0750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrappers+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.Git, err = gitstore.New(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	// Cancellation and deadline behavior are separate contracts. Wait for the
	// actual blob read before canceling; a slow Git startup must not consume a
	// caller deadline and accidentally turn this into a timeout test.
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		out ReadManyResult
		err error
	}
	results := make(chan result, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		out, err := s.ReadMany(ctx, "org/repo", "main", []contract.ReadItemRequest{{Path: "file", StartLine: 1, EndLine: 1}, {Path: "file", StartLine: 1, EndLine: 1}})
		results <- result{out, err}
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("batch worker did not stop after cancellation")
		}
	}()
	readyTimeout := time.NewTimer(10 * time.Second)
	defer readyTimeout.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
waitForRead:
	for {
		select {
		case early := <-results:
			t.Fatalf("batch returned before blob read started: items=%d err=%v", len(early.out.Items), early.err)
		case <-readyTimeout.C:
			t.Fatal("Git blob read did not start before the test watchdog expired")
		case <-ticker.C:
			if _, err := os.Stat(logPath); err == nil {
				break waitForRead
			} else if !os.IsNotExist(err) {
				t.Fatalf("cannot observe blob-read marker: %v", err)
			}
		}
	}
	cancel()
	select {
	case got := <-results:
		if !errors.Is(got.err, context.Canceled) || len(got.out.Items) != 0 {
			t.Fatalf("canceled batch should stop: items=%d err=%v", len(got.out.Items), got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not return after explicit cancellation")
	}
	log, err := os.ReadFile(logPath)
	if err != nil || string(log) != "read\n" {
		t.Fatalf("later Git reads started: %q err=%v", log, err)
	}
}

func TestReadManyPrivateRepositoryStaysHidden(t *testing.T) {
	private := false
	db := &readManyStore{repo: control.Repository{Name: "org/repo", Enabled: true, PublicRead: &private}}
	s := &Service{DB: db}
	ctx := identity.WithAnonymous(context.Background(), "anonymous")
	out, err := s.ReadMany(ctx, "org/repo", "main", []contract.ReadItemRequest{{Path: "file", StartLine: 1, EndLine: 1}})
	var problem *contract.Error
	if !errors.As(err, &problem) || problem.Code != "REPOSITORY_NOT_FOUND" || len(out.Items) != 0 || out.Commit != "" || db.calls != 1 {
		t.Fatalf("private repository leaked: %+v %v", out, err)
	}
}

func TestReadManyDeadlineReturnsCompletedItemsBeforeCallerExpires(t *testing.T) {
	s, _, path, _ := readManyFixture(t, map[string]string{"file": "one\n", "slow": "two\n"})
	slowOID := readManyGit(t, path, "two\n", "hash-object", "--stdin")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrappers := t.TempDir()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\ncase \"$*\" in *'cat-file blob " + slowOID + "'*) while :; do sleep 1; done;; esac\nexec " + quote(realGit) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(wrappers, "git"), []byte(script), 0750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrappers+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.Git, err = gitstore.New(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	out, err := s.ReadMany(ctx, "org/repo", "main", []contract.ReadItemRequest{{Path: "file", StartLine: 1, EndLine: 1}, {Path: "slow", StartLine: 1, EndLine: 1}, {Path: "file", StartLine: 1, EndLine: 1}})
	if err != nil || len(out.Items) != 3 || out.Items[0].File == nil || out.Items[0].File.Content != "one\n" || ctx.Err() != nil {
		t.Fatalf("deadline lost completed result or response margin: %+v err=%v callerErr=%v", out, err, ctx.Err())
	}
	readManyAssertError(t, out.Items[1], "READ_TIMEOUT")
	readManyAssertError(t, out.Items[2], "READ_TIMEOUT")
	if out.ReturnedBytes != 4 || out.ReturnedLines != 1 || !(<-s.metrics.queue).failed {
		t.Fatal("deadline lost accurate content counters or failure telemetry")
	}
}

func TestReadManyOversizedRangeContinuesAndPreservesOtherResults(t *testing.T) {
	s, _, _, _ := readManyFixture(t, map[string]string{"good": "one\n", "long": strings.Repeat("line\n", 620)})
	out, err := s.ReadMany(context.Background(), "org/repo", "", []contract.ReadItemRequest{{Path: "good", StartLine: 1, EndLine: 1}, {Path: "long", StartLine: 1, EndLine: 620}, {Path: "good", StartLine: 0, EndLine: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Items[0].File == nil || out.Items[0].File.Content != "one\n" {
		t.Fatal("valid item lost")
	}
	f := out.Items[1].File
	if out.Items[1].Error != nil || f == nil || !f.Truncated || !*f.HasMore || *f.NextStartLine != 501 || *f.ReturnedEndLine != 500 || out.ReturnedLines != 501 {
		t.Fatalf("bad partial: %+v", out)
	}
	readManyAssertError(t, out.Items[2], "INVALID_LINE_RANGE")
	if !strings.Contains(out.Items[2].Error.Message, "Item 3:") {
		t.Fatal(out.Items[2].Error)
	}
	next, err := s.Read(context.Background(), "org/repo", contract.ReadRequest{Path: "long", StartLine: 501, EndLine: 620})
	if err != nil || *next.ReturnedEndLine != 620 || *next.HasMore {
		t.Fatalf("continuation: %+v %v", next, err)
	}
	first, err := s.Read(context.Background(), "org/repo", contract.ReadRequest{Path: "long", StartLine: 1, EndLine: 620})
	if err != nil || !first.Truncated || *first.NextStartLine != 501 {
		t.Fatalf("single: %+v %v", first, err)
	}
	short, err := s.Read(context.Background(), "org/repo", contract.ReadRequest{Path: "good", StartLine: 1, EndLine: 620})
	if err != nil || short.Truncated || *short.HasMore {
		t.Fatalf("short file: %+v %v", short, err)
	}
}
