package codecredentials

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
)

type SQLStore struct{ DB *sqlstore.DB }

func (s SQLStore) Get(ctx context.Context, id string) (Record, error) {
	var r Record
	e := s.DB.Get(ctx, "credentials", id, &r)
	if errors.Is(e, sqlstore.Missing) {
		e = missing
	}
	return r, e
}
func (s SQLStore) Save(ctx context.Context, r Record, expected int64) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old Record
		e := t.Get("credentials", r.ID, &old)
		if e != nil && !errors.Is(e, sqlstore.Missing) {
			return e
		}
		if old.Revision != expected || r.Revision != expected+1 {
			return conflict
		}
		return t.Put("credentials", r.ID, r)
	})
}
func (s SQLStore) Clear(ctx context.Context) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error { return t.Clear("credentials") })
}
