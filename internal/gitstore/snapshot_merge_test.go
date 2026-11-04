package gitstore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A retained merge commit in a shallow, blob-filtered group mirror must report
// its committer time without loading its parents, which are not local.
func TestSnapshotOfMergeCommitInPartialMirror(t *testing.T) {
	work := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.test", "GIT_COMMITTER_DATE=1700000000 +0000")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	write := func(name, body string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(work, name), []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	run(work, "init", "-q", "-b", "main")
	run(work, "config", "uploadpack.allowFilter", "true")
	write("a.txt", "base\n")
	write("b.txt", "base\n")
	run(work, "add", ".")
	run(work, "commit", "-qm", "base")
	run(work, "checkout", "-q", "-b", "side")
	write("a.txt", "side\n")
	run(work, "commit", "-qam", "side")
	run(work, "checkout", "-q", "main")
	write("b.txt", "main\n")
	run(work, "commit", "-qam", "main")
	run(work, "merge", "-q", "--no-ff", "-m", "merge", "side")
	merge := run(work, "rev-parse", "HEAD")

	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if s, err = s.ForGroup("org/repo", "src"); err != nil {
		t.Fatal(err)
	}
	dir, err := GroupRepositoryDir(root, "org/repo", "src")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	// Production state: the merge is retained but no longer a shallow boundary,
	// and its parents' objects were never fetched into this snapshot mirror.
	run(root, "-c", "protocol.file.allow=always", "clone", "-q", "--bare", "--depth=1", "--filter=blob:none", "--no-local", "file://"+work, dir)
	if err = os.WriteFile(filepath.Join(dir, "shallow"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	commit, err := s.Snapshot(context.Background(), "org/repo", merge)
	if err != nil {
		t.Fatalf("snapshot of a merge commit without local parent blobs: %v", err)
	}
	if commit.SHA != merge || strconv.FormatInt(commit.CommitterTime.Unix(), 10) != "1700000000" {
		t.Fatalf("commit = %+v", commit)
	}
}
