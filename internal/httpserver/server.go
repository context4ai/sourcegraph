package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/context4ai/sourcegraph/internal/config"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/webui"
)

const Prefix = "/sourcegraph"

// New exposes only implemented foundation endpoints. No repository data or
// management mutation is exposed before a trusted registry/access policy exists.
func New(c config.Config, log *slog.Logger) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		started := time.Now()
		// Paths/query/body may contain source information: record only matched route class.
		route := "unknown"
		status := http.StatusNotFound
		defer func() {
			log.Info("request", "request_id", id, "route", route, "method", r.Method, "status", status, "duration_ms", time.Since(started).Milliseconds())
		}()
		defer func() {
			if recover() != nil {
				status = 500
				writeError(w, id, 500, "INTERNAL_ERROR", "Request could not be completed.")
			}
		}()
		switch r.URL.Path {
		case Prefix + "/access-policy":
			route = "access-policy"
		case Prefix + "/healthz":
			route = "health"
		case Prefix + "/readyz":
			route = "readiness"
		case Prefix + "/v1/capabilities":
			route = "capabilities"
		default:
			writeError(w, id, status, "NOT_FOUND", "Endpoint is not available.")
			return
		}
		if r.Method != http.MethodGet {
			status = 405
			w.Header().Set("Allow", "GET")
			writeError(w, id, status, "METHOD_NOT_ALLOWED", "Use GET.")
			return
		}
		status = 200
		switch route {
		case "access-policy":
			writeJSON(w, status, accessPolicy(c, false, false))
		case "health":
			writeJSON(w, status, map[string]any{"status": "alive"})
		case "readiness":
			status = 503
			writeError(w, id, status, "CONTROL_STATE_UNAVAILABLE", "Registry and access policy are not configured.")
		case "capabilities":
			writeJSON(w, status, map[string]any{"Meta": contract.Meta{Protocol: "zoekt-single-repo-v1", RequestID: id, Status: "ok"}, "Capabilities": map[string]any{"Stage": "foundation", "Site": c.Site, "Operations": []string{}, "ManagementEnabled": false}})
		}
	})
	return webui.Wrap(http.TimeoutHandler(handler, c.RequestTimeout(), `{"Error":"Request deadline exceeded.","Meta":{"Status":"error","Code":"QUERY_DEADLINE_EXCEEDED"}}`))
}
func requestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, id string, status int, code, msg string) {
	writeJSON(w, status, contract.ErrorResponse{Error: msg, Meta: contract.Meta{Protocol: "zoekt-single-repo-v1", RequestID: id, Status: "error", Code: code, Retryable: status == 503}})
}
