package control

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"go.mongodb.org/mongo-driver/bson"
	"math"
	"time"
)

type SQL struct{ DB *sqlstore.DB }

func (s *SQL) DocumentDB() *sqlstore.DB { return s.DB }
func (s *SQL) Get(ctx context.Context, name string) (Repository, error) {
	var r Repository
	e := s.DB.Get(ctx, "repositories", ID(name), &r)
	if errors.Is(e, sqlstore.Missing) {
		e = contract.Fail("REPOSITORY_NOT_FOUND", "Repository is not registered.", 404)
	}
	return r, e
}
func (s *SQL) List(ctx context.Context, after string, limit int) ([]Repository, error) {
	if limit < 1 || limit > 1000 {
		return nil, contract.Fail("INVALID_LIMIT", "Invalid catalog limit.", 400)
	}
	rows, e := s.DB.List(ctx, "repositories", after, limit)
	if e != nil {
		return nil, e
	}
	out := []Repository{}
	for _, b := range rows {
		var r Repository
		if e = sqlstore.Decode(b, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *SQL) Create(ctx context.Context, r Repository) error {
	if r.ID != ID(r.Name) || r.Revision != 1 {
		return errors.New("invalid repository identity")
	}
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old Repository
		e := t.Get("repositories", r.ID, &old)
		if e == nil {
			return contract.Fail("REPOSITORY_EXISTS", "Repository already exists.", 409)
		}
		if !errors.Is(e, sqlstore.Missing) {
			return e
		}
		return t.Put("repositories", r.ID, r)
	})
}
func (s *SQL) replace(ctx context.Context, r Repository, rev, lease int64) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old Repository
		if e := t.Get("repositories", r.ID, &old); e != nil {
			return e
		}
		if old.Revision != rev || old.LeaseRevision != lease {
			return contract.Fail("REVISION_CONFLICT", "Repository changed; reload and retry.", 412)
		}
		return t.Put("repositories", r.ID, r)
	})
}
func (s *SQL) Replace(ctx context.Context, r Repository, expected int64) error {
	if r.ID != ID(r.Name) || r.Revision != expected+1 {
		return errors.New("invalid repository revision")
	}
	return s.replace(ctx, r, expected, r.LeaseRevision)
}
func (s *SQL) ReplaceLease(ctx context.Context, r Repository, expected int64) error {
	if r.ID != ID(r.Name) || r.Revision != expected || r.LeaseRevision < 1 {
		return errors.New("invalid lease revision")
	}
	return s.replace(ctx, r, expected, r.LeaseRevision-1)
}
func (s *SQL) Delete(ctx context.Context, r Repository) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old Repository
		if e := t.Get("repositories", r.ID, &old); e != nil {
			return e
		}
		if old.Revision != r.Revision || !old.Deleted || old.Enabled {
			return contract.Fail("REVISION_CONFLICT", "Repository changed during cleanup.", 412)
		}
		return t.Delete("repositories", r.ID)
	})
}
func (s *SQL) NextGeneration(ctx context.Context) (uint32, error) {
	var v struct{ Value int64 }
	e := s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		e := t.Get("counters", "generation", &v)
		if e != nil && !errors.Is(e, sqlstore.Missing) {
			return e
		}
		if v.Value >= math.MaxUint32 {
			return errors.New("generation exhausted")
		}
		v.Value++
		return t.Put("counters", "generation", v)
	})
	return uint32(v.Value), e
}
func (s *SQL) PrepareTelemetry(context.Context) error { return nil }
func (s *SQL) WriteObservation(ctx context.Context, id string, payload any, at time.Time) error {
	b, e := bson.Marshal(payload)
	if e != nil {
		return e
	}
	var p bson.M
	if e = bson.Unmarshal(b, &p); e != nil {
		return e
	}
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		return t.Put("observations", id, Observation{InstanceID: id, Payload: p, ObservedAt: at})
	})
}
func (s *SQL) RecentObservations(ctx context.Context, since time.Time, limit int) ([]Observation, error) {
	rows, e := s.DB.List(ctx, "observations", "", 101)
	if e != nil {
		return nil, e
	}
	out := []Observation{}
	for _, b := range rows {
		var o Observation
		if e = sqlstore.Decode(b, &o); e != nil {
			return nil, e
		}
		if !o.ObservedAt.Before(since) {
			out = append(out, o)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
