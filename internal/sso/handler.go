// Package sso implements an isolated, server-side OIDC browser session.
package sso

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	deployment "github.com/context4ai/sourcegraph/config"
	"github.com/context4ai/sourcegraph/internal/identity"
	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

const Prefix = "/sourcegraph/auth/"
const sessionCookie = "sourcegraph_session"
const flowCookie = "sourcegraph_login_binding"

var Issuer = deployment.Load().SSO.Issuer

type Handler struct {
	Settings         *sitesettings.Service
	Providers        map[string]*Handler
	Provider         string
	verifiedSetup    func() error
	config           Config
	store            Store
	oauth            oauth2.Config
	verifier         *oidc.IDTokenVerifier
	fallbackVerifier *oidc.IDTokenVerifier
	client           *http.Client
	issuer           string
	userInfoURL      string
}
type hmacKeys struct{ secret []byte }

func (k hmacKeys) VerifySignature(_ context.Context, raw string) ([]byte, error) {
	token, e := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.HS256})
	if e != nil {
		return nil, e
	}
	return token.Verify(k.secret)
}
func New(ctx context.Context, c Config, secret string, store Store) (*Handler, error) {
	return newHandler(ctx, c, secret, store, Issuer)
}
func newHandler(ctx context.Context, c Config, secret string, store Store, issuer string) (*Handler, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if !c.Enabled || secret == "" || store == nil {
		return nil, errors.New("SSO credentials and store are required")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx = oidc.ClientContext(ctx, client)
	provider, e := oidc.NewProvider(ctx, issuer)
	if e != nil {
		return nil, errors.New("SSO discovery failed")
	}
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	// Only the configured issuer may receive credentials; discovery is not a redirect proxy.
	origin, _ := url.Parse(issuer)
	for _, raw := range []string{endpoint.AuthURL, endpoint.TokenURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != origin.Scheme || (u.Host != origin.Host && !(issuer == "https://accounts.google.com" && (u.Host == "oauth2.googleapis.com" || u.Host == "openidconnect.googleapis.com"))) || u.User != nil {
			return nil, errors.New("unexpected SSO endpoint")
		}
	}
	var metadata struct {
		UserInfo string `json:"userinfo_endpoint"`
	}
	if e = provider.Claims(&metadata); e != nil {
		return nil, errors.New("invalid SSO metadata")
	}
	if metadata.UserInfo != "" {
		u, err := url.Parse(metadata.UserInfo)
		if err != nil || u.Scheme != origin.Scheme || (u.Host != origin.Host && !(issuer == "https://accounts.google.com" && (u.Host == "oauth2.googleapis.com" || u.Host == "openidconnect.googleapis.com"))) || u.User != nil {
			return nil, errors.New("unexpected SSO UserInfo endpoint")
		}
	}
	if e = initializeAccess(ctx, store, c, issuer); e != nil {
		return nil, e
	}
	algorithm := c.SigningAlgorithm
	if algorithm == "auto" {
		algorithm = "RS256"
	}
	verifyConfig := &oidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: []string{algorithm}}
	verifier := provider.VerifierContext(ctx, verifyConfig)
	if c.SigningAlgorithm == "HS256" {
		verifier = oidc.NewVerifier(issuer, hmacKeys{[]byte(secret)}, verifyConfig)
	}
	var fallback *oidc.IDTokenVerifier
	if c.SigningAlgorithm == "auto" {
		fallback = oidc.NewVerifier(issuer, hmacKeys{[]byte(secret)}, &oidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: []string{"HS256"}})
	}
	return &Handler{userInfoURL: metadata.UserInfo, fallbackVerifier: fallback, config: c, store: store, issuer: issuer, client: client, verifier: verifier, oauth: oauth2.Config{ClientID: c.ClientID, ClientSecret: secret, RedirectURL: c.Origin + Prefix + "callback", Endpoint: endpoint, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}}, nil
}
func random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic("secure randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (h *Handler) cookie(w http.ResponseWriter, name, value string, expiry time.Time) {
	age := int(time.Until(expiry).Seconds())
	if value == "" {
		age = -1
		expiry = time.Unix(1, 0)
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/sourcegraph", Secure: strings.HasPrefix(h.config.Origin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age, Expires: expiry})
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]string{"error": code})
}
func safeReturn(raw string) string {
	if raw == "" {
		return "/sourcegraph/"
	}
	u, e := url.Parse(raw)
	if e != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || strings.ContainsAny(u.Path, "\\\r\n\x00") || strings.HasPrefix(u.Path, "//") {
		return ""
	}
	if u.Path != "/" && u.Path != "/context" && u.Path != "/sourcegraph" && !strings.HasPrefix(u.Path, "/sourcegraph/") && !strings.HasPrefix(u.Path, "/context/") {
		return ""
	}
	if path.Clean(strings.TrimSuffix(u.Path, "/")) != strings.TrimSuffix(u.Path, "/") && u.Path != "/" {
		return ""
	}
	if strings.HasPrefix(u.Path, Prefix) || strings.HasPrefix(u.Path, "/sourcegraph/login") {
		return ""
	}
	return u.String()
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == Prefix+"providers" {
		names := []string{}
		for _, name := range []string{"google", "github"} {
			if h.Providers[name] != nil {
				names = append(names, name)
			}
		}
		respond(w, 200, map[string]any{"providers": names})
		return
	}
	if len(h.Providers) > 0 {
		rest := strings.TrimPrefix(r.URL.Path, Prefix)
		parts := strings.Split(rest, "/")
		if len(parts) == 2 && (parts[1] == "login" || parts[1] == "callback") {
			p := h.Providers[parts[0]]
			if p == nil {
				fail(w, 404, "PROVIDER_NOT_CONFIGURED")
				return
			}
			copy := r.Clone(r.Context())
			copy.URL.Path = Prefix + parts[1]
			p.ServeHTTP(w, copy)
			return
		}
		if r.URL.Path == Prefix+"login" {
			http.Redirect(w, r, "/sourcegraph/login?return_to="+url.QueryEscape(r.URL.Query().Get("return_to")), http.StatusSeeOther)
			return
		}
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.Path == Prefix+"admin/users" || strings.HasPrefix(r.URL.Path, Prefix+"admin/users/") {
		h.adminUsers(w, r)
		return
	}
	if r.URL.Path == Prefix+"api-keys" || strings.HasPrefix(r.URL.Path, Prefix+"api-keys/") {
		h.apiKeys(w, r)
		return
	}
	method := "GET"
	if r.URL.Path == Prefix+"logout" {
		method = "POST"
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		fail(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	switch r.URL.Path {
	case Prefix + "login":
		h.login(w, r)
	case Prefix + "callback":
		h.callback(w, r)
	case Prefix + "me":
		h.me(w, r)
	case Prefix + "logout":
		h.logout(w, r)
	case Prefix + "session":
		h.page(w, r)
	default:
		fail(w, 404, "NOT_FOUND")
	}
}
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, 403, "CROSS_SITE_LOGIN")
		return
	}
	target := safeReturn(r.URL.Query().Get("return_to"))
	if target == "" {
		fail(w, 400, "INVALID_RETURN_TO")
		return
	}
	state, binding, nonce := random(), random(), random()
	expires := time.Now().Add(5 * time.Minute)
	if e := h.store.PutFlow(r.Context(), Flow{ID: digest(state), Binding: digest(binding), Nonce: nonce, Provider: h.Provider, ReturnTo: target, Expires: expires}); e != nil {
		fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
		return
	}
	h.cookie(w, flowCookie, binding, expires)
	http.Redirect(w, r, h.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(nonce)), http.StatusFound)
}
func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	binding, e := r.Cookie(flowCookie)
	h.cookie(w, flowCookie, "", time.Time{})
	if e != nil || q.Get("state") == "" || len(q["state"]) != 1 || len(q.Get("state")) > 256 {
		fail(w, 400, "INVALID_LOGIN_STATE")
		return
	}
	f, e := h.store.ConsumeFlow(r.Context(), digest(q.Get("state")), digest(binding.Value), time.Now())
	if e != nil {
		if errors.Is(e, ErrNotFound) {
			fail(w, 400, "INVALID_LOGIN_STATE")
		} else {
			fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
		}
		return
	}
	if f.Provider != h.Provider {
		fail(w, 400, "INVALID_LOGIN_PROVIDER")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" || len(q["code"]) != 1 {
		fail(w, 400, "LOGIN_NOT_COMPLETED")
		return
	}
	ctx := oidc.ClientContext(r.Context(), h.client)
	token, e := h.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(f.Nonce))
	if e != nil {
		fail(w, 502, "SSO_TOKEN_EXCHANGE_FAILED")
		return
	}
	if h.Provider == "github" {
		h.githubIdentity(w, r, f, token.AccessToken)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		fail(w, 401, "INVALID_IDENTITY")
		return
	}
	id, e := h.verifier.Verify(ctx, raw)
	if e != nil && h.fallbackVerifier != nil {
		id, e = h.fallbackVerifier.Verify(ctx, raw)
	}
	if e != nil || id.Subject == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(f.Nonce)) != 1 {
		reason := "subject_or_nonce"
		if e != nil {
			reason = "token_verification"
		}
		slog.Warn("SSO identity rejected", "reason", reason)
		fail(w, 401, "INVALID_IDENTITY")
		return
	}
	var claims struct {
		Name       string          `json:"name"`
		Email      string          `json:"email"`
		Picture    string          `json:"picture"`
		Department json.RawMessage `json:"department"`
		NotBefore  int64           `json:"nbf"`
	}
	now := time.Now()
	// Providers and the application may differ by a few seconds. Keep a
	// bounded one-minute skew for nbf/iat; signature, issuer, audience, nonce
	// and expiry are still verified above, without extending token expiry.
	if id.Claims(&claims) != nil || claims.NotBefore > now.Add(time.Minute).Unix() || id.IssuedAt.After(now.Add(time.Minute)) {
		slog.Warn("SSO identity rejected", "reason", "claims_or_time")
		fail(w, 401, "INVALID_IDENTITY")
		return
	}
	if len(claims.Name) > 512 || len(claims.Email) > 512 || len(claims.Picture) > 4096 || len(id.Subject) > 512 {
		fail(w, 401, "INVALID_IDENTITY")
		return
	}
	if h.verifiedSetup != nil {
		if err := h.verifiedSetup(); err != nil {
			fail(w, 503, "SSO_SETUP_SAVE_FAILED")
			return
		}
	}
	h.finishLogin(w, r, f, User{ID: digest(h.issuer + "\x00" + id.Subject), Issuer: h.issuer, Subject: id.Subject, Name: claims.Name, Email: claims.Email, Picture: claims.Picture, Enabled: true, Created: now, LastLogin: now})
}
func (h *Handler) finishLogin(w http.ResponseWriter, r *http.Request, f Flow, input User) {
	ctx := r.Context()
	now := time.Now()
	u, firstLogin, e := h.admitUser(ctx, input)
	if errors.Is(e, ErrRegistrationClosed) {
		h.registrationClosed(w, r, f.ReturnTo)
		return
	}
	if e != nil {
		fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
		return
	}
	if !u.Enabled {
		fail(w, 403, "USER_DISABLED")
		return
	}
	// The ID Token is verified at authentication above. The site's opaque
	// session has its own fixed lifetime; no upstream token is reused or renewed.
	expiry := now.Add(90 * 24 * time.Hour)
	value := random()
	session := Session{ID: digest(value), UserID: u.ID, CSRF: random(), Expires: expiry}
	// Reauthentication rotates and revokes this browser's previous session.
	if old, e := r.Cookie(sessionCookie); e == nil {
		if e = h.store.DeleteSession(ctx, digest(old.Value)); e != nil {
			fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
			return
		}
	}
	if e = h.store.PutSession(ctx, session); e != nil {
		fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
		return
	}
	h.cookie(w, sessionCookie, value, expiry)
	target := f.ReturnTo
	if firstLogin {
		target = "/sourcegraph/settings?setup=1"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
func (h *Handler) session(r *http.Request) (Session, User, error) {
	c, e := r.Cookie(sessionCookie)
	if e != nil || len(c.Value) > 256 {
		return Session{}, User{}, ErrNotFound
	}
	s, e := h.store.GetSession(r.Context(), digest(c.Value))
	if e != nil {
		return s, User{}, e
	}
	if !s.Expires.After(time.Now()) {
		return s, User{}, ErrNotFound
	}
	u, e := h.store.GetUser(r.Context(), s.UserID)
	if e != nil {
		return s, u, e
	}
	if !u.Enabled {
		return s, u, ErrNotFound
	}
	access, e := readAccess(r.Context(), h.store)
	if e != nil {
		return s, u, errors.New("role authority unavailable")
	}
	u.Role = access.role(u.ID)
	if u.Role != roleAdmin && !h.allowRegistration() {
		return s, u, ErrNotFound
	}
	return s, u, nil
}
func (h *Handler) rights(u User) (read, admin bool) {
	return u.Enabled, u.Enabled && u.Role == roleAdmin
}

// No directory inference: only a string in a verified ID Token is stored.
// Missing/object/array department claims remain unknown rather than invented.
func verifiedDepartment(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	value = strings.TrimSpace(value)
	if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}
func (h *Handler) csrf(r *http.Request, s Session) bool {
	return r.Header.Get("Origin") == h.config.Origin && r.Header.Get("X-CSRF-Token") != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF)) == 1
}
func (h *Handler) authError(w http.ResponseWriter, e error) {
	if errors.Is(e, ErrNotFound) {
		fail(w, 401, "AUTHENTICATION_REQUIRED")
	} else {
		fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
	}
}
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	s, u, e := h.session(r)
	if e != nil {
		h.authError(w, e)
		return
	}
	read, admin := h.rights(u)
	respond(w, 200, map[string]any{"user": u, "permissions": map[string]bool{"read": read, "manage": admin}, "csrf_token": s.CSRF, "expires_at": s.Expires})
}
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	s, _, e := h.session(r)
	if e != nil {
		// A stale/disabled session can still be revoked with a same-origin request.
		if !errors.Is(e, ErrNotFound) {
			h.authError(w, e)
			return
		}
		if r.Header.Get("Origin") != h.config.Origin {
			fail(w, 403, "CSRF_REQUIRED")
			return
		}
	} else if !h.csrf(r, s) {
		fail(w, 403, "CSRF_REQUIRED")
		return
	}
	if c, e := r.Cookie(sessionCookie); e == nil {
		if e = h.store.DeleteSession(r.Context(), digest(c.Value)); e != nil {
			fail(w, 503, "AUTH_STORAGE_UNAVAILABLE")
			return
		}
	}
	h.cookie(w, sessionCookie, "", time.Time{})
	h.cookie(w, flowCookie, "", time.Time{})
	respond(w, 200, map[string]bool{"logged_out": true})
}

// Authorize is used only when no Authorization header is present.
func (h *Handler) Authorize(w http.ResponseWriter, r *http.Request, management bool) (*http.Request, bool) {
	s, u, e := h.session(r)
	if e != nil {
		h.authError(w, e)
		return r, false
	}
	read, admin := h.rights(u)
	if !read || (management && !admin) {
		fail(w, 403, "ACCESS_DENIED")
		return r, false
	}
	if r.Method != "GET" && r.Method != "HEAD" && !h.csrf(r, s) {
		fail(w, 403, "CSRF_REQUIRED")
		return r, false
	}
	return r.WithContext(identity.WithActor(r.Context(), "sso:"+u.ID)), true
}

// HasSession reports a browser credential without treating it as authenticated.
func (h *Handler) HasSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	return err == nil && cookie.Value != ""
}
