package sitesettings

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestGitTimeoutLegacyPersistenceValidationAndCAS(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	old := Record{Name: RepositoryDefaultsName, Revision: 3, Values: map[string]any{"retention_mode": "days", "retention_days": 30, "retention_count": 20}, UpdatedAt: time.Now(), UpdatedBy: "admin"}
	if err := store.Ensure(ctx, old); err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, store, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.GitTimeoutMinutes() != 15 {
		t.Fatal("legacy default must be 15")
	}
	revision := int64(3)
	for _, n := range []int{15, 30, 45, 60} {
		body := []byte(fmt.Sprintf(`{"retention_mode":"days","retention_days":30,"retention_count":20,"git_timeout_minutes":%d}`, n))
		if _, err = s.Update(ctx, RepositoryDefaultsName, revision, body, "admin"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Update(ctx, RepositoryDefaultsName, revision, body, "admin"); err == nil {
			t.Fatal("stale revision accepted")
		}
		revision++
		restarted, e := New(ctx, store, true)
		if e != nil || restarted.GitTimeoutMinutes() != n {
			t.Fatalf("restart %d: %v", n, e)
		}
	}
	// Older clients can edit retention without resetting the timeout.
	if _, err = s.Update(ctx, RepositoryDefaultsName, revision, []byte(`{"retention_mode":"days","retention_days":20,"retention_count":20}`), "admin"); err != nil {
		t.Fatal(err)
	}
	revision++
	if s.GitTimeoutMinutes() != 60 {
		t.Fatal("omitted timeout reset existing value")
	}
	for _, value := range []string{"0", "-1", "16", "61", "90", "15.5", "null", `"30"`} {
		body := []byte(fmt.Sprintf(`{"retention_mode":"days","retention_days":30,"retention_count":20,"git_timeout_minutes":%s}`, value))
		if _, err = s.Update(ctx, RepositoryDefaultsName, revision, body, "admin"); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
