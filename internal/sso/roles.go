package sso

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
)

const roleUser = "user"
const roleAdmin = "admin"
const accessID = "website"
const maxAdmins = 1024

type RoleEvent struct {
	Actor  string    `bson:"actor"`
	Target string    `bson:"target"`
	Role   string    `bson:"role"`
	At     time.Time `bson:"at"`
}

// AccessControl is the only role authority. Roles are never copied into sessions
// or trusted from a per-user cached field. Every change uses one document CAS.
type AccessControl struct {
	FounderID        string      `bson:"founder_id,omitempty"`
	ID               string      `bson:"_id"`
	Revision         int64       `bson:"revision"`
	BootstrapPending bool        `bson:"bootstrap_pending"`
	AdminIDs         []string    `bson:"admin_ids"`
	Updated          time.Time   `bson:"updated_at"`
	Events           []RoleEvent `bson:"events"`
}

type UserQuery struct {
	Q, Department, Role, After string
	Limit                      int
}
type AccessStore interface {
	GetAccess(context.Context) (AccessControl, error)
	CreateAccess(context.Context, AccessControl) (bool, error)
	ReplaceAccess(context.Context, int64, AccessControl) (bool, error)
	ListUsers(context.Context, UserQuery, []string) ([]User, error)
}

func (a AccessControl) valid() bool {
	if a.ID != accessID || a.Revision < 1 || len(a.AdminIDs) > maxAdmins || (len(a.AdminIDs) == 0) != a.BootstrapPending {
		return false
	}
	seen := map[string]bool{}
	for _, id := range a.AdminIDs {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func (a AccessControl) role(id string) string {
	if slices.Contains(a.AdminIDs, id) {
		return roleAdmin
	}
	return roleUser
}
func readAccess(ctx context.Context, s AccessStore) (AccessControl, error) {
	a, e := s.GetAccess(ctx)
	if e == nil && !a.valid() {
		return a, errors.New("invalid access-control state")
	}
	return a, e
}

// initializeAccess runs before accepting logins. Explicit admin_subjects seed
// the initial authority; otherwise the first successful login claims it. Old
// profiles do not count as assigned roles. A concurrent initializer cannot
// replace the winner.
func initializeAccess(ctx context.Context, s Store, c Config, issuer string) error {
	if _, e := readAccess(ctx, s); e == nil {
		return nil
	} else if !errors.Is(e, ErrNotFound) {
		return e
	}
	ids := []string{}
	for _, subject := range c.AdminSubjects {
		id := digest(issuer + "\x00" + subject)
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxAdmins {
		return errors.New("too many bootstrap administrators")
	}
	now := time.Now().UTC()
	a := AccessControl{ID: accessID, Revision: 1, BootstrapPending: len(ids) == 0, AdminIDs: ids, Updated: now}
	if len(ids) > 0 {
		for _, id := range ids {
			a.Events = append(a.Events, RoleEvent{Actor: "configured-migration", Target: id, Role: roleAdmin, At: now})
		}
		if len(a.Events) > 256 {
			a.Events = a.Events[len(a.Events)-256:]
		}
	}
	if _, e := s.CreateAccess(ctx, a); e != nil {
		return e
	}
	_, e := readAccess(ctx, s)
	return e
}

func bootstrapUser(ctx context.Context, s Store, id string) (string, error) {
	role, _, err := bootstrapLogin(ctx, s, id)
	return role, err
}

func bootstrapLogin(ctx context.Context, s Store, id string) (string, bool, error) {
	for i := 0; i < 16; i++ {
		a, e := readAccess(ctx, s)
		if e != nil {
			return "", false, e
		}
		if !a.BootstrapPending {
			return a.role(id), false, nil
		}
		a.BootstrapPending = false
		a.AdminIDs = []string{id}
		a.FounderID = id
		a.Updated = time.Now().UTC()
		a.Events = []RoleEvent{{Actor: id, Target: id, Role: roleAdmin, At: a.Updated}}
		revision := a.Revision
		a.Revision++
		ok, e := s.ReplaceAccess(ctx, revision, a)
		if e != nil {
			return "", false, e
		}
		if ok {
			return roleAdmin, true, nil
		}
	}
	return "", false, contract.Fail("ROLE_CONFLICT", "Role initialization changed concurrently; retry login.", 409)
}

func changeRole(ctx context.Context, s Store, actor, target, expected, desired string) (AccessControl, error) {
	if (expected != roleUser && expected != roleAdmin) || (desired != roleUser && desired != roleAdmin) {
		return AccessControl{}, contract.Fail("INVALID_USER_INPUT", "Unknown role.", 400)
	}
	for i := 0; i < 16; i++ {
		a, e := readAccess(ctx, s)
		if e != nil {
			return a, e
		}
		if a.role(actor) != roleAdmin {
			return a, contract.Fail("ACCESS_DENIED", "Administrator permission is required.", 403)
		}
		if a.role(target) != expected {
			return a, contract.Fail("ROLE_CONFLICT", "The user's role changed; reload before trying again.", 409)
		}
		if expected == desired {
			return a, nil
		}
		if desired == roleUser {
			users, err := s.ListUsers(ctx, UserQuery{Role: roleAdmin, Limit: maxAdmins}, a.AdminIDs)
			if err != nil {
				return a, err
			}
			otherEnabled := false
			for _, u := range users {
				if u.ID != target && u.Enabled {
					otherEnabled = true
					break
				}
			}
			if !otherEnabled {
				return a, contract.Fail("LAST_ADMIN_REQUIRED", "At least one enabled administrator must remain.", 409)
			}
			if actor == target {
				return a, contract.Fail("SELF_DEMOTION_FORBIDDEN", "Administrators cannot demote themselves.", 403)
			}
			a.AdminIDs = slices.DeleteFunc(slices.Clone(a.AdminIDs), func(id string) bool { return id == target })
		} else {
			if len(a.AdminIDs) >= maxAdmins {
				return a, contract.Fail("ADMIN_LIMIT_REACHED", "Administrator capacity has been reached.", 409)
			}
			a.AdminIDs = append(slices.Clone(a.AdminIDs), target)
		}
		a.Updated = time.Now().UTC()
		a.Events = append(slices.Clone(a.Events), RoleEvent{Actor: actor, Target: target, Role: desired, At: a.Updated})
		if len(a.Events) > 256 {
			a.Events = a.Events[len(a.Events)-256:]
		}
		revision := a.Revision
		a.Revision++
		ok, e := s.ReplaceAccess(ctx, revision, a)
		if e != nil {
			return a, e
		}
		if ok {
			return a, nil
		}
	}
	return AccessControl{}, contract.Fail("ROLE_CONFLICT", "Roles changed concurrently; reload before trying again.", 409)
}
