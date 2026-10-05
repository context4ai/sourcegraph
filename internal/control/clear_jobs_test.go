package control

import (
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"testing"
	"time"
)

func TestClearTerminalJobs(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		success, failed bool
		want            int
	}{{true, true, 2}, {true, false, 1}, {false, true, 1}, {false, false, 0}} {
		r := Repository{Enabled: true, Jobs: []Job{{ID: "s", State: "succeeded", Updated: now}, {ID: "f", State: "failed", Updated: now}, {ID: "r", State: "running", Updated: now}, {ID: "q", State: "queued", Updated: now}, {ID: "new", State: "succeeded", Updated: now.Add(time.Second)}, {ID: "c", State: "canceled", Updated: now}}, Receipts: []Receipt{{Key: "key", Digest: "digest", JobID: "s", Expires: now.Add(time.Hour)}}, Versions: []Version{{Commit: "sha", Generation: 1}}}
		if n := r.ClearTerminalJobs(tc.success, tc.failed, now); n != tc.want {
			t.Fatalf("removed %d want %d", n, tc.want)
		}
		if len(r.Jobs) != 6-tc.want || len(r.Receipts) != 1 || r.Versions[0].Generation != 1 {
			t.Fatal("unrelated state changed")
		}
	}
}
func TestExpiredJobRecoveryReason(t *testing.T) {
	now := time.Now().UTC()
	r := Repository{Enabled: true, Jobs: []Job{{ID: "j", State: "running", Attempt: 3, LeaseUntil: now.Add(-time.Second)}}}
	if _, ok := r.Claim("worker", now); ok {
		t.Fatal("exhausted task claimed")
	}
	if r.Jobs[0].Error != "RECOVERY_EXHAUSTED" || r.Jobs[0].RecoveryReason != "LEASE_EXPIRED" {
		t.Fatal(r.Jobs[0])
	}
}

func TestClearedReceiptDoesNotQueueDuplicate(t *testing.T) {
	now := time.Now().UTC()
	r := Repository{Enabled: true, IndexMode: "full", Receipts: []Receipt{{Key: "request-key", Digest: "digest", JobID: "removed", Expires: now.Add(time.Hour)}}}
	_, err := r.QueueSource("sync", "", "request-key", "digest", "user", SourceManual, now)
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != "JOB_HISTORY_CLEARED" || len(r.Jobs) != 0 {
		t.Fatalf("unexpected replay: %v", err)
	}
}
