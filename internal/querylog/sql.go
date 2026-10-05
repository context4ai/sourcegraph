package querylog

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"time"
)

type SQLStore struct{ DB *sqlstore.DB }

func (s SQLStore) Read(ctx context.Context) ([]Entry, error) {
	var v fileState
	e := s.DB.Get(ctx, "querylog", "recent", &v)
	if errors.Is(e, sqlstore.Missing) {
		e = nil
	}
	return v.Entries, e
}
func (s SQLStore) Append(ctx context.Context, entries []Entry) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		var v fileState
		e := t.Get("querylog", "recent", &v)
		if e != nil && !errors.Is(e, sqlstore.Missing) {
			return e
		}
		v.Entries = merge(v.Entries, afterClear(entries, v.ClearedBefore))
		return t.Put("querylog", "recent", v)
	})
}
func (s SQLStore) Clear(ctx context.Context) error {
	return s.DB.Write(ctx, func(t *sqlstore.Tx) error {
		return t.Put("querylog", "recent", fileState{Entries: []Entry{}, ClearedBefore: time.Now().UTC()})
	})
}
