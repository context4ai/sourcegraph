package service

import (
	"github.com/context4ai/sourcegraph/internal/control"
	"testing"
	"time"
)

func TestPrunedCheckPreservesSchedule(t *testing.T) {
	s := &Service{}
	now := time.Now().UTC()
	r := control.Repository{Enabled: true, PolicyRevision: 1, SyncIntervalMinutes: 3, SyncChecks: map[string]control.SyncCheck{"": {StartedAt: now, PolicyRevision: 1}}}
	if s.scopeSyncDue(r, "", now.Add(2*time.Minute)) {
		t.Fatal("pruning caused premature sync")
	}
	if !s.scopeSyncDue(r, "", now.Add(3*time.Minute)) {
		t.Fatal("sync not due")
	}
	r.SyncIntervalMinutes = 1
	if !s.scopeSyncDue(r, "", now.Add(time.Minute)) {
		t.Fatal("interval edit ignored")
	}
	r.Jobs = []control.Job{{Kind: "sync", State: "running", PolicyRevision: 1}}
	if s.scopeSyncDue(r, "", now.Add(time.Hour)) {
		t.Fatal("duplicate work while active")
	}
	r.Jobs = nil
	r.PolicyRevision = 2
	if !s.scopeSyncDue(r, "", now) {
		t.Fatal("old policy summary blocked new work")
	}
}
