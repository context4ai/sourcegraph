package sso

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"slices"
	"strings"
	"time"
)

type SQLStore struct{ DB *sqlstore.DB }

func sqlAuth(e error) error {
	if errors.Is(e, sqlstore.Missing) {
		return ErrNotFound
	}
	return e
}
func (s *SQLStore) EnsureIndexes(context.Context) error { return nil }
func (s *SQLStore) put(ctx context.Context, ns, id string, v any) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error { return t.Put(ns, id, v) })
}
func (s *SQLStore) PutFlow(ctx context.Context, f Flow) error { return s.put(ctx, "flows", f.ID, f) }
func (s *SQLStore) ConsumeFlow(ctx context.Context, id, binding string, now time.Time) (Flow, error) {
	var f Flow
	e := s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		if e := t.Get("flows", id, &f); e != nil {
			return sqlAuth(e)
		}
		if f.Binding != binding || !f.Expires.After(now) {
			return ErrNotFound
		}
		return t.Delete("flows", id)
	})
	return f, e
}
func (s *SQLStore) UpsertUser(ctx context.Context, u User) (User, error) {
	e := s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old User
		e := t.Get("users", u.ID, &old)
		if e == nil {
			u.Created = old.Created
			u.Enabled = old.Enabled
		} else if !errors.Is(e, sqlstore.Missing) {
			return e
		}
		return t.Put("users", u.ID, u)
	})
	return u, e
}
func (s *SQLStore) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	e := s.DB.Get(ctx, "users", id, &u)
	return u, sqlAuth(e)
}
func (s *SQLStore) PutSession(ctx context.Context, v Session) error {
	return s.put(ctx, "sessions", v.ID, v)
}
func (s *SQLStore) GetSession(ctx context.Context, id string) (Session, error) {
	var v Session
	e := s.DB.Get(ctx, "sessions", id, &v)
	return v, sqlAuth(e)
}
func (s *SQLStore) DeleteSession(ctx context.Context, id string) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error { return t.Delete("sessions", id) })
}
func (s *SQLStore) GetAccess(ctx context.Context) (AccessControl, error) {
	var a AccessControl
	e := s.DB.Get(ctx, "access", accessID, &a)
	return a, sqlAuth(e)
}
func (s *SQLStore) CreateAccess(ctx context.Context, a AccessControl) (bool, error) {
	ok := false
	e := s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old AccessControl
		e := t.Get("access", accessID, &old)
		if e == nil {
			return nil
		}
		if !errors.Is(e, sqlstore.Missing) {
			return e
		}
		ok = true
		return t.Put("access", accessID, a)
	})
	return ok, e
}
func (s *SQLStore) ReplaceAccess(ctx context.Context, rev int64, a AccessControl) (bool, error) {
	ok := false
	e := s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old AccessControl
		if e := t.Get("access", accessID, &old); e != nil {
			return e
		}
		if old.Revision != rev {
			return nil
		}
		ok = true
		return t.Put("access", accessID, a)
	})
	return ok, e
}
func (s *SQLStore) ListUsers(ctx context.Context, q UserQuery, admins []string) ([]User, error) {
	out := []User{}
	after := q.After
	for len(out) < q.Limit {
		rows, e := s.DB.List(ctx, "users", after, 256)
		if e != nil {
			return nil, e
		}
		if len(rows) == 0 {
			break
		}
		for _, b := range rows {
			var u User
			if e = sqlstore.Decode(b, &u); e != nil {
				return nil, e
			}
			after = u.ID
			role := roleUser
			if slices.Contains(admins, u.ID) {
				role = roleAdmin
			}
			if (q.Role == "" || q.Role == role) && (q.Department == "" || q.Department == u.Department) && (q.Q == "" || strings.Contains(strings.ToLower(u.ID+" "+u.Name+" "+u.Email), strings.ToLower(q.Q))) {
				out = append(out, u)
				if len(out) == q.Limit {
					break
				}
			}
		}
	}
	return out, nil
}
func (s *SQLStore) CreateAPIKey(ctx context.Context, k APIKey) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old APIKey
		e := t.Get("key_hashes", k.Hash, &old)
		if e == nil {
			return errors.New("duplicate API key")
		}
		if !errors.Is(e, sqlstore.Missing) {
			return e
		}
		if e = t.Put("key_hashes", k.Hash, k); e != nil {
			return e
		}
		return t.Put("keys", k.ID, k)
	})
}
func (s *SQLStore) FindAPIKey(ctx context.Context, hash string) (APIKey, error) {
	var k APIKey
	e := s.DB.Get(ctx, "key_hashes", hash, &k)
	return k, sqlAuth(e)
}
func (s *SQLStore) ListAPIKeys(ctx context.Context, owner, after string, limit int) ([]APIKey, error) {
	out := []APIKey{}
	for len(out) < limit {
		rows, e := s.DB.List(ctx, "keys", after, 256)
		if e != nil {
			return nil, e
		}
		if len(rows) == 0 {
			break
		}
		for _, b := range rows {
			var k APIKey
			if e = sqlstore.Decode(b, &k); e != nil {
				return nil, e
			}
			after = k.ID
			if k.UserID == owner {
				out = append(out, k)
				if len(out) == limit {
					break
				}
			}
		}
	}
	return out, nil
}
func (s *SQLStore) DeleteAPIKey(ctx context.Context, owner, id string) (bool, error) {
	ok := false
	e := s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var k APIKey
		e := t.Get("keys", id, &k)
		if errors.Is(e, sqlstore.Missing) {
			return nil
		}
		if e != nil {
			return e
		}
		if k.UserID != owner {
			return nil
		}
		ok = true
		if e = t.Delete("key_hashes", k.Hash); e != nil {
			return e
		}
		return t.Delete("keys", id)
	})
	return ok, e
}
func (s *SQLStore) ResetIdentity(ctx context.Context) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		for _, ns := range []string{"users", "sessions", "flows", "access", "keys", "key_hashes"} {
			if e := t.Clear(ns); e != nil {
				return e
			}
		}
		return nil
	})
}

// AdmitUser commits the first administrator and profile together. No rejected
// identity creates a user record, session or permanent administrator claim.
func (s *SQLStore) AdmitUser(ctx context.Context, u User, allow bool) (User, bool, error) {
	first := false
	e := s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var a AccessControl
		if e := t.Get("access", accessID, &a); e != nil {
			return e
		}
		if !a.valid() {
			return errors.New("invalid access state")
		}
		var old User
		e := t.Get("users", u.ID, &old)
		if e == nil {
			u.Created = old.Created
			u.Enabled = old.Enabled
		} else if !errors.Is(e, sqlstore.Missing) {
			return e
		}
		if !u.Enabled {
			return ErrRegistrationClosed
		}
		if a.BootstrapPending {
			a.BootstrapPending = false
			a.AdminIDs = []string{u.ID}
			a.FounderID = u.ID
			a.Revision++
			a.Updated = time.Now().UTC()
			a.Events = []RoleEvent{{Actor: u.ID, Target: u.ID, Role: roleAdmin, At: a.Updated}}
			if e = t.Put("access", accessID, a); e != nil {
				return e
			}
			first = true
		} else if a.role(u.ID) != roleAdmin && !allow {
			return ErrRegistrationClosed
		}
		return t.Put("users", u.ID, u)
	})
	return u, first, e
}
