package control

import (
	"testing"
	"time"
)

func TestUnchangedSyncHistory(t *testing.T) {
	now := time.Now().UTC()
	r := Repository{Enabled: true, PolicyRevision: 1, Heads: map[string]string{"main": "sha"}, Versions: []Version{{Commit: "sha", Generation: 1}}}
	if !r.SyncUnchanged("", map[string]string{"main": "sha"}) {
		t.Fatal("ready unchanged head not detected")
	}
	if r.SyncUnchanged("", map[string]string{"main": "other"}) {
		t.Fatal("changed head skipped")
	}
	r.Versions[0].Generation = 0
	if r.SyncUnchanged("", r.Heads) {
		t.Fatal("missing index skipped")
	}
	for i := 0; i < 120; i++ {
		j := Job{ID: NewID(), Kind: "sync", Source: SourceAutomatic, State: "running", NoChanges: true, Owner: "worker", Fence: 1, PolicyRevision: 1, Created: now.Add(time.Duration(i) * time.Minute), LeaseUntil: now.Add(24 * time.Hour)}
		r.Jobs = append(r.Jobs, j)
		if err := r.Complete(j, "", j.Created.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		r.Prune(j.Created.Add(time.Second))
	}
	if len(r.Jobs) != 0 || r.SyncChecks[""].UnchangedCount != 120 {
		t.Fatalf("unbounded history: %+v", r)
	}
	for _, j := range []Job{
		{ID: "manual", Kind: "sync", Source: SourceManual, NoChanges: true, State: "succeeded", Updated: now},
		{ID: "failed", Kind: "sync", Source: SourceAutomatic, NoChanges: true, State: "failed", Updated: now},
		{ID: "index", Kind: "index", Source: SourceAutomatic, State: "succeeded", Updated: now},
		{ID: "running", Kind: "sync", Source: SourceAutomatic, NoChanges: true, State: "running", Updated: now},
		{ID: "protected", Kind: "sync", Source: SourceAutomatic, NoChanges: true, State: "succeeded", Updated: now},
	} {
		r.Jobs = append(r.Jobs, j)
	}
	r.Receipts = []Receipt{{JobID: "protected", Expires: now.Add(time.Hour)}}
	r.Prune(now)
	if len(r.Jobs) != 5 {
		t.Fatal("important job or idempotency receipt removed")
	}
}

func TestSyncSummaryScopesAndFailure(t *testing.T) {
	now := time.Now().UTC()
	r := Repository{StorageID: "disk"}
	r.RecordSyncCheck(Job{Group: "a", Kind: "sync", Source: SourceAutomatic, State: "succeeded", NoChanges: true, Created: now, Updated: now, PolicyRevision: 2})
	r.RecordSyncCheck(Job{Group: "b", Kind: "sync", Source: SourceAutomatic, State: "succeeded", Created: now, Updated: now, PolicyRevision: 2})
	r.RecordSyncCheck(Job{Group: "a", Kind: "sync", State: "failed", Error: "FETCH_FAILED", Created: now.Add(time.Minute), Updated: now.Add(time.Minute), PolicyRevision: 2})
	a := r.SyncChecks["a"]
	if a.NoChanges || a.UnchangedCount != 0 || a.Error != "FETCH_FAILED" || !a.SuccessStartedAt.Equal(now) || a.SuccessSource != SourceAutomatic {
		t.Fatalf("invalid summary: %+v", a)
	}
	if !r.SyncChecks["b"].CheckedAt.Equal(now) {
		t.Fatal("scope contaminated")
	}
}
