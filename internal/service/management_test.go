package service

import (
	"context"
	"testing"

	"github.com/context4ai/sourcegraph/internal/control"
)

type managementStore struct {
	Store
	repo control.Repository
}

func (db *managementStore) Get(context.Context, string) (control.Repository, error) {
	return db.repo, nil
}

func (db *managementStore) Replace(_ context.Context, r control.Repository, _ int64) error {
	db.repo = r
	return nil
}

func TestIndexGeneratedSettingKeepsPublishedVersions(t *testing.T) {
	policy := control.Policy{Branches: []string{"main"}, Count: 10}
	versions := []control.Version{{Commit: "1111111111111111111111111111111111111111", Generation: 3}}
	db := &managementStore{repo: control.Repository{Name: "org/repo", Enabled: true, Revision: 4, Policy: policy, Heads: map[string]string{"main": versions[0].Commit}, Versions: versions}}
	s := &Service{DB: db, Root: t.TempDir()}
	if db.repo.IndexGenerated {
		t.Fatal("generated files must be skipped by default")
	}
	on := true
	u := Update{Policy: policy, Enabled: true, IndexGenerated: &on}
	u.Preview = preview(db.repo, u)
	r, err := s.Update(context.Background(), "org/repo", u, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IndexGenerated || !db.repo.IndexGenerated {
		t.Fatal("index_generated was not saved")
	}
	if len(db.repo.Versions) != 1 || db.repo.Versions[0].Generation != 3 || db.repo.PolicyRevision != 0 {
		t.Fatalf("setting must apply to the next build without revoking versions: %+v", db.repo)
	}
	u = Update{Policy: policy, Enabled: true}
	u.Preview = preview(db.repo, u)
	if r, err = s.Update(context.Background(), "org/repo", u, 5); err != nil || !r.IndexGenerated {
		t.Fatalf("omitting index_generated must keep it: %v %v", r.IndexGenerated, err)
	}
}

func TestIndexSymlinksSettingKeepsPublishedVersions(t *testing.T) {
	versions := []control.Version{{Commit: "1111111111111111111111111111111111111111", Generation: 3, LinkFormat: 1}}
	policy := control.Policy{Branches: []string{"main"}, Count: 3}
	db := &managementStore{repo: control.Repository{Name: "org/repo", Revision: 4, PolicyRevision: 2, Enabled: true, Policy: policy, Versions: versions}}
	s := &Service{DB: db, Root: t.TempDir()}
	off := false
	u := Update{Policy: policy, Enabled: true, IndexSymlinks: &off}
	u.Preview = preview(db.repo, u)
	r, err := s.Update(context.Background(), "org/repo", u, 4)
	if err != nil || r.IndexSymlinks == nil || *r.IndexSymlinks || r.PolicyRevision != 2 || r.Versions[0].Generation != 3 || r.Versions[0].LinkFormat != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}
