package httpserver

import (
	"context"
	"encoding/json"
	"github.com/context4ai/sourcegraph/internal/querylog"
	"github.com/context4ai/sourcegraph/internal/service"
	"net/http"
	"time"
)

// Mounted only behind the site-settings administrator session guard.
func queryErrors(w http.ResponseWriter, r *http.Request, s *service.Service) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" && r.Method != "DELETE" {
		w.Header().Set("Allow", "GET, DELETE")
		writeError(w, "", 405, "METHOD_NOT_ALLOWED", "Use GET or DELETE.")
		return
	}
	for k, v := range r.URL.Query() {
		if (k != "after" && k != "id") || len(v) != 1 || len(v[0]) > 128 {
			writeError(w, "", 400, "INVALID_REQUEST", "Use id or after.")
			return
		}
	}
	if s.QueryLogs == nil {
		writeError(w, "", 503, "ERROR_LOGS_UNAVAILABLE", "Query logs are unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if r.Method == "DELETE" {
		if r.URL.RawQuery != "" {
			writeError(w, "", 400, "INVALID_REQUEST", "Clear does not accept query parameters.")
			return
		}
		if err := s.QueryLogs.Store.Clear(ctx); err != nil {
			writeError(w, "", 503, "ERROR_LOGS_UNAVAILABLE", "Query logs could not be cleared.")
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"cleared": true})
		return
	}
	rows, err := s.QueryLogs.Store.Read(ctx)
	if err != nil {
		writeError(w, "", 503, "ERROR_LOGS_UNAVAILABLE", "Query logs could not be loaded.")
		return
	}
	if id := r.URL.Query().Get("id"); id != "" {
		for _, entry := range rows {
			if entry.ID == id {
				json.NewEncoder(w).Encode(entry)
				return
			}
		}
		writeError(w, "", 404, "ERROR_LOG_NOT_FOUND", "Log expired or does not exist.")
		return
	}
	start := 0
	if after := r.URL.Query().Get("after"); after != "" {
		found := false
		for i, entry := range rows {
			if entry.ID == after {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			writeError(w, "", 409, "ERROR_LOG_LIST_CHANGED", "Log retention changed; refresh the first page.")
			return
		}
	}
	end := min(start+50, len(rows))
	items := append([]querylog.Entry{}, rows[start:end]...)
	for i := range items {
		items[i].Context = ""
		items[i].SuggestedQuery = ""
		items[i].QueryFragment = ""
	}
	next := ""
	if end < len(rows) && end > start {
		next = rows[end-1].ID
	}
	json.NewEncoder(w).Encode(map[string]any{"items": items, "next_cursor": next, "retention_limit": querylog.Limit, "dropped_current_instance": s.QueryLogs.Dropped.Load(), "write_failures_current_instance": s.QueryLogs.WriteFailures.Load()})
}
