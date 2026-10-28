package service

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotRetentionPreservesPinsHeadsAndActiveJobs(t *testing.T) {
	now := time.Now().UTC()
	v := func(c string, age time.Duration) control.Version {
		return control.Version{Commit: strings.Repeat(c, 40), Time: now.Add(-365 * 24 * time.Hour), ObservedAt: now.Add(-age)}
	}
	versions := []control.Version{v("a", 100*time.Hour), v("b", 99*time.Hour), v("c", 98*time.Hour), v("d", time.Hour), v("e", 50*time.Hour)}
	r := control.Repository{Policy: control.Policy{Days: 1}, PinnedCommits: []string{versions[0].Commit}, Jobs: []control.Job{{State: "queued", RequestedRevision: versions[2].Commit}}}
	heads := map[string]string{"main": versions[1].Commit}
	out := retainedSnapshots(r, versions, heads, now)
	if len(out) != 4 {
		t.Fatalf("retained %v", out)
	}
	for _, x := range out {
		if x.Commit == versions[4].Commit {
			t.Fatal("expired ordinary snapshot retained")
		}
	}
	r.PinnedCommits = nil
	r.Jobs = nil
	out = retainedSnapshots(r, versions, heads, now)
	if len(out) != 2 {
		t.Fatal("released pin was not expired", out)
	}
}
func TestSnapshotBudgetCountsTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	s := &Service{Root: root}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "pending.pack"), make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	err := s.checkRepositoryBudget(context.Background(), control.Repository{Name: "org/repo", MaxStorageBytes: 4096}, stage)
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != "REPOSITORY_STORAGE_LIMIT" {
		t.Fatalf("budget: %v", err)
	}
}
func TestSnapshotSettingsPreserveVersionsAndClearPins(t *testing.T) {
	sha := strings.Repeat("a", 40)
	policy := control.Policy{Branches: []string{"main"}, Count: 10}
	db := &managementStore{repo: control.Repository{Name: "org/repo", Enabled: true, Revision: 4, Policy: policy, PinnedCommits: []string{sha}, Versions: []control.Version{{Commit: sha, Generation: 1}}}}
	s := &Service{DB: db, Root: t.TempDir()}
	pins := []string{}
	limit := int64(2 << 30)
	u := Update{Policy: policy, Enabled: true, PinnedCommits: &pins, MaxStorageBytes: &limit}
	u.Preview = preview(db.repo, u)
	if _, err := s.Update(context.Background(), "org/repo", u, 4); err != nil {
		t.Fatal(err)
	}
	if len(db.repo.PinnedCommits) != 0 || db.repo.MaxStorageBytes != limit || len(db.repo.Versions) != 1 {
		t.Fatal("settings lost versions or ignored values")
	}
}

func TestMissingSHAIndexSelectionQueuesDurablePrepare(t *testing.T) {
	ctx := context.Background()
	db := &managementStore{repo: control.Repository{Name: "org/repo", Enabled: true, Revision: 1, Policy: control.Policy{Branches: []string{"main"}, Count: 2}}}
	s := &Service{DB: db, Root: t.TempDir()}
	q := IndexSelection{Branch: "main", Commit: strings.Repeat("b", 40)}
	p, err := s.PreviewIndex(ctx, "org/repo", q)
	if err != nil || p.AlreadyIndexed {
		t.Fatalf("preview: %+v %v", p, err)
	}
	q.Preview = p.Preview
	first, err := s.CreateIndex(ctx, "org/repo", q, p.Revision, "snapshot-request")
	if err != nil {
		t.Fatal(err)
	}
	job, ok := first.(control.Job)
	if !ok || job.Kind != "prepare" || job.RequestedRevision != q.Commit {
		t.Fatalf("not an exact preparation: %+v", first)
	}
	replay, err := s.CreateIndex(ctx, "org/repo", q, p.Revision, "snapshot-request")
	if err != nil || replay.(control.Job).ID != job.ID || len(db.repo.Jobs) != 1 {
		t.Fatalf("lost response replay: %+v %v", replay, err)
	}
}

func TestSnapshotCountExcludesProtectedVersions(t *testing.T) {
	now := time.Now()
	vs := []control.Version{}
	for _, c := range []string{"a", "b", "c", "d"} {
		vs = append(vs, control.Version{Commit: strings.Repeat(c, 40), ObservedAt: now})
	}
	r := control.Repository{Policy: control.Policy{Count: 1}, PinnedCommits: []string{vs[0].Commit}}
	out := retainedSnapshots(r, vs, map[string]string{"main": vs[1].Commit}, now)
	if len(out) != 3 || out[2].Commit != vs[2].Commit {
		t.Fatalf("count/protection: %+v", out)
	}
}
