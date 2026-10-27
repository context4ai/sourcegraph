package sitesettings

import (
	"context"
	"testing"
	"time"
)

func TestUserRepositoriesSettingDefaultsOff(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	// A record saved before the setting existed still loads, with the setting off.
	now := time.Now().UTC()
	if err := store.Ensure(ctx, Record{Name: AccessName, Revision: 3, Values: map[string]any{"public_read": true}, UpdatedAt: now, UpdatedBy: "sso:admin"}); err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if !s.PublicRead() || s.AllowUserRepositories() {
		t.Fatalf("legacy access = %+v", s.Snapshot().Access.Values)
	}
	if _, err = s.Update(ctx, AccessName, 3, []byte(`{"public_read":true,"allow_user_repositories":true}`), "sso:admin"); err != nil {
		t.Fatal(err)
	}
	if !s.AllowUserRepositories() {
		t.Fatal("setting was not saved")
	}
	// Older clients that send only public_read turn the setting off rather than failing.
	if _, err = s.Update(ctx, AccessName, 4, []byte(`{"public_read":false}`), "sso:admin"); err != nil || s.AllowUserRepositories() || s.PublicRead() {
		t.Fatalf("public_read only: %v %+v", err, s.Snapshot().Access.Values)
	}
	if _, err = s.Update(ctx, AccessName, 5, []byte(`{"public_read":true,"allow_user_repositories":"yes"}`), "sso:admin"); err == nil {
		t.Fatal("non-boolean setting accepted")
	}
	var nilService *Service
	if nilService.AllowUserRepositories() {
		t.Fatal("no settings service means the setting is off")
	}
	fresh, err := New(ctx, NewMemoryStore(), true)
	if err != nil || fresh.AllowUserRepositories() {
		t.Fatalf("new deployments start with the setting off: %v", err)
	}
}
