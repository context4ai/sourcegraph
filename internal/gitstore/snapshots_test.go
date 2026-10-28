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

func TestShallowSnapshotsIncrementalHistoricalAndCollection(t *testing.T) {
	for _, group := range []string{"", "src", "existing-full"} {
		t.Run("group-"+group, func(t *testing.T) {
			initialFull := group == "existing-full"
			if initialFull {
				group = ""
			}
			work := t.TempDir()
			run := func(args ...string) string {
				t.Helper()
				c := exec.Command("git", args...)
				c.Dir = work
				c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.test")
				b, e := c.CombinedOutput()
				if e != nil {
					t.Fatalf("%v: %v %s", args, e, b)
				}
				return strings.TrimSpace(string(b))
			}
			run("init", "-q", "-b", "main")
			run("config", "uploadpack.allowFilter", "true")
			run("config", "uploadpack.allowAnySHA1InWant", "true")
			commit := func(body string) string {
				t.Helper()
				if e := os.WriteFile(filepath.Join(work, "file.txt"), []byte(body), 0600); e != nil {
					t.Fatal(e)
				}
				run("add", ".")
				run("commit", "-qm", body)
				return run("rev-parse", "HEAD")
			}
			if e := os.WriteFile(filepath.Join(work, "unselected.txt"), []byte("never hydrate this object"), 0600); e != nil {
				t.Fatal(e)
			}
			old := commit("old content")
			oldBlob := run("rev-parse", "HEAD:file.txt")
			middle := commit("middle content")
			head := commit("head content")
			root := t.TempDir()
			s, e := New(root)
			if e != nil {
				t.Fatal(e)
			}
			if group != "" {
				s, e = s.ForGroup("org/repo", group)
				if e != nil {
					t.Fatal(e)
				}
			}
			// Exercise real Git transport and depth handling; only rewrite the fixture URL.
			realGit, e := exec.LookPath("git")
			if e != nil {
				t.Fatal(e)
			}
			wrapper := filepath.Join(t.TempDir(), "git")
			script := "#!/bin/sh\nallow=never\nfor arg in \"$@\"; do case \"$arg\" in fetch|ls-remote) allow=always;; esac; done\nexec '" + realGit + "' -c protocol.file.allow=$allow -c 'url.file://" + work + ".insteadOf=https://fixture.example/org/repo.git' \"$@\"\n"
			if e = os.WriteFile(wrapper, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			s.git = wrapper
			ctx := context.Background()
			options := FetchOptions{Host: "https://fixture.example", Anonymous: true, CredentialHelper: "/bin/false", Branches: []string{"main"}, Snapshot: !initialFull}
			if e = s.Fetch(ctx, "org/repo", options); e != nil {
				t.Fatal(e)
			}
			hydrate := func(sha string) {
				t.Helper()
				if group == "" {
					return
				}
				entries, err := s.SnapshotEntries(ctx, "org/repo", sha, []string{"file.txt"})
				if err != nil {
					t.Fatal(err)
				}
				if err = s.HydrateEntries(ctx, "org/repo", sha, entries, options); err != nil {
					t.Fatal(err)
				}
			}
			hydrate(head)
			p, _ := s.repoPath("org/repo")
			if _, e = s.Resolve(ctx, "org/repo", middle); e == nil && !initialFull {
				t.Fatal("initial fetch downloaded ancestor")
			}
			if _, e = s.Snapshot(ctx, "org/repo", head); e != nil {
				t.Fatal(e)
			}
			if e = s.RetainSnapshots(ctx, "org/repo", []string{head}, func() error { return errors.New("capacity interruption") }); e == nil {
				t.Fatal("collection ignored capacity failure")
			}
			if _, e = s.Snapshot(ctx, "org/repo", head); e != nil {
				t.Fatal("interrupted collection lost head", e)
			}
			if e = s.RetainSnapshots(ctx, "org/repo", []string{head}); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Resolve(ctx, "org/repo", middle); e == nil {
				t.Fatal("collection retained parent history")
			}
			options.Snapshot = true
			next := commit("new head")
			if e = s.Fetch(ctx, "org/repo", options); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Snapshot(ctx, "org/repo", head); e != nil {
				t.Fatal("incremental lost old snapshot", e)
			}
			hydrate(next)
			options.Revision = old
			if e = s.Fetch(ctx, "org/repo", options); e != nil {
				t.Fatal("historical fetch", e)
			}
			hydrate(old)
			if e = s.RetainSnapshots(ctx, "org/repo", []string{old, head, next}); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Snapshot(ctx, "org/repo", old); e != nil {
				t.Fatal(e)
			}
			// Retained snapshots stay readable while replacement packs are published.
			stop := make(chan struct{})
			reads := make(chan error, 1)
			go func() {
				for {
					select {
					case <-stop:
						reads <- nil
						return
					default:
					}
					if _, err := s.run(ctx, p, 1024, "show", next+":file.txt"); err != nil {
						reads <- err
						return
					}
				}
			}()
			e = s.RetainSnapshots(ctx, "org/repo", []string{head, next})
			close(stop)
			if readErr := <-reads; readErr != nil {
				t.Fatal("concurrent retained read", readErr)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.run(ctx, p, 1024, "cat-file", "-e", old); e == nil {
				t.Fatal("expired commit still stored")
			}
			if _, e = s.run(ctx, p, 1024, "cat-file", "-e", oldBlob); e == nil {
				t.Fatal("expired unique blob still stored")
			}
			for _, sha := range []string{head, next} {
				if _, e = s.run(ctx, p, 1024, "show", sha+":file.txt"); e != nil {
					t.Fatal("retained snapshot unreadable", e)
				}
			}
			if e = s.RetainSnapshots(ctx, "org/repo", []string{head, next}); e != nil {
				t.Fatal("collection is not idempotent", e)
			}
			if e = s.RetainSnapshots(ctx, "org/repo", []string{strings.Repeat("f", 40)}); e == nil {
				t.Fatal("missing retained object accepted")
			}
			if _, e = s.Snapshot(ctx, "org/repo", next); e != nil {
				t.Fatal("failed collection lost valid data", e)
			}
		})
	}
}
