package control

import (
	"fmt"
	"testing"
	"time"
)

func TestFailureArchiveDoesNotFillQueue(t *testing.T) {
	now := time.Now().UTC()
	r := Repository{Enabled: true, PolicyRevision: 1}
	for i := 0; i < 300; i++ {
		r.Jobs = append(r.Jobs, Job{ID: fmt.Sprint(i), Kind: "index", State: "failed", Error: "INDEX_FAILED", Source: SourceAutomatic, Updated: now.Add(-48*time.Hour + time.Duration(i)*time.Minute)})
	}
	r.Prune(now)
	if len(r.Jobs) != 0 || len(r.JobFailures) != JobFailureLimit || r.JobFailures[0].ID != "299" {
		t.Fatalf("bad archive: jobs=%d failures=%d", len(r.Jobs), len(r.JobFailures))
	}
	r.Prune(now)
	if len(r.JobFailures) != JobFailureLimit {
		t.Fatal("archive duplicated or lost")
	}
	if _, err := r.Queue("sync", "", "key", "digest", "actor", now); err != nil {
		t.Fatalf("history blocked work: %v", err)
	}
	r.Prune(now.Add(8 * 24 * time.Hour))
	if len(r.JobFailures) != 0 || len(r.Jobs) != 1 {
		t.Fatal("expiry removed active work or retained stale diagnostics")
	}
}
func TestFailureArchivePreservesReceiptsAndClearCutoff(t *testing.T) {
	now := time.Now().UTC()
	r := Repository{Jobs: []Job{{ID: "old", State: "failed", Updated: now.Add(-48 * time.Hour)}, {ID: "new", State: "failed", Updated: now.Add(time.Second)}, {ID: "active", State: "running", Updated: now.Add(-8 * 24 * time.Hour)}}, Receipts: []Receipt{{JobID: "old", Expires: now.Add(time.Hour)}}}
	r.Prune(now)
	if len(r.Jobs) != 3 || len(r.JobFailures) != 2 || len(r.Receipts) != 1 {
		t.Fatal("receipt or active work lost")
	}
	r.ClearTerminalJobs(false, true, now)
	r.Prune(now)
	if len(r.JobFailures) != 1 || r.JobFailures[0].ID != "new" || len(r.Receipts) != 1 || len(r.Jobs) != 2 {
		t.Fatal("clear resurrected failure or removed later work")
	}
}

func TestClearArchivedFailureCount(t *testing.T) {
	now := time.Now().UTC()
	r := Repository{JobFailures: []JobFailure{{ID: "archived", Time: now.Add(-time.Hour)}}}
	if got := r.ClearTerminalJobs(false, true, now); got != 1 || len(r.JobFailures) != 0 {
		t.Fatalf("clear count=%d", got)
	}
}
