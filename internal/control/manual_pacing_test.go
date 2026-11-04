package control

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
)

func pacedRepository() Repository {
	return Repository{Name: "org/repo", Enabled: true, Policy: Policy{Branches: []string{"main"}, Count: 10}}
}

func TestManualPreparationJoinsLiveWorkAndPacesNewWork(t *testing.T) {
	r := pacedRepository()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	first, err := r.QueueRequested("prepare", "main", "", "k1", "d1", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := r.QueueRequested("prepare", "main", "", "k2", "d1", "u", now.Add(5*time.Second))
	if err != nil || joined.ID != first.ID || len(r.Jobs) != 1 {
		t.Fatalf("identical live request must join job %s: %v %#v", first.ID, err, joined)
	}
	_, err = r.QueueRequested("sync", "", "", "k3", "d3", "u", now.Add(10*time.Second))
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != "RATE_LIMITED" || ce.HTTPStatus != 429 || ce.RetryAfterSeconds != 20 {
		t.Fatalf("new work inside the cooldown must be refused with retry-after: %v", err)
	}
	if _, err = r.QueueScoped("index", "abc", "", "k4", "d4", "scheduler", SourceAutomatic, now.Add(11*time.Second)); err != nil {
		t.Fatalf("automatic work is not paced: %v", err)
	}
	// A manual sync's follow-up head index keeps the manual source but is queued
	// by the worker, not requested by a caller.
	if _, err = r.QueueScoped("index", "def", "", "k6", "d6", "worker", SourceManual, now.Add(12*time.Second)); err != nil {
		t.Fatalf("worker follow-up work is not paced: %v", err)
	}
	if _, err = r.QueueRequested("sync", "", "", "k5", "d5", "u", now.Add(ManualCooldown)); err != nil {
		t.Fatalf("new work after the cooldown is accepted: %v", err)
	}
}

func TestOneManualRequestMayQueueSeveralGroups(t *testing.T) {
	r := pacedRepository()
	r.IndexMode = IndexModeMonorepo
	r.PathGroups = []PathGroup{{Name: "a", Paths: []string{"a"}}, {Name: "b", Paths: []string{"b"}}}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for _, g := range []string{"a", "b"} {
		if _, err := r.QueueRequested("sync", "", g, "k-"+g, "d", "u", now); err != nil {
			t.Fatalf("group %s: %v", g, err)
		}
	}
}

func TestReceiptsDropOldestInsteadOfBlocking(t *testing.T) {
	r := pacedRepository()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	first, err := r.QueueRequested("prepare", "main", "", "k0", "d", "u", now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxReceipts+20; i++ {
		joined, err := r.QueueRequested("prepare", "main", "", fmt.Sprintf("key-%d", i), "d", "u", now.Add(time.Second))
		if err != nil || joined.ID != first.ID {
			t.Fatalf("join %d: %v", i, err)
		}
	}
	if len(r.Receipts) != maxReceipts {
		t.Fatalf("receipts = %d, want %d", len(r.Receipts), maxReceipts)
	}
}
