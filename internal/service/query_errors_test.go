package service

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/querylog"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryFailuresShareLog(t *testing.T) {
	s, _, _, _ := readManyFixture(t, map[string]string{"ok.txt": "hello\n"})
	store := &querylog.FileStore{Path: filepath.Join(t.TempDir(), "errors.json")}
	s.QueryLogs = querylog.New(store)
	ctx := querylog.WithRequest(context.Background(), "request-test", "mcp")
	_, _ = s.Search(ctx, "invalid", contract.SearchRequest{Revision: "main", Pattern: "x"})
	_, _ = s.Read(ctx, "org/repo", contract.ReadRequest{Revision: "main", Path: "missing.txt", StartLine: 1, EndLine: 10})
	_, _ = s.ReadMany(ctx, "org/repo", "main", []contract.ReadItemRequest{{Path: "missing.txt", StartLine: 1, EndLine: 10}})
	rows, err := store.Read(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatal("request wrote storage synchronously")
	}
	bg, cancel := context.WithCancel(context.Background())
	cancel()
	s.QueryLogs.Run(bg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rows, err = store.Read(context.Background())
	if err != nil || len(rows) != 3 {
		t.Fatalf("got %d logs: %v", len(rows), err)
	}
	for _, e := range rows {
		if e.RequestID != "request-test" || e.Time.Before(time.Now().Add(-time.Minute)) {
			t.Fatal(e)
		}
	}
}
