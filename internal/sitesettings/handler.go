package sitesettings

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/identity"
)

const AdminPath = "/sourcegraph/api/admin/settings"
const PublicPath = "/sourcegraph/site-settings"

// Handler must be installed after the host's SSO admin + CSRF middleware for
// AdminPath. PublicPath returns only safe repository form defaults. This module
// does not accept credentials or decide who has access to administrative routes.
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.ServeHTTP) }
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id := w.Header().Get("X-Request-ID")
	if id == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err == nil {
			id = hex.EncodeToString(b[:])
			w.Header().Set("X-Request-ID", id)
		}
	}
	fail := func(e error) {
		var ce *contract.Error
		if !errors.As(e, &ce) {
			ce = &contract.Error{Code: "SETTINGS_UNAVAILABLE", Message: "Site settings are unavailable.", HTTPStatus: 503}
		}
		w.WriteHeader(ce.HTTPStatus)
		_ = json.NewEncoder(w).Encode(contract.ErrorResponse{Error: ce.Message, Meta: contract.Meta{Protocol: "site-settings-v1", RequestID: id, Status: "error", Code: ce.Code, Retryable: ce.Retryable}})
	}
	method := func(value string) bool {
		if r.Method == value {
			return true
		}
		w.Header().Set("Allow", value)
		fail(contract.Fail("METHOD_NOT_ALLOWED", "Unsupported settings method.", 405))
		return false
	}
	if r.URL.RawQuery != "" {
		fail(contract.Fail("INVALID_QUERY_PARAMETERS", "Settings endpoints do not accept query parameters.", 400))
		return
	}
	switch {
	case r.URL.Path == PublicPath:
		if method("GET") {
			_ = json.NewEncoder(w).Encode(map[string]any{"repository_defaults": s.Snapshot().RepositoryDefaults.Values})
		}
	case r.URL.Path == AdminPath:
		if method("GET") {
			// An explicit admin reload must see the persisted revision after a
			// conflict or uncertain write, even before the periodic refresh runs.
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if err := s.Refresh(ctx); err != nil {
				fail(err)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sections": s.Snapshot()})
		}
	case strings.HasPrefix(r.URL.Path, AdminPath+"/"):
		section := strings.TrimPrefix(r.URL.Path, AdminPath+"/")
		if !validSection(section) {
			fail(contract.Fail("SETTINGS_SECTION_NOT_FOUND", "Unknown settings section.", 404))
			return
		}
		if !method("PATCH") {
			return
		}
		var input struct {
			Revision int64           `json:"revision"`
			Values   json.RawMessage `json:"values"`
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if err != nil || contract.DecodeObject(body, &input) != nil {
			fail(contract.Fail("INVALID_SETTINGS_REQUEST", "Expected revision and values JSON object; maximum 8192 bytes.", 400))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		out, err := s.Update(ctx, section, input.Revision, input.Values, identity.Actor(ctx))
		if err != nil {
			fail(err)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		fail(contract.Fail("NOT_FOUND", "Unknown settings endpoint.", 404))
	}
}
