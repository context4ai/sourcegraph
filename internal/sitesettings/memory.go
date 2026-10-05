package sitesettings

import (
	"context"
	"maps"
	"sync"
)

// MemoryStore supports isolated fixtures/tests. Production uses MongoStore.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{records: map[string]Record{}} }
func clone(r Record) Record        { r.Values = maps.Clone(r.Values); return r }
func (m *MemoryStore) Ensure(ctx context.Context, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records == nil {
		m.records = map[string]Record{}
	}
	if _, exists := m.records[r.Name]; !exists {
		m.records[r.Name] = clone(r)
	}
	return nil
}
func (m *MemoryStore) Load(ctx context.Context) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.records))
	for _, r := range m.records {
		out = append(out, clone(r))
	}
	return out, nil
}
func (m *MemoryStore) Replace(ctx context.Context, r Record, expected int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.records[r.Name]
	if !ok || old.Revision != expected {
		return ErrConflict
	}
	if r.Revision != expected+1 {
		return ErrUnavailable
	}
	m.records[r.Name] = clone(r)
	return nil
}
