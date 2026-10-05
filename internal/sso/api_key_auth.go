package sso

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/identity"
)

type APIKeyLookup interface {
	FindAPIKey(context.Context, string) (APIKey, error)
}

// AuthorizeAPIKey authenticates an enabled user without granting administrative
// authority. Optional prepare permission is rechecked against the current role.
// Lookup on every request makes deletion and user disablement effective promptly;
// authentication never writes last-use timestamps into the query path.
func (h *Handler) AuthorizeAPIKey(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	header := r.Header.Get("Authorization")
	raw := strings.TrimPrefix(header, "Bearer ")
	if !strings.HasPrefix(header, "Bearer sgk_") || len(raw) != 47 || strings.ContainsAny(raw, " \t\r\n") {
		fail(w, 401, "AUTHENTICATION_REQUIRED")
		return r, false
	}
	store, ok := h.store.(APIKeyLookup)
	if !ok {
		fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
		return r, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	hash := digest(raw)
	key, err := store.FindAPIKey(ctx, hash)
	if err != nil {
		h.authError(w, err)
		return r, false
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(key.Hash)) != 1 || key.ID == "" || key.UserID == "" || (key.Expires != nil && !key.Expires.After(time.Now())) {
		fail(w, 401, "AUTHENTICATION_REQUIRED")
		return r, false
	}
	user, err := h.store.GetUser(ctx, key.UserID)
	if err != nil {
		h.authError(w, err)
		return r, false
	}
	if !user.Enabled {
		fail(w, 401, "AUTHENTICATION_REQUIRED")
		return r, false
	}
	access, err := readAccess(ctx, h.store)
	if err != nil {
		fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
		return r, false
	}
	if access.role(user.ID) != roleAdmin && !h.allowRegistration() {
		fail(w, 403, "REGISTRATION_CLOSED")
		return r, false
	}
	canPrepare := false
	if key.AllowPrepare {
		access, e := readAccess(ctx, h.store)
		if e != nil {
			fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
			return r, false
		}
		canPrepare = access.role(user.ID) == roleAdmin
	}
	ctx = identity.WithActor(r.Context(), "api-key:"+user.ID+":"+key.ID)
	ctx = identity.WithPreparePermission(ctx, canPrepare)
	return r.WithContext(ctx), true
}
