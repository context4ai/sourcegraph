package sso

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
)

var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type userView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Email      string    `json:"email"`
	Department string    `json:"department"`
	Role       string    `json:"role"`
	Created    time.Time `json:"created_at"`
	LastLogin  time.Time `json:"last_login_at"`
}

func viewUser(u User, a AccessControl) userView {
	return userView{u.ID, u.Name, u.Email, u.Department, a.role(u.ID), u.Created, u.LastLogin}
}

type userCursor struct {
	After    string `json:"after"`
	Filter   string `json:"filter"`
	Revision int64  `json:"revision"`
}

func userFilter(q UserQuery) string {
	b, _ := json.Marshal([]string{q.Q, q.Department, q.Role})
	return digest(string(b))
}

func (h *Handler) adminUsers(w http.ResponseWriter, r *http.Request) {
	collection := r.URL.Path == Prefix+"admin/users"
	id := ""
	if !collection {
		tail := strings.TrimPrefix(r.URL.Path, Prefix+"admin/users/")
		if !strings.HasSuffix(tail, "/role") {
			fail(w, 404, "NOT_FOUND")
			return
		}
		id = strings.TrimSuffix(tail, "/role")
		if !userIDPattern.MatchString(id) {
			fail(w, 404, "NOT_FOUND")
			return
		}
	}
	method := "PATCH"
	if collection {
		method = "GET"
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		fail(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	if _, present := r.Header["Authorization"]; present {
		fail(w, 403, "ACCESS_DENIED")
		return
	}
	session, user, e := h.session(r)
	if e != nil {
		if errors.Is(e, ErrNotFound) {
			fail(w, 403, "ACCESS_DENIED")
		} else {
			h.authError(w, e)
		}
		return
	}
	if user.Role != roleAdmin {
		fail(w, 403, "ACCESS_DENIED")
		return
	}
	if !collection && !h.csrf(r, session) {
		fail(w, 403, "CSRF_REQUIRED")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if collection {
		h.listUsers(ctx, w, r, user.ID)
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, 400, "INVALID_USER_INPUT")
		return
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	var input struct {
		Role         string `json:"role"`
		ExpectedRole string `json:"expected_role"`
	}
	if e != nil || contract.DecodeObject(data, &input) != nil || (input.Role != roleUser && input.Role != roleAdmin) || (input.ExpectedRole != roleUser && input.ExpectedRole != roleAdmin) {
		fail(w, 400, "INVALID_USER_INPUT")
		return
	}
	target, e := h.store.GetUser(ctx, id)
	if errors.Is(e, ErrNotFound) {
		fail(w, 404, "USER_NOT_FOUND")
		return
	}
	if e != nil {
		fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
		return
	}
	if !target.Enabled {
		fail(w, 409, "USER_DISABLED")
		return
	}
	a, e := changeRole(ctx, h.store, user.ID, id, input.ExpectedRole, input.Role)
	if e != nil {
		roleError(w, e)
		return
	}
	respond(w, 200, map[string]any{"user": viewUser(target, a), "admin_count": len(a.AdminIDs)})
}

func (h *Handler) listUsers(ctx context.Context, w http.ResponseWriter, r *http.Request, actor string) {
	values, parseError := url.ParseQuery(r.URL.RawQuery)
	if parseError != nil {
		fail(w, 400, "INVALID_USER_FILTER")
		return
	}
	for key, vs := range values {
		if len(vs) != 1 || (key != "q" && key != "role" && key != "department" && key != "limit" && key != "cursor") {
			fail(w, 400, "INVALID_USER_FILTER")
			return
		}
	}
	q := UserQuery{Q: strings.TrimSpace(values.Get("q")), Role: values.Get("role"), Department: strings.TrimSpace(values.Get("department")), Limit: 50}
	if len(q.Q) > 128 || len(q.Department) > 256 || !utf8.ValidString(q.Q) || !utf8.ValidString(q.Department) || strings.ContainsAny(q.Q+q.Department, "\r\n\x00") || (q.Role != "" && q.Role != roleAdmin && q.Role != roleUser) {
		fail(w, 400, "INVALID_USER_FILTER")
		return
	}
	if raw := values.Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			fail(w, 400, "INVALID_PAGINATION")
			return
		}
		q.Limit = n
	}
	a, e := readAccess(ctx, h.store)
	if e != nil {
		roleError(w, e)
		return
	}
	if a.role(actor) != roleAdmin {
		fail(w, 403, "ACCESS_DENIED")
		return
	}
	if raw := values.Get("cursor"); raw != "" {
		data, e := base64.RawURLEncoding.DecodeString(raw)
		var cursor userCursor
		if len(raw) > 1024 || e != nil || contract.DecodeObject(data, &cursor) != nil || !userIDPattern.MatchString(cursor.After) || cursor.Filter != userFilter(q) {
			fail(w, 400, "INVALID_PAGINATION")
			return
		}
		if cursor.Revision != a.Revision {
			fail(w, 409, "USER_LIST_CHANGED")
			return
		}
		q.After = cursor.After
	}
	limit := q.Limit
	q.Limit++
	users, e := h.store.ListUsers(ctx, q, a.AdminIDs)
	if e != nil {
		roleError(w, e)
		return
	}
	next := ""
	if len(users) > limit {
		users = users[:limit]
		data, _ := json.Marshal(userCursor{After: users[len(users)-1].ID, Filter: userFilter(q), Revision: a.Revision})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	items := make([]userView, 0, len(users))
	for _, u := range users {
		items = append(items, viewUser(u, a))
	}
	respond(w, 200, map[string]any{"items": items, "next_cursor": next, "admin_count": len(a.AdminIDs)})
}
func roleError(w http.ResponseWriter, e error) {
	var ce *contract.Error
	if errors.As(e, &ce) {
		fail(w, ce.HTTPStatus, ce.Code)
	} else {
		fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
	}
}
