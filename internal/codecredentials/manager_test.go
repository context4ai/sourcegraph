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

func TestOnlyRetiredSourcesMove(t *testing.T) {
	ctx := context.Background()
	m := testManager(t)
	token := "custom_git_token"
	saved, err := m.Save(ctx, Global, "git", Input{Token: &token})
	if err != nil {
		t.Fatal(err)
	}
	var ce *contract.Error
	if _, err = m.Move(ctx, Global, "git", "github", saved.Revision); !errors.As(err, &ce) || ce.Code != "INVALID_CREDENTIAL_MOVE" {
		t.Fatalf("visible source moved: %v", err)
	}
}
