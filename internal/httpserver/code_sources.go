package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/context4ai/sourcegraph/internal/codesources"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
)

// codeSourceSettings guards the website layer before the settings module stores it:
// fields set in the configuration file stay read-only, and a host in use cannot move.
func codeSourceSettings(w http.ResponseWriter, r *http.Request, s *service.Service, settings *sitesettings.Service) {
	if r.Method != "PATCH" {
		w.Header().Set("Allow", "PATCH")
		writeError(w, "", 405, "METHOD_NOT_ALLOWED", "Use PATCH.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil {
		writeError(w, "", 400, "INVALID_SETTINGS_REQUEST", "Request body exceeds 8192 bytes.")
		return
	}
	var input struct {
		Values sitesettings.CodeSource `json:"values"`
	}
	if json.Unmarshal(body, &input) == nil {
		current, ok := codesources.EditableSource()
		if !ok {
			writeError(w, "", 404, "CODE_SOURCE_NOT_EDITABLE", "This distribution has no editable code source.")
			return
		}
		requested := map[string]string{"name": input.Values.Name, "host": input.Values.Host, "username": input.Values.Username}
		for _, field := range current.Locked {
			if requested[field] != "" {
				writeError(w, "", 409, "CODE_SOURCE_LOCKED", "The "+field+" field is set in the service configuration file.")
				return
			}
		}
		ui := codesources.Override{Name: input.Values.Name, Host: input.Values.Host, Username: input.Values.Username}
		if codesources.ValidateSettings(ui) == nil {
			next := codesources.Effective(ui)
			i := slices.IndexFunc(next, func(v codesources.Source) bool { return v.ID == current.ID })
			if i >= 0 && next[i].Host != current.Host && current.Host != "" {
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				used, err := s.CodeSourceInUse(ctx, current.ID)
				cancel()
				if err != nil {
					writeError(w, "", 503, "CONTROL_STATE_UNAVAILABLE", "Cannot check repositories using this code source.")
					return
				}
				if used {
					writeError(w, "", 409, "CODE_SOURCE_IN_USE", "Registered repositories use this host; it can change only when no repository records it.")
					return
				}
			}
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	settings.ServeHTTP(w, r)
}
