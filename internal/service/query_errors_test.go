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

func TestReadManyDiagnosticsPreservePartialSuccess(t *testing.T) {
	s, _, _, _ := readManyFixture(t, map[string]string{"ok.txt": "hello\n", "empty.txt": ""})
	store := &querylog.FileStore{Path: filepath.Join(t.TempDir(), "errors.json")}
	s.QueryLogs = querylog.New(store)
	result, err := s.ReadMany(context.Background(), "org/repo", "main", []contract.ReadItemRequest{{Path: "ok.txt", StartLine: 1, EndLine: 2}, {Path: "missing.txt", StartLine: 1, EndLine: 2}, {Path: "empty.txt", StartLine: 1, EndLine: 2}})
	if err != nil || len(result.Items) != 3 || result.Items[0].File == nil || result.Items[1].Error == nil || result.Items[2].File == nil {
		t.Fatalf("partial results changed: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.QueryLogs.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rows, err := store.Read(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("log: %+v %v", rows, err)
	}
	b := rows[0].Batch
	if b == nil || b.Total != 3 || b.Succeeded != 2 || b.Failed != 1 || b.ContentItems != 2 {
		t.Fatalf("incorrect batch counts: %+v", b)
	}
}
