package querylog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/context4ai/sourcegraph/internal/contract"
	"go.mongodb.org/mongo-driver/bson"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRetentionAndRetry(t *testing.T) {
	s := &FileStore{Path: filepath.Join(t.TempDir(), "logs.json")}
	entries := make([]Entry, 1200)
	for i := range entries {
		entries[i] = Entry{ID: fmt.Sprint(i), Time: time.Unix(int64(i), 0)}
	}
	for i := 0; i < 2; i++ {
		if err := s.Append(context.Background(), entries); err != nil {
			t.Fatal(err)
		}
	}
	reopened := &FileStore{Path: s.Path}
	rows, err := reopened.Read(context.Background())
	if err != nil || len(rows) != 1000 || rows[0].ID != "1199" || rows[999].ID != "200" {
		t.Fatalf("retention: %d %v", len(rows), err)
	}
}
func TestNonblockingAndSanitized(t *testing.T) {
	l := New(nil)
	ctx := WithRequest(context.Background(), "request-1", "mcp")
	input := map[string]string{"q": "Bearer sensitive-value code_pat_private sgk_private token=private " + strings.Repeat("直播", 3000)}
	for i := 0; i < 300; i++ {
		l.Record(ctx, "instance", "sso:user", "org/repo", "search", time.Now(), input, contract.Fail("INVALID_QUERY", "password=secret-value", 400))
	}
	if l.Dropped.Load() != 44 {
		t.Fatal(l.Dropped.Load())
	}
	e := <-l.queue
	if e.RequestID != "request-1" || e.Transport != "mcp" || !e.ContextTruncated || !utf8.ValidString(e.Context) {
		t.Fatalf("bad context: %+v", e)
	}
	for _, secret := range []string{"sensitive-value", "code_pat_private", "sgk_private", "secret-value"} {
		if strings.Contains(e.Context+e.Message, secret) {
			t.Fatal("secret recorded")
		}
	}
	b, err := bson.Marshal(e)
	if err != nil || len(b) > 8192 {
		t.Fatalf("entry exceeds document budget: %d %v", len(b), err)
	}
	l.Record(ctx, "", "", "", "read", time.Now(), nil, nil)
	if len(l.queue) != 255 {
		t.Fatal("success recorded")
	}
}

type failingStore struct{ calls int }

func (s *failingStore) Append(context.Context, []Entry) error {
	s.calls++
	return errors.New("secret raw database error")
}
func (s *failingStore) Read(context.Context) ([]Entry, error) { return nil, nil }
func TestShutdownFlushAndStorageFailure(t *testing.T) {
	store := &failingStore{}
	l := New(store)
	l.Record(context.Background(), "", "", "repo", "read", time.Now(), nil, context.DeadlineExceeded)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if store.calls != 3 || l.Dropped.Load() != 1 || l.WriteFailures.Load() != 3 {
		t.Fatalf("retry/drop: %d %d", store.calls, l.Dropped.Load())
	}
}
func TestConcurrentFileWrites(t *testing.T) {
	s := &FileStore{Path: filepath.Join(t.TempDir(), "logs.json")}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Append(context.Background(), []Entry{{ID: fmt.Sprint(i), Time: time.Now()}}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	rows, err := s.Read(context.Background())
	if err != nil || len(rows) != 20 {
		t.Fatalf("%d %v", len(rows), err)
	}
}

func TestQueryRecoveryHintsPersistSafely(t *testing.T) {
	for _, tc := range []struct {
		name, suggestion string
		omitted          bool
	}{
		{"complete", "file:knowledge/ (content:审核 or content:review)", false},
		{"sensitive", "content:code_pat_private", true},
		{"oversized", strings.Repeat("界", 800), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &FileStore{Path: filepath.Join(t.TempDir(), "logs.json")}
			l := New(s)
			problem := &contract.Error{Code: "INVALID_QUERY", Message: "invalid group", QueryFragment: "content:(审核 or review)", SuggestedQuery: tc.suggestion}
			l.Record(context.Background(), "instance", "actor", "repo", "search", time.Now(), map[string]string{"q": "original"}, fmt.Errorf("wrapped: %w", problem))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			l.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
			rows, err := s.Read(context.Background())
			if err != nil || len(rows) != 1 {
				t.Fatalf("persist: %v %+v", err, rows)
			}
			e := rows[0]
			if e.QueryFragment != problem.QueryFragment || e.SuggestionOmitted != tc.omitted {
				t.Fatalf("hints: %+v", e)
			}
			if tc.omitted && e.SuggestedQuery != "" {
				t.Fatal("unsafe or incomplete executable suggestion")
			}
			if !tc.omitted && e.SuggestedQuery != tc.suggestion {
				t.Fatal("suggestion changed")
			}
		})
	}
}

func (s *failingStore) Clear(context.Context) error { return errors.New("unavailable") }

func TestClearPersistsAndRejectsDelayedEntries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.json")
	old := Entry{ID: "old", Time: time.Now().Add(-time.Hour)}
	// Migrate the legacy local array format while retaining the clear marker.
	raw, _ := json.Marshal([]Entry{old})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{Path: path}
	if err := store.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Read(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("%v %v", rows, err)
	}
	reopened := &FileStore{Path: path}
	newEntry := Entry{ID: "new", Time: time.Now().Add(time.Second)}
	if err := reopened.Append(ctx, []Entry{old, newEntry}); err != nil {
		t.Fatal(err)
	}
	rows, err = reopened.Read(ctx)
	if err != nil || len(rows) != 1 || rows[0].ID != "new" {
		t.Fatalf("%v %v", rows, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := reopened.Clear(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	rows, _ = reopened.Read(ctx)
	if len(rows) != 1 {
		t.Fatal("canceled clear removed records")
	}
}
