package httpserver

import (
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/service"
	"io"
	"net/http"
)

// Routing applies administrator session and CSRF checks before this handler.
func clearFinishedJobs(w http.ResponseWriter, r *http.Request, s *service.Service) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		writeError(w, "", 405, "METHOD_NOT_ALLOWED", "Use POST.")
		return
	}
	var q struct {
		Confirm string `json:"confirm"`
		Success bool   `json:"success"`
		Failed  bool   `json:"failed"`
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if e != nil || contract.DecodeObject(b, &q) != nil || q.Confirm != "CLEAR_FINISHED_JOBS" || (!q.Success && !q.Failed) || r.URL.RawQuery != "" {
		writeError(w, "", 400, "INVALID_CLEAR_JOBS", "Confirm clearing and select at least one completed state.")
		return
	}
	result, e := s.ClearFinishedJobs(r.Context(), q.Success, q.Failed)
	if e != nil {
		writeJSON(w, 503, map[string]any{"error": "CLEAR_JOBS_INCOMPLETE", "message": "Some records may already be cleared. Retry to finish.", "removed": result.Removed})
		return
	}
	writeJSON(w, 200, result)
}
