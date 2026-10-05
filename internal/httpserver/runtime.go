package httpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/config"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
	"github.com/context4ai/sourcegraph/internal/mcpserver"
	"github.com/context4ai/sourcegraph/internal/querylog"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"github.com/context4ai/sourcegraph/internal/sso"
	"github.com/context4ai/sourcegraph/internal/webui"
)

// Runtime uses the configured website policy. Machine credentials remain
// available for automation; account and site administration require SSO.
func Runtime(c config.Config, log *slog.Logger, s *service.Service, token string, auth ...*sso.Handler) http.Handler {
	return runtime(c, log, s, token, nil, nil, auth...)
}
func RuntimeWithSettings(c config.Config, log *slog.Logger, s *service.Service, token string, settings *sitesettings.Service, auth *sso.Handler) http.Handler {
	return runtime(c, log, s, token, settings, nil, auth)
}
func RuntimeWithBootstrap(c config.Config, log *slog.Logger, s *service.Service, token string, settings *sitesettings.Service, bootstrap *sso.Bootstrap) http.Handler {
	return runtime(c, log, s, token, settings, bootstrap)
}
func runtime(c config.Config, log *slog.Logger, s *service.Service, token string, settings *sitesettings.Service, bootstrap *sso.Bootstrap, auth ...*sso.Handler) http.Handler {
	fallback := New(c, log)
	limits := &websiteLimits{}
	mcpHandler := mcpserver.New(s)
	// Discovery routing filters tools; keep this independent invocation guard
	// as well. CanPrepare only reads the already-authenticated request context.
	personalMCP := mcpserver.NewWithPrepareAuthorization(s, func(ctx context.Context) error {
		if identity.CanPrepare(ctx) {
			if !limits.allow(identity.Actor(ctx), "prepare", 10, 60) {
				return contract.Fail("RATE_LIMITED", "Retry preparation after one minute.", 429)
			}
			return nil
		}
		return contract.Fail("ACCESS_DENIED", "Index preparation requires a token with prepare permission and an active administrator owner.", 403)
	})
	readOnlyMCP := mcpserver.NewReadOnly(s)

	expected := sha256.Sum256([]byte(token))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := c // request-local snapshot; a saved access policy affects the next request.
		if settings != nil {
			c.WebsitePublicRead = settings.PublicRead()
		}
		c.WebsitePublicPrepare = false
		var browserAuth *sso.Handler
		if len(auth) > 0 {
			browserAuth = auth[0]
		}
		if bootstrap != nil {
			browserAuth = bootstrap.Current()
		}
		if credentialRoute(r.URL.Path) {
			if browserAuth == nil {
				writeError(w, "", 401, "AUTHENTICATION_REQUIRED", "Sign in to manage code-service credentials.")
				return
			}
			if _, present := r.Header["Authorization"]; present {
				writeError(w, "", 403, "ACCESS_DENIED", "Use a browser session.")
				return
			}
			global := globalCredentialRoute(r.URL.Path)
			authenticated, ok := browserAuth.Authorize(w, r, global)
			if !ok {
				return
			}
			codeCredentials(w, authenticated, s.Credentials, global)
			return
		}
		if r.URL.Path == sso.SetupPath && bootstrap != nil {
			bootstrap.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == sitesettings.PublicPath || r.URL.Path == sitesettings.AdminPath || strings.HasPrefix(r.URL.Path, sitesettings.AdminPath+"/") {
			if r.URL.Path != sitesettings.PublicPath {
				if _, present := r.Header["Authorization"]; present {
					writeError(w, "", 403, "ACCESS_DENIED", "Site administration requires a browser session without an Authorization header.")
					return
				}
				if browserAuth == nil {
					writeError(w, "", 401, "AUTHENTICATION_REQUIRED", "Administrator login is required.")
					return
				}
				var ok bool
				r, ok = browserAuth.Authorize(w, r, true)
				if !ok {
					return
				}
			}
			if r.URL.Path == sitesettings.AdminPath+"/query-errors" {
				queryErrors(w, r, s)
				return
			}
			if r.URL.Path == sitesettings.AdminPath+"/jobs/clear" {
				clearFinishedJobs(w, r, s)
				return
			}
			if r.URL.Path == sitesettings.AdminPath+"/storage" {
				if r.URL.RawQuery != "" {
					writeError(w, "", 400, "INVALID_QUERY_PARAMETERS", "No query parameters allowed.")
					return
				}
				storageSettings(w, r, s)
				return
			}
			if settings == nil {
				writeError(w, "", 503, "SETTINGS_UNAVAILABLE", "Site settings are not configured.")
				return
			}
			settings.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == Prefix+"/access-policy" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != "GET" {
				w.Header().Set("Allow", "GET")
				writeError(w, "", 405, "METHOD_NOT_ALLOWED", "Use GET.")
			} else {
				writeJSON(w, 200, accessPolicy(c, true, browserAuth != nil))
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, sso.Prefix) {
			if browserAuth == nil {
				writeError(w, "", 404, "SSO_DISABLED", "Browser login is not enabled.")
				return
			}
			browserAuth.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == Prefix+"/healthz" {
			fallback.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == Prefix+"/readyz" && r.Method == "GET" {
			_, e := s.Catalog(r.Context(), "")
			if e != nil {
				writeError(w, "", 503, "CONTROL_STATE_UNAVAILABLE", "Control store unavailable.")
			} else {
				writeJSON(w, 200, map[string]string{"status": "ready", "scope": "authenticated-backend"})
			}
			return
		}
		id := requestID()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if recover() != nil {
				writeError(w, id, 500, "INTERNAL_ERROR", "Request could not complete.")
			}
		}()
		started := time.Now()
		recorded := &statusWriter{ResponseWriter: w}
		w = recorded
		status := 200
		defer func() {
			log.Info("request", "request_id", id, "method", r.Method, "status", recorded.status, "duration_ms", time.Since(started).Milliseconds())
		}()
		transport := "http"
		if r.URL.Path == Prefix+"/mcp" {
			transport = "mcp"
		}
		r = r.WithContext(querylog.WithRequest(r.Context(), id, transport))
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		hash := sha256.Sum256([]byte(supplied))
		bearer := len(token) >= 32 && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && subtle.ConstantTimeCompare(hash[:], expected[:]) == 1
		anonymous := false
		personal := false
		if !bearer && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sgk_") && browserAuth != nil {
			var ok bool
			r, ok = browserAuth.AuthorizeAPIKey(w, r)
			if !ok {
				return
			}
			if r.URL.Path != Prefix+"/mcp" && !publicRead(r) {
				writeError(w, id, 403, "ACCESS_DENIED", "Personal API tokens are read-only.")
				return
			}
			personal = true
		}
		if !bearer && !personal {
			if r.Header.Get("Authorization") != "" {
				writeError(w, id, 401, "AUTHENTICATION_REQUIRED", "The supplied service credential is invalid.")
				return
			}
			public := c.WebsitePublicRead && (publicRead(r) || r.URL.Path == Prefix+"/mcp" || (c.WebsitePublicPrepare && r.Method == "POST" && r.URL.Path == Prefix+"/api/admin/v1/repo/prepare"))
			if public && browserAuth != nil && browserAuth.HasSession(r) && browserAuth.UseSession(w, r) && r.URL.Path != Prefix+"/mcp" {
				var ok bool
				r, ok = browserAuth.Authorize(w, r, false)
				if !ok {
					return
				}
			} else if public {
				anonymous = true
				if !limits.allow(anonymousActor(r), "read", 1200, 3000) {
					w.Header().Set("Retry-After", "60")
					writeError(w, id, 429, "RATE_LIMITED", "Request rate limit reached; retry after one minute.")
					return
				}
				r = r.WithContext(identity.WithAnonymous(r.Context(), anonymousActor(r)))
			} else if browserAuth != nil && r.URL.Path != Prefix+"/mcp" {
				var ok bool
				r, ok = browserAuth.Authorize(w, r, strings.HasPrefix(r.URL.Path, Prefix+"/api/admin/") && !publicRead(r))
				if !ok {
					return
				}
			} else {
				writeError(w, id, 401, "AUTHENTICATION_REQUIRED", "Service access token or browser session is required.")
				return
			}
		}
		if r.URL.Path == Prefix+"/mcp" {
			r.Body = http.MaxBytesReader(w, r.Body, c.MaxBodyBytes)
			if personal && identity.CanPrepare(r.Context()) {
				personalMCP.ServeHTTP(w, r)
			} else if anonymous || personal {
				readOnlyMCP.ServeHTTP(w, r)
			} else {
				mcpHandler.ServeHTTP(w, r)
			}
			return
		}
		if r.URL.RawQuery != "" {
			for k, v := range r.URL.Query() {
				if (k != "repo" && k != "after" && !(k == "revision" && r.URL.Path == Prefix+"/v1/repo/plugins") && !(k == "branch" && r.URL.Path == Prefix+"/api/admin/v1/repo/commit-options")) || len(v) != 1 {
					status = 400
					writeError(w, id, status, "INVALID_QUERY_PARAMETERS", "Unexpected or repeated query parameter.")
					return
				}
			}
		}
		repo := r.URL.Query().Get("repo")
		var result any
		var err error
		decode := func(v any) bool {
			b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, c.MaxBodyBytes))
			if e == nil {
				e = contract.DecodeObject(b, v)
			}
			if e != nil {
				err = contract.Fail("INVALID_REQUEST", "Invalid JSON fields or request size.", 400)
				return false
			}
			return true
		}
		method := func(m string) bool {
			if r.Method != m {
				w.Header().Set("Allow", m)
				err = contract.Fail("METHOD_NOT_ALLOWED", "Unsupported method.", 405)
				return false
			}
			return true
		}
		switch r.URL.Path {
		case Prefix + "/readyz":
			if method("GET") {
				_, err = s.Catalog(r.Context(), "")
				result = map[string]any{"status": "ready", "scope": "authenticated-backend"}
			}
		case Prefix + "/api/admin/v1/statistics":
			if method("GET") {
				result = s.StatisticsFor(r.Context())
				if s.Credentials != nil {
					budget, stop := context.WithTimeout(r.Context(), 3*time.Second)
					result.(map[string]any)["credential_warnings"] = s.Credentials.Warnings(budget)
					stop()
				}
			}
		case Prefix + "/api/admin/v1/instances":
			if method("GET") {
				result, err = s.Instances(r.Context())
			}
		case Prefix + "/v1/capabilities":
			if method("GET") {
				result = map[string]any{"Protocol": "zoekt-single-repo-v1", "Operations": []string{"search", "read", "list", "diff", "resolve", "availability", "commits", "prepare"}, "Authentication": "access-policy", "SearchProfile": "text-no-ctags", "SymbolSearch": false, "ChunkMatches": false}
			}
		case Prefix + "/v1/repos", Prefix + "/api/admin/v1/repos":
			if r.Method == "GET" {
				result, err = s.Catalog(r.Context(), r.URL.Query().Get("after"))
			} else if r.URL.Path == Prefix+"/api/admin/v1/repos" && method("POST") {
				var q registrationRequest
				if decode(&q) {
					var policy control.Policy
					policy, err = registrationPolicy(q, settings)
					if err == nil {
						interval := control.DefaultSyncIntervalMinutes
						if q.SyncIntervalMinutes != nil {
							interval = *q.SyncIntervalMinutes
						}
						result, err = s.RegisterWithProvider(r.Context(), q.Name, policy, interval, q.IndexMode, q.PathGroups, q.PublicRead, q.CodeProvider, r.Header.Get("Idempotency-Key"))
					}
					status = 202
				}
			} else {
				err = contract.Fail("METHOD_NOT_ALLOWED", "Use GET.", 405)
			}
		case Prefix + "/v1/repo/plugins":
			if r.Method == "GET" {
				result, err = s.DiscoverPlugins(r.Context(), repo, r.URL.Query().Get("revision"))
			} else {
				err = contract.Fail("METHOD_NOT_ALLOWED", "Use GET.", 405)
			}
		case Prefix + "/v1/repo/availability", Prefix + "/v1/repo/commits", Prefix + "/api/admin/v1/repo":
			if r.Method == "GET" {
				var row control.Repository
				row, err = s.Repo(r.Context(), repo)
				w.Header().Set("ETag", `"`+strconv.FormatInt(row.Revision, 10)+`"`)
				result = row
			} else if r.URL.Path == Prefix+"/api/admin/v1/repo" && method("PATCH") {
				var q service.Update
				if decode(&q) {
					rev, e := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), `"`), 10, 64)
					if e != nil {
						err = contract.Fail("PRECONDITION_REQUIRED", "If-Match revision is required.", 428)
					} else {
						result, err = s.Update(r.Context(), repo, q, rev)
					}
				}
			} else {
				err = contract.Fail("METHOD_NOT_ALLOWED", "Use GET.", 405)
			}
		case Prefix + "/api/admin/v1/repo/preview":
			if method("POST") {
				var q service.Update
				if decode(&q) {
					result, err = s.Preview(r.Context(), repo, q)
				}
			}
		case Prefix + "/api/admin/v1/repo/prepare":
			if method("POST") {
				var q struct {
					Group  string `json:"group"`
					Kind   string `json:"kind"`
					Commit string `json:"commit"`
				}
				if decode(&q) {
					if anonymous {
						err = limits.authorizePrepare(r, c)
					}
					if err == nil {
						result, err = s.QueueSelection(r.Context(), repo, q.Kind, q.Commit, q.Group, r.Header.Get("Idempotency-Key"))
					}
					status = 202
				}
			}
		case Prefix + "/api/admin/v1/repo/commit-options", Prefix + "/api/admin/v1/repo/index/preview", Prefix + "/api/admin/v1/repo/index", Prefix + "/api/admin/v1/repo/cleanup/preview", Prefix + "/api/admin/v1/repo/cleanup", Prefix + "/api/admin/v1/repo/delete/preview", Prefix + "/api/admin/v1/repo/delete":
			nextResult, nextErr, nextStatus := repositoryMaintenance(r, s, decode, method)
			result, status = nextResult, nextStatus
			if nextErr != nil {
				err = nextErr
			}
		case Prefix + "/api/admin/v1/repo/jobs/cancel":
			if method("POST") {
				var q struct {
					JobID string `json:"job_id"`
				}
				if decode(&q) {
					result, err = s.CancelJob(r.Context(), repo, q.JobID)
				}
			}
		case Prefix + "/api/admin/v1/repo/purge":
			if method("POST") {
				rev, e := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), `"`), 10, 64)
				if e != nil {
					err = contract.Fail("PRECONDITION_REQUIRED", "If-Match revision is required.", 428)
				} else {
					err = s.Purge(r.Context(), repo, rev)
					result = map[string]bool{"purged": err == nil}
				}
			}
		case Prefix + "/v1/repo/resolve":
			if method("POST") {
				var q contract.ResolveRequest
				if decode(&q) {
					result, err = s.Resolve(r.Context(), repo, q.Revision)
				}
			}
		case Prefix + "/v1/repo/read":
			if method("POST") {
				var q contract.ReadRequest
				if decode(&q) {
					result, err = s.Read(r.Context(), repo, q)
				}
			}
		case Prefix + "/v1/repo/list":
			if method("POST") {
				var q contract.ListRequest
				if decode(&q) {
					result, err = s.List(r.Context(), repo, q)
				}
			}
		case Prefix + "/v1/repo/diff":
			if method("POST") {
				var q contract.DiffRequest
				if decode(&q) {
					result, err = s.Diff(r.Context(), repo, q)
				}
			}
		case Prefix + "/api/search":
			if method("POST") {
				var q contract.SearchRequest
				if decode(&q) {
					result, err = s.Search(r.Context(), repo, q)
				}
			}
		default:
			err = contract.Fail("NOT_FOUND", "Endpoint is not available.", 404)
		}
		if err != nil {
			var ce *contract.Error
			if errors.As(err, &ce) {
				status = ce.HTTPStatus
				writeError(w, id, status, ce.Code, ce.Message)
			} else {
				status = 503
				writeError(w, id, status, "DEPENDENCY_UNAVAILABLE", "Operation could not complete within its budget.")
			}
			return
		}
		writeJSON(w, status, result)
	})
	var guarded http.Handler = h
	return webui.Wrap(http.TimeoutHandler(guarded, c.RequestTimeout(), `{"Error":"Request deadline exceeded.","Meta":{"Status":"error","Code":"QUERY_DEADLINE_EXCEEDED"}}`))
}
