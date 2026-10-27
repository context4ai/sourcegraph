package codecredentials

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseImportsLegacyKeyAndSurvivesHostReplacement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	legacy := &FileStore{Path: filepath.Join(root, "credentials.json")}
	old, e := New(root, legacy)
	if e != nil {
		t.Fatal(e)
	}
	token := "existing-private-token"
	if _, e = old.Save(ctx, Global, "github", Input{Token: &token}); e != nil {
		t.Fatal(e)
	}
	row, e := legacy.Get(ctx, recordID(Global, "github"))
	if e != nil {
		t.Fatal(e)
	}
	db, e := sqlstore.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	store := SQLStore{DB: db}
	if e = store.Save(ctx, row, 0); e != nil {
		t.Fatal(e)
	}
	migrated, e := New(root, store)
	if e != nil {
		t.Fatal(e)
	}
	got, e := migrated.Candidates(ctx, "", "github")
	if e != nil || len(got) < 1 || got[0].Token != token {
		t.Fatal("legacy token lost", e)
	}
	if e = os.Remove(filepath.Join(root, "code-credentials.key")); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SOURCEGRAPH_CREDENTIAL_KEY", "stale-invalid-bootstrap-value")
	freshRoot := t.TempDir()
	fresh, e := New(freshRoot, store)
	if e != nil {
		t.Fatal(e)
	}
	got, e = fresh.Candidates(ctx, "", "github")
	if e != nil || len(got) < 1 || got[0].Token != token {
		t.Fatal("replacement host token lost", e)
	}
	if _, e = os.Stat(filepath.Join(freshRoot, "code-credentials.key")); !os.IsNotExist(e) {
		t.Fatal("new local key created")
	}
}
