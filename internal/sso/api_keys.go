package sso

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APIKey keeps the digest for authentication and the plaintext so its owner can list and copy it.
// Keys created before plaintext was stored have an empty Key and show only the prefix.
type APIKey struct {
	AllowPrepare bool       `bson:"allow_prepare,omitempty" json:"allow_prepare"`
	ID           string     `bson:"_id" json:"id"`
	UserID       string     `bson:"user_id" json:"-"`
	Name         string     `bson:"name" json:"name"`
	Hash         string     `bson:"key_hash" json:"-"`
	Key          string     `bson:"key,omitempty" json:"key,omitempty"`
	Prefix       string     `bson:"key_prefix" json:"key_prefix"`
	Created      time.Time  `bson:"created_at" json:"created_at"`
	Expires      *time.Time `bson:"expires_at,omitempty" json:"expires_at"`
}
type APIKeyStore interface {
	CreateAPIKey(context.Context, APIKey) error
	ListAPIKeys(context.Context, string, string, int) ([]APIKey, error)
	DeleteAPIKey(context.Context, string, string) (bool, error)
}

func (h *Handler) apiKeys(w http.ResponseWriter, r *http.Request) {
	collection := r.URL.Path == Prefix+"api-keys"
	id := strings.TrimPrefix(r.URL.Path, Prefix+"api-keys/")
	if !collection && (id == "" || len(id) > 128 || strings.Contains(id, "/")) {
		fail(w, 404, "NOT_FOUND")
		return
	}
	allow := "DELETE"
	if collection {
		allow = "GET, POST"
	}
	if (collection && r.Method != "GET" && r.Method != "POST") || (!collection && r.Method != "DELETE") {
		w.Header().Set("Allow", allow)
		fail(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	session, user, err := h.session(r)
	if err != nil {
		h.authError(w, err)
		return
	}
	if r.Method != "GET" && !h.csrf(r, session) {
		fail(w, 403, "CSRF_REQUIRED")
		return
	}
	store, ok := h.store.(APIKeyStore)
	if !ok {
		fail(w, 503, "API_KEY_STORAGE_UNAVAILABLE")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	switch r.Method {
	case "POST":
		var input struct {
			AllowPrepare bool       `json:"allow_prepare"`
			Name         string     `json:"name"`
			Expires      *time.Time `json:"expires_at"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			fail(w, 400, "INVALID_API_KEY_INPUT")
			return
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			fail(w, 400, "INVALID_API_KEY_INPUT")
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		now := time.Now().UTC()
		if input.Name == "" || len(input.Name) > 128 || strings.ContainsAny(input.Name, "\r\n\x00") || (input.Expires != nil && !input.Expires.After(now)) {
			fail(w, 400, "INVALID_API_KEY_INPUT")
			return
		}
		if input.AllowPrepare && user.Role != roleAdmin {
			fail(w, 403, "ADMIN_REQUIRED")
			return
		}
		raw := "sgk_" + random()
		record := APIKey{AllowPrepare: input.AllowPrepare, ID: random(), UserID: user.ID, Name: input.Name, Hash: digest(raw), Key: raw, Prefix: raw[:12], Created: now, Expires: input.Expires}
		if err = store.CreateAPIKey(ctx, record); err != nil {
			fail(w, 503, "API_KEY_STORAGE_UNAVAILABLE")
			return
		}
		respond(w, 201, record)
	case "GET":
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 100 {
				fail(w, 400, "INVALID_PAGINATION")
				return
			}
		}
		after := r.URL.Query().Get("after")
		if len(after) > 128 {
			fail(w, 400, "INVALID_PAGINATION")
			return
		}
		items, err := store.ListAPIKeys(ctx, user.ID, after, limit+1)
		if err != nil {
			fail(w, 503, "API_KEY_STORAGE_UNAVAILABLE")
			return
		}
		next := ""
		if len(items) > limit {
			items = items[:limit]
			next = items[len(items)-1].ID
		}
		if items == nil {
			items = []APIKey{}
		}
		respond(w, 200, map[string]any{"items": items, "next_cursor": next})
	case "DELETE":
		deleted, err := store.DeleteAPIKey(ctx, user.ID, id)
		if err != nil {
			fail(w, 503, "API_KEY_STORAGE_UNAVAILABLE")
			return
		}
		// Missing and foreign-owned IDs deliberately have the same result.
		respond(w, 200, map[string]bool{"deleted": deleted})
	}
}
