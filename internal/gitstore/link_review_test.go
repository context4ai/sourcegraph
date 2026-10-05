package gitstore

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"testing"
)

func TestLinkHydrationBudgetSkipsOnlyEntrance(t *testing.T) {
	s, sha := linkFixture(t)
	ctx := context.Background()
	entries, err := s.SnapshotEntries(ctx, "org/repo", sha, []string{"context", "valid"})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"GIT_HYDRATION_BUDGET_EXCEEDED", "GIT_FETCH_FAILED"} {
		t.Run(code, func(t *testing.T) {
			hydrate := func(items []Entry) error {
				for _, e := range items {
					if e.Path == "docs/a.md" {
						return contract.Fail(code, "fixture", 503)
					}
				}
				return nil
			}
			out, m, err := s.ExpandLinks(ctx, "org/repo", sha, entries, []string{"context", "valid"}, DefaultLinkLimits(), hydrate, func(string, []byte) bool { return true })
			if code == "GIT_FETCH_FAILED" {
				if err == nil {
					t.Fatal("network failure hidden")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			paths := map[string]bool{}
			for _, e := range out {
				paths[e.Path] = true
			}
			if !paths["context/knowledge.md"] || !paths["valid"] || paths["context/view/a.md"] || m.Skipped["LINK_TARGET_TOO_LARGE"] != 2 {
				t.Fatalf("%v %+v", paths, m)
			}
		})
	}
}
func TestMissingFileUnderValidLinkIsNotDangling(t *testing.T) {
	s, sha := linkFixture(t)
	for path, want := range map[string]string{"context/view/missing.md": "FILE_NOT_FOUND", "bad/file.md": "LINK_DANGLING"} {
		_, err := s.ResolvePath(context.Background(), "org/repo", sha, path)
		var e *contract.Error
		if !errors.As(err, &e) || e.Code != want {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestActualHydratorBudgetRejectsEntranceAndAllowsSmallerOne(t *testing.T) {
	s, sha := linkFixture(t)
	ctx := context.Background()
	entries, err := s.SnapshotEntries(ctx, "org/repo", sha, []string{"context", "valid"})
	if err != nil {
		t.Fatal(err)
	}
	hydrate := s.BudgetLinkHydrator(ctx, "org/repo", sha, func([]Entry) error { return nil }, 24)
	out, m, err := s.ExpandLinks(ctx, "org/repo", sha, entries, []string{"context", "valid"}, DefaultLinkLimits(), hydrate, func(string, []byte) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, e := range out {
		paths[e.Path] = true
	}
	if !paths["context/knowledge.md"] || !paths["valid"] || paths["context/view/a.md"] || m.Skipped["LINK_TARGET_TOO_LARGE"] != 2 {
		t.Fatalf("%v %+v", paths, m)
	}
}
