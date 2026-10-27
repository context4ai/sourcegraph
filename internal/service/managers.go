package service

import (
	"context"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
)

// ManagerChange adds or removes one repository manager. Callers resolve and
// validate the user before calling; authorization happens at the HTTP gate.
type ManagerChange struct {
	Add    string `json:"add,omitempty"`
	Remove string `json:"remove,omitempty"`
}

// UpdateManagers edits the per-repository manager list under the repository's
// revision. Removing the last manager is allowed: site administrators keep
// management, and a manager may remove themselves.
func (s *Service) UpdateManagers(ctx context.Context, name string, change ManagerChange, revision int64) (control.Repository, error) {
	if (change.Add == "") == (change.Remove == "") {
		return control.Repository{}, contract.Fail("INVALID_MANAGER_CHANGE", "Specify exactly one user to add or remove.", 400)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.DB.Get(ctx, name)
	if e != nil {
		return r, e
	}
	if r.Deleted {
		return r, contract.Fail("REPOSITORY_DELETED", "Deleted repositories cannot change managers.", 409)
	}
	if r.Revision != revision {
		return r, contract.Fail("REVISION_CONFLICT", "Repository changed; reload and try again.", 412)
	}
	managers := r.EffectiveManagers()
	action := ""
	if change.Add != "" {
		if r.IsManager(change.Add) {
			return projectRepository(s.markStorage(r)), nil
		}
		if len(managers) >= control.MaxManagers {
			return r, contract.Fail("TOO_MANY_MANAGERS", "A repository can have at most 50 managers.", 409)
		}
		managers = append(managers, change.Add)
		action = "managers.add:" + change.Add
	} else {
		kept := managers[:0]
		for _, id := range managers {
			if id != change.Remove {
				kept = append(kept, id)
			}
		}
		if len(kept) == len(managers) {
			return projectRepository(s.markStorage(r)), nil
		}
		managers = kept
		action = "managers.remove:" + change.Remove
	}
	r.Managers = managers
	r.ManagersSet = true
	r.Revision++
	r.Updated = time.Now().UTC()
	r.Audit = append(r.Audit, control.Audit{Actor: identity.Actor(ctx), Action: action, At: r.Updated})
	if len(r.Audit) > 256 {
		r.Audit = r.Audit[len(r.Audit)-256:]
	}
	e = s.DB.Replace(ctx, r, revision)
	return projectRepository(s.markStorage(r)), e
}
