package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/codecredentials"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/identity"
)

// Called only after browser session authorization and the existing CSRF checks.
func codeCredentials(w http.ResponseWriter, r *http.Request, m *codecredentials.Manager, global bool) {
	w.Header().Set("Cache-Control", "no-store")
	owner := identity.UserID(r.Context())
	if global {
		owner = codecredentials.Global
	}
	if owner == "" {
		writeError(w, "", 403, "ACCESS_DENIED", "A personal browser session is required.")
		return
	}
	if m == nil {
		writeError(w, "", 503, "CREDENTIAL_STORAGE_UNAVAILABLE", "Credential storage is unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.URL.RawQuery != "" {
		writeError(w, "", 400, "INVALID_QUERY_PARAMETERS", "No query parameters allowed.")
		return
	}
	if r.Method == "GET" {
		out, err := m.Status(ctx, owner)
		if err != nil {
			credentialError(w, err)
			return
		}
		writeJSON(w, 200, out)
		return
	}
	if r.Method != "PUT" {
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, "", 405, "METHOD_NOT_ALLOWED", "Use GET or PUT.")
		return
	}
	var input struct {
		Kind     string     `json:"kind"`
		Revision int64      `json:"revision"`
		Token    *string    `json:"token,omitempty"`
		Expires  *time.Time `json:"expires_at"`
		Clear    bool       `json:"clear"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, "", 400, "INVALID_CREDENTIAL", "Invalid credential settings.")
		return
	}
	out, err := m.Save(ctx, owner, input.Kind, codecredentials.Input{Revision: input.Revision, Token: input.Token, Expires: input.Expires, Clear: input.Clear})
	if err != nil {
		credentialError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func credentialRoute(path string) bool {
	return path == "/sourcegraph/auth/code-credentials" || path == "/sourcegraph/api/admin/settings/code-credentials"
}
func globalCredentialRoute(path string) bool { return strings.Contains(path, "/admin/") }

func credentialError(w http.ResponseWriter, err error) {
	var ce *contract.Error
	if errors.As(err, &ce) {
		writeError(w, "", ce.HTTPStatus, ce.Code, ce.Message)
	} else {
		writeError(w, "", 503, "CREDENTIAL_STORAGE_UNAVAILABLE", "Credential storage is unavailable.")
	}
}
