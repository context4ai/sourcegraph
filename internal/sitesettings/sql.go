package sitesettings

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
)

type SQLStore struct{ DB *sqlstore.DB }

func (s *SQLStore) Ensure(ctx context.Context, r Record) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old Record
		e := t.Get("settings", r.Name, &old)
		if e == nil {
			return nil
		}
		if !errors.Is(e, sqlstore.Missing) {
			return e
		}
		return t.Put("settings", r.Name, r)
	})
}
func (s *SQLStore) Load(ctx context.Context) ([]Record, error) {
	rows, e := s.DB.List(ctx, "settings", "", 2)
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, b := range rows {
		var r Record
		if e = sqlstore.Decode(b, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *SQLStore) Replace(ctx context.Context, r Record, expected int64) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var old Record
		if e := t.Get("settings", r.Name, &old); e != nil {
			return e
		}
		if old.Revision != expected || r.Revision != expected+1 {
			return ErrConflict
		}
		return t.Put("settings", r.Name, r)
	})
}
