package secretstore

import (
	"bytes"
	"context"
	"sync"
	"testing"
)

type memory struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func (m *memory) get(_ context.Context, id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.rows[id]
	if !ok {
		return nil, ErrMissing
	}
	return append([]byte(nil), b...), nil
}
func (m *memory) insert(_ context.Context, id string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[id]; ok {
		return ErrExists
	}
	m.rows[id] = append([]byte(nil), b...)
	return nil
}
func TestConcurrentInitializationAndHostReplacement(t *testing.T) {
	ctx := context.Background()
	m := &memory{rows: map[string][]byte{}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, e := open(ctx, m)
			if e != nil {
				t.Error(e)
				return
			}
			if _, e = s.Ensure(ctx, "api", []byte("existing-service-token")); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	fresh, e := open(ctx, m)
	if e != nil {
		t.Fatal(e)
	}
	got, e := fresh.Get(ctx, "api")
	if e != nil || string(got) != "existing-service-token" {
		t.Fatal("credential did not survive replacement", e)
	}
	if bytes.Contains(m.rows["value:api"], got) {
		t.Fatal("plaintext persisted")
	}
	if _, e = fresh.Ensure(ctx, "api", []byte("must-not-overwrite")); e != nil {
		t.Fatal(e)
	}
	got, _ = fresh.Get(ctx, "api")
	if string(got) != "existing-service-token" {
		t.Fatal("overwrote credential")
	}
	m.rows["value:other"] = m.rows["value:api"]
	if _, e = fresh.Get(ctx, "other"); e == nil {
		t.Fatal("AAD did not bind record ID")
	}
}
func TestCorruptKeyFailsClosed(t *testing.T) {
	m := &memory{rows: map[string][]byte{"encryption-key-v1": {1}}}
	if _, e := open(context.Background(), m); e == nil {
		t.Fatal("accepted corrupt key")
	}
}
