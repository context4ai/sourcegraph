package secretstore

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
)

type sqlBackend struct{ db *sqlstore.DB }

func NewSQL(ctx context.Context, db *sqlstore.DB, seed ...KeySeed) (*Store, error) {
	return open(ctx, sqlBackend{db}, seed...)
}
func (s sqlBackend) get(ctx context.Context, id string) ([]byte, error) {
	var r record
	e := s.db.Get(ctx, "secrets", id, &r)
	if errors.Is(e, sqlstore.Missing) {
		return nil, ErrMissing
	}
	if e != nil {
		return nil, unavailable
	}
	return r.Data, nil
}
func (s sqlBackend) insert(ctx context.Context, id string, data []byte) error {
	e := s.db.Write(ctx, func(t *sqlstore.Tx) error {
		var r record
		e := t.Get("secrets", id, &r)
		if e == nil {
			return ErrExists
		}
		if !errors.Is(e, sqlstore.Missing) {
			return e
		}
		return t.Put("secrets", id, record{ID: id, Data: data})
	})
	if errors.Is(e, ErrExists) {
		return ErrExists
	}
	if e != nil {
		return unavailable
	}
	return nil
}
