package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
	"github.com/context4ai/sourcegraph/internal/indexer"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
)

type collectionStore struct {
	Store
	repos map[string]control.Repository
}

func (db *collectionStore) Get(_ context.Context, name string) (control.Repository, error) {
	r, ok := db.repos[name]
	if !ok {
		return r, contract.Fail("REPOSITORY_NOT_FOUND", "Repository is not available.", 404)
	}
	return r, nil
}
func (db *collectionStore) Replace(_ context.Context, r control.Repository, _ int64) error {
	db.repos[r.Name] = r
	return nil
}
func (db *collectionStore) List(context.Context, string, int) ([]control.Repository, error) {
	out := []control.Repository{}
	for _, r := range db.repos {
		out = append(out, r)
	}
	return out, nil
}

const collectionSHA = "1111111111111111111111111111111111111111"

// writeGeneration writes one manifest and shard; old moves both past expiredAge.
func writeGeneration(t *testing.T, root, repo string, id uint32, old bool) string {
	t.Helper()
	shard := "snapshot-" + strconv.FormatUint(uint64(id), 10) + "_0.zoekt"
	for _, dir := range []string{"manifests", "index"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	a := indexer.Artifact{Target: zoektclient.Target{Repo: repo, Commit: collectionSHA, RepositoryID: id}, Shards: []string{shard}}
	b, _ := json.Marshal(a)
	manifest := filepath.Join(root, "manifests", strconv.FormatUint(uint64(id), 10)+".json")
	if err := os.WriteFile(manifest, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index", shard), make([]byte, 1000), 0600); err != nil {
		t.Fatal(err)
	}
	if old {
		past := time.Now().Add(-time.Hour)
		os.Chtimes(manifest, past, past)
	}
	return manifest
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestCollectRetiresUnreferencedPerRepositoryWhileAnotherIsBusy(t *testing.T) {
	root := t.TempDir()
	db := &collectionStore{repos: map[string]control.Repository{
		"org/a": {Name: "org/a", Enabled: true, Versions: []control.Version{{Commit: collectionSHA, Generation: 1}}},
		"org/b": {Name: "org/b", Enabled: true},
	}}
	s := &Service{DB: db, Root: root}
	kept := writeGeneration(t, root, "org/a", 1, true)
	expired := writeGeneration(t, root, "org/a", 2, true)
	young := writeGeneration(t, root, "org/a", 3, false)
	busy := writeGeneration(t, root, "org/b", 4, true)
	removedRepo := writeGeneration(t, root, "org/gone", 5, true)
	release, ok := s.acquireRepository("org/b")
	if !ok {
		t.Fatal("could not hold org/b")
	}
	round, err := s.Collect(context.Background())
	release()
	if err != nil {
		t.Fatal(err)
	}
	if !exists(kept) || !exists(young) || !exists(busy) || exists(expired) || exists(removedRepo) {
		t.Fatalf("kept=%v young=%v busy=%v expired=%v gone=%v", exists(kept), exists(young), exists(busy), exists(expired), exists(removedRepo))
	}
	if round.RemovedGenerations != 2 || round.ReclaimedBytes != 2000 || round.BusyRepositories != 1 || round.PendingGenerations != 1 || round.PendingBytes != 1000 {
		t.Fatalf("round = %+v", round)
	}
	if !db.repos["org/a"].DiskUsagePending {
		t.Fatal("collection must request a fresh disk sample")
	}
	if s.IndexCollection().LastReclaimAt.IsZero() {
		t.Fatal("status must record the reclaim")
	}
	if round, _ = s.Collect(context.Background()); round.RemovedGenerations != 1 || exists(busy) {
		t.Fatalf("the busy repository is collected on the next round: %+v", round)
	}
}

func TestStorageBudgetReclaimsExpiredIndexesFirst(t *testing.T) {
	root := t.TempDir()
	// The meter counts allocated 4 KiB blocks: one shard fits the budget, two do not.
	r := control.Repository{Name: "org/a", Enabled: true, MaxStorageBytes: 6000, Versions: []control.Version{{Commit: collectionSHA, Generation: 1}}}
	db := &collectionStore{repos: map[string]control.Repository{"org/a": r}}
	s := &Service{DB: db, Root: root}
	writeGeneration(t, root, "org/a", 1, true)
	expired := writeGeneration(t, root, "org/a", 2, true)
	if err := s.checkRepositoryBudget(context.Background(), r); err != nil {
		t.Fatalf("expired indexes must be reclaimed before refusing: %v", err)
	}
	if exists(expired) {
		t.Fatal("expired generation still present")
	}
	writeGeneration(t, root, "org/a", 3, false) // young, referenced by nothing yet
	var ce *contract.Error
	if err := s.checkRepositoryBudget(context.Background(), r); !errors.As(err, &ce) || ce.Code != "REPOSITORY_STORAGE_LIMIT" {
		t.Fatalf("a build in progress is never reclaimed: %v", err)
	}
}

func TestCleanupFallsBackToExpiredIndexesWhenLatestIsNotReady(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"repos", "manifests", "index"} {
		os.MkdirAll(filepath.Join(root, dir), 0700)
	}
	head := "2222222222222222222222222222222222222222"
	r := control.Repository{Name: "org/a", Enabled: true, Revision: 3, Policy: control.Policy{Branches: []string{"main"}, Count: 10},
		Heads: map[string]string{"main": head}, Versions: []control.Version{{Commit: collectionSHA, Generation: 1}, {Commit: head}}}
	db := &collectionStore{repos: map[string]control.Repository{"org/a": r}}
	s := &Service{DB: db, Root: root}
	writeGeneration(t, root, "org/a", 1, true)
	writeGeneration(t, root, "org/a", 2, true)
	p, err := s.PreviewMaintenance(context.Background(), "org/a", "cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if !p.ExpiredOnly || p.RemoveGenerations != 1 || len(p.artifacts) != 1 || p.artifacts[0].Target.RepositoryID != 2 {
		t.Fatalf("plan = %+v", p)
	}
}

func TestPrepareRequiresAdministratorOrRepositoryManager(t *testing.T) {
	db := &collectionStore{repos: map[string]control.Repository{"org/a": {Name: "org/a", Enabled: true, ManagersSet: true, Managers: []string{"u1"}}}}
	s := &Service{DB: db}
	base := context.Background()
	cases := []struct {
		name string
		ctx  context.Context
		ok   bool
	}{
		{"service credential", identity.WithAdmin(base), true},
		{"administrator token", identity.WithTokenOwnerAdmin(identity.WithActor(base, "api-key:u9:k")), true},
		{"manager session", identity.WithActor(base, "sso:u1"), true},
		{"manager token", identity.WithActor(base, "api-key:u1:k"), true},
		{"other user token", identity.WithActor(base, "api-key:u2:k"), false},
		{"anonymous", identity.WithAnonymous(base, "anon"), false},
	}
	for _, c := range cases {
		if err := s.AuthorizePrepare(c.ctx, "org/a"); (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.name, err)
		}
	}
	if ok, err := s.ManagesAnyRepository(base, "u1"); !ok || err != nil {
		t.Fatalf("u1 manages org/a: %v %v", ok, err)
	}
	if ok, _ := s.ManagesAnyRepository(base, "u2"); ok {
		t.Fatal("u2 manages nothing")
	}
}
