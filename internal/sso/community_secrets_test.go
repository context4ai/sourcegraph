package sso

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/secretstore"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"path/filepath"
	"testing"
)

func TestOAuthCredentialsPersistWithoutEnvironment(t *testing.T) {
	ctx := context.Background()
	db, e := sqlstore.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	vault, e := secretstore.NewSQL(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	t.Setenv("GITHUB_CLIENT_ID", "")
	t.Setenv("GITHUB_CLIENT_SECRET", "")
	empty, e := communityCredentials(ctx, []CommunitySecrets{vault})
	if e != nil || empty != (oauthCredentials{}) {
		t.Fatal(e)
	}
	t.Setenv("GITHUB_CLIENT_ID", "example-client")
	t.Setenv("GITHUB_CLIENT_SECRET", "example-secret")
	first, e := communityCredentials(ctx, []CommunitySecrets{vault})
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("GITHUB_CLIENT_ID", "")
	t.Setenv("GITHUB_CLIENT_SECRET", "")
	fresh, e := secretstore.NewSQL(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	after, e := communityCredentials(ctx, []CommunitySecrets{fresh})
	if e != nil || after != first {
		t.Fatal("OAuth identity changed after restart", e)
	}
	t.Setenv("GITHUB_CLIENT_ID", "different-client")
	t.Setenv("GITHUB_CLIENT_SECRET", "different-secret")
	after, e = communityCredentials(ctx, []CommunitySecrets{fresh})
	if e != nil || after != first {
		t.Fatal("database credentials overwritten", e)
	}
}
