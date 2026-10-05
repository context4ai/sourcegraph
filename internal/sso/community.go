package sso

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"golang.org/x/oauth2"
	"html/template"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

var ErrRegistrationClosed = errors.New("registration closed")

func (h *Handler) allowRegistration() bool {
	return h.Settings != nil && h.Settings.Snapshot().Access.Values.AllowRegistration
}
func (h *Handler) admitUser(ctx context.Context, u User) (User, bool, error) {
	if s, ok := h.store.(interface {
		AdmitUser(context.Context, User, bool) (User, bool, error)
	}); ok {
		return s.AdmitUser(ctx, u, h.allowRegistration())
	}
	// Non-production in-memory test/development stores retain their existing interface.
	role, first, e := bootstrapLogin(ctx, h.store, u.ID)
	if e != nil {
		return u, false, e
	}
	if role != roleAdmin && !h.allowRegistration() {
		return u, false, ErrRegistrationClosed
	}
	u, e = h.store.UpsertUser(ctx, u)
	return u, first, e
}
func NewCommunity(ctx context.Context, root, origin string, store Store, settings *sitesettings.Service) (*Bootstrap, error) {
	b := &Bootstrap{store: store}
	cfg := Config{Enabled: true, Origin: origin, ClientID: "community", SigningAlgorithm: "RS256"}
	if e := cfg.Validate(); e != nil {
		return nil, e
	}
	if e := initializeAccess(ctx, store, cfg, ""); e != nil {
		return nil, e
	}
	if indexed, ok := store.(interface{ EnsureIndexes(context.Context) error }); ok {
		if e := indexed.EnsureIndexes(ctx); e != nil {
			return nil, e
		}
	}
	primary := &Handler{config: cfg, store: store, Settings: settings, Providers: map[string]*Handler{}}
	if id, secret := os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET"); id != "" && secret != "" {
		c := cfg
		c.ClientID = id
		h, e := newHandler(ctx, c, secret, store, "https://accounts.google.com")
		if e != nil {
			return nil, e
		}
		h.Provider = "google"
		h.Settings = settings
		h.oauth.RedirectURL = origin + Prefix + "google/callback"
		primary.Providers["google"] = h
	}
	if id, secret := os.Getenv("GITHUB_CLIENT_ID"), os.Getenv("GITHUB_CLIENT_SECRET"); id != "" && secret != "" {
		h := &Handler{config: cfg, store: store, Settings: settings, Provider: "github", issuer: "https://github.com", client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, oauth: oauth2.Config{ClientID: id, ClientSecret: secret, RedirectURL: origin + Prefix + "github/callback", Endpoint: oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams}, Scopes: []string{"read:user"}}}
		primary.Providers["github"] = h
	}
	b.current.Store(primary)
	return b, nil
}
func (h *Handler) githubIdentity(w http.ResponseWriter, r *http.Request, f Flow, token string) {
	req, e := http.NewRequestWithContext(r.Context(), "GET", "https://api.github.com/user", nil)
	if e != nil {
		fail(w, 502, "IDENTITY_UNAVAILABLE")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, e := h.client.Do(req)
	if e != nil {
		fail(w, 502, "IDENTITY_UNAVAILABLE")
		return
	}
	defer resp.Body.Close()
	var v struct {
		ID      int64  `json:"id"`
		Login   string `json:"login"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		Picture string `json:"avatar_url"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&v) != nil || v.ID <= 0 {
		fail(w, 401, "INVALID_IDENTITY")
		return
	}
	if v.Name == "" {
		v.Name = v.Login
	}
	sub := strconv.FormatInt(v.ID, 10)
	now := time.Now()
	h.finishLogin(w, r, f, User{ID: digest(h.issuer + "\x00" + sub), Issuer: h.issuer, Subject: sub, Name: v.Name, Email: v.Email, Picture: v.Picture, Enabled: true, Created: now, LastLogin: now})
}

var closedTemplate = template.Must(template.New("closed").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta http-equiv="refresh" content="5;url={{.Target}}"><title>Registration is currently closed</title><body><main><h1>当前未开放注册 · Registration is currently closed</h1><p>{{.ZH}}</p><p>{{.EN}}</p><p><a href="https://github.com/context4ai/sourcegraph">GitHub</a></p><p>5 秒后返回 · Returning in 5 seconds</p><a href="{{.Target}}">立即返回 · Return now</a></main></body></html>`))

func (h *Handler) registrationClosed(w http.ResponseWriter, r *http.Request, target string) {
	h.cookie(w, sessionCookie, "", time.Time{})
	target = safeReturn(target)
	if target == "" {
		target = "/sourcegraph/"
	}
	v := h.Settings.Snapshot().Access.Values
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusForbidden)
	_ = closedTemplate.Execute(w, struct{ Target, ZH, EN string }{target, v.RegistrationNoticeZH, v.RegistrationNoticeEN})
}

func (h *Handler) ValidSession(r *http.Request) bool { _, _, e := h.session(r); return e == nil }

func (h *Handler) UseSession(w http.ResponseWriter, r *http.Request) bool {
	_, _, e := h.session(r)
	if errors.Is(e, ErrNotFound) {
		h.cookie(w, sessionCookie, "", time.Time{})
	}
	return e == nil
}
