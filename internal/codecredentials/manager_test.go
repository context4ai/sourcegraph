package codecredentials

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	m, err := New(root, &FileStore{Path: filepath.Join(root, "credentials.json")})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestGitHubTriesAnonymousAfterTokens(t *testing.T) {
	ctx := context.Background()
	m := testManager(t)
	got, err := m.Candidates(ctx, "user-1", "github")
	if err != nil || len(got) != 1 || got[0].Token != "" {
		t.Fatalf("anonymous candidate = %#v, %v", got, err)
	}
	m.Report(ctx, got[0], true)
	if w := m.Warnings(ctx); len(w) != 0 {
		t.Fatalf("anonymous failure must not mark credentials invalid: %#v", w)
	}
	token := "ghp_example_token"
	if _, err = m.Save(ctx, Global, "github", Input{Token: &token}); err != nil {
		t.Fatal(err)
	}
	if got, err = m.Candidates(ctx, "user-1", "github"); err != nil || len(got) != 2 || got[0].Token != token || got[1].Token != "" {
		t.Fatalf("token candidate = %#v, %v", got, err)
	}
}

func TestCustomGitRequiresToken(t *testing.T) {
	_, err := testManager(t).Candidates(context.Background(), "user-1", "git")
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != "GIT_CREDENTIAL_UNAVAILABLE" {
		t.Fatalf("custom git without token: %v", err)
	}
}

func TestStatusListsBothSources(t *testing.T) {
	got, err := testManager(t).Status(context.Background(), Global)
	if err != nil || len(got) != 2 {
		t.Fatalf("status = %#v, %v", got, err)
	}
}

func TestEnvironmentTokenFollowsSourceVariable(t *testing.T) {
	t.Setenv("SOURCEGRAPH_GIT_TOKEN", "legacy_github_token")
	got, err := testManager(t).Candidates(context.Background(), "", "github")
	if err != nil || len(got) != 2 || got[0].Token != "legacy_github_token" || got[1].Token != "" {
		t.Fatalf("legacy variable candidates = %#v, %v", got, err)
	}
	t.Setenv("SOURCEGRAPH_GITHUB_TOKEN", "env_github_token")
	if got, err = testManager(t).Candidates(context.Background(), "", "github"); err != nil || got[0].Token != "env_github_token" {
		t.Fatalf("current variable must win: %#v, %v", got, err)
	}
	if _, err = testManager(t).Candidates(context.Background(), "", "git"); err == nil {
		t.Fatal("custom git must not reuse the GitHub variables")
	}
}

func TestLostKeyCanBeReplacedWithoutResettingRecords(t *testing.T) {
	ctx := context.Background()
	store := &FileStore{Path: filepath.Join(t.TempDir(), "credentials.json")}
	old, e := New(t.TempDir(), store)
	if e != nil {
		t.Fatal(e)
	}
	token := "old-secret-value"
	saved, e := old.Save(ctx, Global, "github", Input{Token: &token})
	if e != nil {
		t.Fatal(e)
	}
	fresh, e := New(t.TempDir(), store)
	if e != nil {
		t.Fatal(e)
	}
	states, e := fresh.Status(ctx, Global)
	if e != nil || states["github"].State != "unreadable" || states["github"].Revision != saved.Revision {
		t.Fatalf("status %#v: %v", states, e)
	}
	if _, e = fresh.Save(ctx, Global, "github", Input{Revision: saved.Revision}); !errors.Is(e, unreadable) {
		t.Fatalf("metadata-only update must not discard unreadable secret: %v", e)
	}
	replacement := "replacement-secret-value"
	if _, e = fresh.Save(ctx, Global, "github", Input{Revision: saved.Revision - 1, Token: &replacement}); !errors.Is(e, conflict) {
		t.Fatalf("CAS bypassed: %v", e)
	}
	if _, e = fresh.Save(ctx, Global, "github", Input{Revision: saved.Revision, Token: &replacement}); e != nil {
		t.Fatal(e)
	}
	got, e := fresh.Candidates(ctx, "", "github")
	if e != nil || len(got) < 1 || got[0].Token != replacement {
		t.Fatal("replacement unusable", e)
	}
}

func TestUnreadablePersonalCredentialDoesNotBlockGlobal(t *testing.T) {
	ctx := context.Background()
	store := &FileStore{Path: filepath.Join(t.TempDir(), "credentials.json")}
	old, e := New(t.TempDir(), store)
	if e != nil {
		t.Fatal(e)
	}
	token := "personal-old-token"
	if _, e = old.Save(ctx, "user-1", "github", Input{Token: &token}); e != nil {
		t.Fatal(e)
	}
	fresh, e := New(t.TempDir(), store)
	if e != nil {
		t.Fatal(e)
	}
	token = "new-global-token"
	if _, e = fresh.Save(ctx, Global, "github", Input{Token: &token}); e != nil {
		t.Fatal(e)
	}
	got, e := fresh.Candidates(ctx, "user-1", "github")
	if e != nil || len(got) < 1 || got[0].Token != token {
		t.Fatal("unreadable personal credential blocked fallback", e)
	}
}
