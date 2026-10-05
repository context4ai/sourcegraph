package httpserver

import (
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/service"
	"io"
	"net/http"
)

// Called only after the site's SSO administrator and CSRF checks.
func storageSettings(w http.ResponseWriter, r *http.Request, s *service.Service) {
	if r.Method == "GET" {
		writeJSON(w, 200, s.StorageSettings())
		return
	}
	if r.Method != "PATCH" {
		w.Header().Set("Allow", "GET, PATCH")
		writeError(w, "", 405, "METHOD_NOT_ALLOWED", "Use GET or PATCH.")
		return
	}
	var input struct {
		Revision      int64  `json:"revision"`
		BaseDirectory string `json:"base_directory"`
		Create        bool   `json:"create"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil || contract.DecodeObject(raw, &input) != nil {
		writeError(w, "", 400, "INVALID_STORAGE_SETTINGS", "Expected revision, base_directory and create.")
		return
	}
	result, err := s.ChangeStorage(r.Context(), input.Revision, input.BaseDirectory, input.Create)
	if err != nil {
		var ce *contract.Error
		if errors.As(err, &ce) {
			writeError(w, "", ce.HTTPStatus, ce.Code, ce.Message)
		} else {
			writeError(w, "", 503, "STORAGE_SAVE_FAILED", "Directory configuration could not be saved.")
		}
		return
	}
	writeJSON(w, 200, result)
}
