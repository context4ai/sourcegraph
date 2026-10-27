package service

import (
	"context"
	"errors"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
)

func managerCode(err error) string {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func TestUpdateManagers(t *testing.T) {
	db := &sourceStore{repos: map[string]control.Repository{"org/repo": {Name: "org/repo", CreatorID: "creator", Revision: 3, Enabled: true}}}
	s := &Service{DB: db, Root: t.TempDir()}
	ctx := identity.WithActor(context.Background(), "sso:creator")

	if _, err := s.UpdateManagers(ctx, "org/repo", ManagerChange{Add: "a", Remove: "b"}, 3); managerCode(err) != "INVALID_MANAGER_CHANGE" {
		t.Fatalf("both add and remove: %v", err)
	}
	if _, err := s.UpdateManagers(ctx, "org/repo", ManagerChange{Add: "second"}, 2); managerCode(err) != "REVISION_CONFLICT" {
		t.Fatalf("stale revision: %v", err)
	}
	r, err := s.UpdateManagers(ctx, "org/repo", ManagerChange{Add: "second"}, 3)
	if err != nil || r.Revision != 4 || !r.IsManager("creator") || !r.IsManager("second") {
		t.Fatalf("add: %#v %v", r.EffectiveManagers(), err)
	}
	if got := db.repos["org/repo"].Audit; len(got) != 1 || got[0].Action != "managers.add:second" || got[0].Actor != "sso:creator" {
		t.Fatalf("audit = %#v", got)
	}
	if r, err = s.UpdateManagers(ctx, "org/repo", ManagerChange{Add: "second"}, 4); err != nil || r.Revision != 4 {
		t.Fatalf("adding an existing manager is a no-op: %d %v", r.Revision, err)
	}
	// The creator may remove themselves; the stored list stays authoritative.
	if r, err = s.UpdateManagers(ctx, "org/repo", ManagerChange{Remove: "creator"}, 4); err != nil || r.IsManager("creator") || !r.IsManager("second") {
		t.Fatalf("remove self: %v %v", r.EffectiveManagers(), err)
	}
	if r, err = s.UpdateManagers(ctx, "org/repo", ManagerChange{Remove: "second"}, 5); err != nil || len(r.EffectiveManagers()) != 0 || !db.repos["org/repo"].ManagersSet {
		t.Fatalf("removing the last manager leaves site administrators only: %v %v", r.EffectiveManagers(), err)
	}
	full := control.Repository{Name: "org/full", Revision: 1, ManagersSet: true}
	for i := 0; i < control.MaxManagers; i++ {
		full.Managers = append(full.Managers, string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	db.repos["org/full"] = full
	if _, err = s.UpdateManagers(ctx, "org/full", ManagerChange{Add: "new"}, 1); managerCode(err) != "TOO_MANY_MANAGERS" {
		t.Fatalf("manager limit: %v", err)
	}
	db.repos["org/gone"] = control.Repository{Name: "org/gone", Revision: 1, Deleted: true}
	if _, err = s.UpdateManagers(ctx, "org/gone", ManagerChange{Add: "new"}, 1); managerCode(err) != "REPOSITORY_DELETED" {
		t.Fatalf("deleted repository: %v", err)
	}
}

func TestRegistrationRecordsTheCreatorAsManager(t *testing.T) {
	db := &sourceStore{repos: map[string]control.Repository{}}
	s := &Service{DB: db, Root: t.TempDir()}
	ctx := identity.WithActor(context.Background(), "sso:creator")
	r, err := s.RegisterWithProvider(ctx, "context4ai/agent-graph", control.Policy{Branches: []string{"main"}, Count: 10}, 60, "full", nil, nil, "github", "register")
	if err != nil || r.CreatorID != "creator" || !r.IsManager("creator") {
		t.Fatalf("registered = %q %v", r.CreatorID, err)
	}
}
