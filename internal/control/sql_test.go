package control

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"path/filepath"
	"testing"
)

func TestSQLRepositoryCASAndGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	db, e := sqlstore.Open(ctx, "sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	s := &SQL{DB: db}
	r := Repository{ID: ID("context4ai/context"), Name: "context4ai/context", Revision: 1}
	if e = s.Create(ctx, r); e != nil {
		t.Fatal(e)
	}
	lease := r
	lease.LeaseRevision = 1
	if e = s.ReplaceLease(ctx, lease, 1); e != nil {
		t.Fatal(e)
	}
	r.Revision = 2
	if e = s.Replace(ctx, r, 1); e == nil {
		t.Fatal("stale lease overwrite succeeded")
	}
	lease.Revision = 2
	if e = s.Replace(ctx, lease, 1); e != nil {
		t.Fatal(e)
	}
	a, e := s.NextGeneration(ctx)
	if e != nil || a != 1 {
		t.Fatal(a, e)
	}
	db.Close()
	db, e = sqlstore.Open(ctx, "sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s.DB = db
	got, e := s.Get(ctx, r.Name)
	if e != nil || got.LeaseRevision != 1 || got.Revision != 2 {
		t.Fatalf("private fields lost: %+v %v", got, e)
	}
	b, e := s.NextGeneration(ctx)
	if e != nil || b != 2 {
		t.Fatal(b, e)
	}
}
