package sso

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Person is the directory view other signed-in users may see: enough to
// recognize a repository creator or choose a manager, without roles or logins.
type Person struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email,omitempty"`
	Department string `json:"department,omitempty"`
}

func person(u User) Person {
	return Person{ID: u.ID, Name: u.Name, Email: u.Email, Department: u.Department}
}

// ErrUserUnavailable reports a missing or disabled user.
var ErrUserUnavailable = errors.New("user unavailable")

// EnabledPerson resolves one enabled user, e.g. before adding a manager.
func (h *Handler) EnabledPerson(ctx context.Context, id string) (Person, error) {
	if !userIDPattern.MatchString(id) {
		return Person{}, ErrUserUnavailable
	}
	u, e := h.store.GetUser(ctx, id)
	if errors.Is(e, ErrNotFound) || (e == nil && !u.Enabled) {
		return Person{}, ErrUserUnavailable
	}
	if e != nil {
		return Person{}, e
	}
	return person(u), nil
}

// People resolves user IDs for display. Unknown IDs keep only their ID so a
// removed account still shows up in a manager list rather than vanishing.
func (h *Handler) People(ctx context.Context, ids []string) []Person {
	out := make([]Person, 0, len(ids))
	for _, id := range ids {
		p := Person{ID: id}
		if userIDPattern.MatchString(id) {
			if u, e := h.store.GetUser(ctx, id); e == nil {
				p = person(u)
			}
		}
		out = append(out, p)
	}
	return out
}

// searchPeople lets any signed-in user find enabled users by name or email,
// e.g. to add a repository manager. It returns at most 20 directory entries.
func (h *Handler) searchPeople(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		fail(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	if _, present := r.Header["Authorization"]; present {
		fail(w, 403, "ACCESS_DENIED")
		return
	}
	_, user, e := h.session(r)
	if e != nil {
		h.authError(w, e)
		return
	}
	if read, _ := h.rights(user); !read {
		fail(w, 403, "ACCESS_DENIED")
		return
	}
	values, e := url.ParseQuery(r.URL.RawQuery)
	q := strings.TrimSpace(values.Get("q"))
	if e != nil || len(values) != 1 || len(values["q"]) != 1 || q == "" || len(q) > 128 || !utf8.ValidString(q) || strings.ContainsAny(q, "\r\n\x00") {
		fail(w, 400, "INVALID_USER_FILTER")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	a, e := readAccess(ctx, h.store)
	if e != nil {
		roleError(w, e)
		return
	}
	users, e := h.store.ListUsers(ctx, UserQuery{Q: q, Limit: 40}, a.AdminIDs)
	if e != nil {
		roleError(w, e)
		return
	}
	items := make([]Person, 0, 20)
	for _, u := range users {
		if u.Enabled && len(items) < 20 {
			items = append(items, person(u))
		}
	}
	respond(w, 200, map[string]any{"items": items})
}
