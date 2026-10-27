package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/config"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"github.com/context4ai/sourcegraph/internal/sso"
)

const testOrigin = "https://site.test"

func hexID(seed string) string { h := sha256.Sum256([]byte(seed)); return hex.EncodeToString(h[:]) }

// people is an in-memory sso.Store holding users, sessions and the admin set.
type people struct {
	mu       sync.Mutex
	users    map[string]sso.User
	sessions map[string]sso.Session
	access   sso.AccessControl
}

func (p *people) GetAccess(context.Context) (sso.AccessControl, error) { return p.access, nil }
func (p *people) CreateAccess(context.Context, sso.AccessControl) (bool, error) {
	return false, nil
}
func (p *people) ReplaceAccess(context.Context, int64, sso.AccessControl) (bool, error) {
	return false, nil
}
func (p *people) ListUsers(_ context.Context, q sso.UserQuery, _ []string) ([]sso.User, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []sso.User{}
	for _, u := range p.users {
		if strings.Contains(strings.ToLower(u.Name+" "+u.Email), strings.ToLower(q.Q)) {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (p *people) PutFlow(context.Context, sso.Flow) error { return nil }
func (p *people) ConsumeFlow(context.Context, string, string, time.Time) (sso.Flow, error) {
	return sso.Flow{}, sso.ErrNotFound
}
func (p *people) UpsertUser(_ context.Context, u sso.User) (sso.User, error) { return u, nil }
func (p *people) GetUser(_ context.Context, id string) (sso.User, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u, ok := p.users[id]; ok {
		return u, nil
	}
	return sso.User{}, sso.ErrNotFound
}
func (p *people) PutSession(context.Context, sso.Session) error { return nil }
func (p *people) GetSession(_ context.Context, id string) (sso.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.sessions[id]; ok {
		return s, nil
	}
	return sso.Session{}, sso.ErrNotFound
}
func (p *people) DeleteSession(context.Context, string) error { return nil }

// repositories is an in-memory service.Store.
type repositories struct {
	mu    sync.Mutex
	repos map[string]control.Repository
}

func (db *repositories) Get(_ context.Context, name string) (control.Repository, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if r, ok := db.repos[name]; ok {
		return r, nil
	}
	return control.Repository{}, contract.Fail("REPOSITORY_NOT_FOUND", "Repository is not registered.", 404)
}
func (db *repositories) List(_ context.Context, after string, limit int) ([]control.Repository, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	out := []control.Repository{}
	for _, r := range db.repos {
		if r.ID > after {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (db *repositories) Create(_ context.Context, r control.Repository) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.repos[r.Name] = r
	return nil
}
func (db *repositories) Replace(_ context.Context, r control.Repository, _ int64) error {
	return db.Create(context.Background(), r)
}
func (db *repositories) ReplaceLease(_ context.Context, r control.Repository, _ int64) error {
	return db.Create(context.Background(), r)
}
func (db *repositories) NextGeneration(context.Context) (uint32, error) { return 1, nil }

type site struct {
	t        *testing.T
	handler  http.Handler
	settings *sitesettings.Service
	db       *repositories
}

// fakeIssuer serves just enough OIDC discovery for sso.New; no login runs.
func fakeIssuer(t *testing.T) {
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"HS256"}})
	}))
	issuer = server.URL
	previous := sso.Issuer
	sso.Issuer = issuer
	t.Cleanup(func() { sso.Issuer = previous; server.Close() })
}

func newSite(t *testing.T, users map[string]string, admins ...string) *site {
	fakeIssuer(t)
	store := &people{users: map[string]sso.User{}, sessions: map[string]sso.Session{}, access: sso.AccessControl{ID: "website", Revision: 1, AdminIDs: admins}}
	for name, id := range users {
		store.users[id] = sso.User{ID: id, Name: name, Email: name + "@example.com", Department: "Team", Enabled: true}
		store.sessions[hexID("cookie-"+id)] = sso.Session{ID: hexID("cookie-" + id), UserID: id, CSRF: "csrf-" + id, Expires: time.Now().Add(time.Hour)}
	}
	auth, err := sso.New(context.Background(), sso.Config{Enabled: true, Origin: testOrigin, ClientID: "client", SigningAlgorithm: "HS256"}, "secret", store)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := sitesettings.New(context.Background(), sitesettings.NewMemoryStore(), false)
	if err != nil {
		t.Fatal(err)
	}
	// Community sites admit non-administrators only while other users may sign in.
	auth.Settings = settings
	if _, err = settings.Update(context.Background(), sitesettings.AccessName, settings.Snapshot().Access.Revision, []byte(`{"public_read":false,"allow_registration":true}`), "sso:admin"); err != nil {
		t.Fatal(err)
	}
	db := &repositories{repos: map[string]control.Repository{}}
	s, err := service.New(db, t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &site{t: t, handler: RuntimeWithSettings(config.Defaults(), slog.New(slog.NewTextHandler(io.Discard, nil)), s, strings.Repeat("t", 32), settings, auth), settings: settings, db: db}
}

// call sends a browser request as user (empty for anonymous) and decodes the JSON body.
func (s *site) call(user, method, path, body string, headers ...string) (int, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		r.AddCookie(&http.Cookie{Name: "sourcegraph_session", Value: "cookie-" + user})
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("X-CSRF-Token", "csrf-"+user)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func errorCode(body map[string]any) string {
	if meta, ok := body["Meta"].(map[string]any); ok {
		code, _ := meta["Code"].(string)
		return code
	}
	code, _ := body["error"].(string)
	return code
}

func (s *site) allowUsers(on bool) {
	rev := s.settings.Snapshot().Access.Revision
	values, _ := json.Marshal(map[string]bool{"public_read": s.settings.PublicRead(), "allow_registration": true, "allow_user_repositories": on})
	if _, err := s.settings.Update(context.Background(), sitesettings.AccessName, rev, values, "sso:admin"); err != nil {
		s.t.Fatal(err)
	}
}

const register = `{"name":"context4ai/agent-graph","policy":{"branches":["main"],"count":10},"code_provider":"github"}`

func TestOrdinaryUsersRegisterAndManageTheirRepositories(t *testing.T) {
	admin, alice, bob := hexID("admin"), hexID("alice"), hexID("bob")
	s := newSite(t, map[string]string{"admin": admin, "alice": alice, "bob": bob}, admin)
	repo := "/sourcegraph/api/admin/v1/repo"
	q := "?repo=context4ai%2Fagent-graph"

	// Off by default: only administrators and the service credential register.
	if code, body := s.call(alice, "POST", "/sourcegraph/api/admin/v1/repos", register, "Idempotency-Key", "register-k1"); code != 403 || errorCode(body) != "REGISTRATION_DISABLED" {
		t.Fatalf("registration while off: %d %v", code, body)
	}
	if _, policy := s.call("", "GET", "/sourcegraph/access-policy", ""); policy["user_repositories"] != false {
		t.Fatalf("access policy while off: %v", policy)
	}

	s.allowUsers(true)
	if _, policy := s.call("", "GET", "/sourcegraph/access-policy", ""); policy["user_repositories"] != true {
		t.Fatalf("access policy while on: %v", policy)
	}
	if code, body := s.call(alice, "POST", "/sourcegraph/api/admin/v1/repos", register, "Idempotency-Key", "register-k1"); code != 202 || body["created_by"] != alice {
		t.Fatalf("user registration: %d %v", code, body)
	}

	// The creator sees themselves as creator and manager; others see the same people but cannot manage.
	code, view := s.call(alice, "GET", "/sourcegraph/v1/repo/availability"+q, "")
	creator, _ := view["creator"].(map[string]any)
	if code != 200 || view["can_manage"] != true || creator["name"] != "alice" || len(view["managers"].([]any)) != 1 {
		t.Fatalf("creator view: %d %v", code, view)
	}
	if _, view = s.call(bob, "GET", repo+q, ""); view["can_manage"] != false || view["creator"] == nil {
		t.Fatalf("other user view: %v", view)
	}
	// With public read on, anonymous callers see the repository but not who manages it.
	rev0 := s.settings.Snapshot().Access.Revision
	if _, err := s.settings.Update(context.Background(), sitesettings.AccessName, rev0, []byte(`{"public_read":true,"allow_registration":true,"allow_user_repositories":true}`), "sso:admin"); err != nil {
		t.Fatal(err)
	}
	if code, view := s.call("", "GET", "/sourcegraph/v1/repo/availability"+q, ""); code != 200 || view["creator"] != nil || view["managers"] != nil || view["can_manage"] != false {
		t.Fatalf("anonymous view: %d %v", code, view)
	}

	// Repository-scoped management: bob is refused, alice and the admin pass the gate.
	cancel := `{"job_id":"missing"}`
	if code, body := s.call(bob, "POST", repo+"/jobs/cancel"+q, cancel); code != 403 || errorCode(body) != "REPOSITORY_MANAGEMENT_DENIED" {
		t.Fatalf("non-manager cancel: %d %v", code, body)
	}
	for _, who := range []string{alice, admin} {
		if code, body := s.call(who, "POST", repo+"/jobs/cancel"+q, cancel); code == 403 {
			t.Fatalf("manager gate refused %s: %v", who, body)
		}
	}
	// Site-wide administration stays admin-only.
	if code, _ := s.call(alice, "GET", "/sourcegraph/api/admin/v1/instances", ""); code != 403 {
		t.Fatalf("instances for a repository manager: %d", code)
	}

	// Managers add and remove managers, including themselves.
	rev := func() string {
		r, _ := s.db.Get(context.Background(), "context4ai/agent-graph")
		return `"` + itoa(r.Revision) + `"`
	}
	if code, body := s.call(alice, "POST", repo+"/managers"+q, `{"add":"`+hexID("nobody")+`"}`, "If-Match", rev()); code != 404 || errorCode(body) != "USER_NOT_FOUND" {
		t.Fatalf("unknown manager: %d %v", code, body)
	}
	if code, body := s.call(bob, "POST", repo+"/managers"+q, `{"add":"`+bob+`"}`, "If-Match", rev()); code != 403 {
		t.Fatalf("non-manager added themselves: %d %v", code, body)
	}
	if code, body := s.call(alice, "POST", repo+"/managers"+q, `{"add":"`+bob+`"}`, "If-Match", rev()); code != 200 || len(body["managers"].([]any)) != 2 {
		t.Fatalf("add manager: %d %v", code, body)
	}
	if code, body := s.call(bob, "POST", repo+"/managers"+q, `{"remove":"`+alice+`"}`, "If-Match", rev()); code != 200 || len(body["managers"].([]any)) != 1 {
		t.Fatalf("manager removes the creator: %d %v", code, body)
	}
	if code, _ := s.call(alice, "POST", repo+"/jobs/cancel"+q, cancel); code != 403 {
		t.Fatalf("a removed creator still manages: %d", code)
	}
	if code, body := s.call(bob, "POST", repo+"/managers"+q, `{"remove":"`+bob+`"}`, "If-Match", rev()); code != 200 || body["can_manage"] != false {
		t.Fatalf("leave: %d %v", code, body)
	}
	if code, body := s.call(admin, "POST", repo+"/managers"+q, `{"add":"`+alice+`"}`, "If-Match", rev()); code != 200 {
		t.Fatalf("admin restores a manager: %d %v", code, body)
	}

	// Turning the setting off stops new registrations; existing managers keep their repositories.
	s.allowUsers(false)
	other := strings.Replace(register, "agent-graph", "context", 1)
	if code, _ := s.call(alice, "POST", "/sourcegraph/api/admin/v1/repos", other, "Idempotency-Key", "register-k2"); code != 403 {
		t.Fatalf("registration after turning off: %d", code)
	}
	if code, _ := s.call(alice, "POST", repo+"/jobs/cancel"+q, cancel); code == 403 {
		t.Fatal("existing managers keep managing after the setting is turned off")
	}
	if code, _ := s.call(admin, "POST", "/sourcegraph/api/admin/v1/repos", other, "Idempotency-Key", "register-k3"); code != 202 {
		t.Fatalf("admin registration: %d", code)
	}

	// Any signed-in user can search people to choose a manager; anonymous callers cannot.
	if code, body := s.call(bob, "GET", "/sourcegraph/auth/users/search?q=ali", ""); code != 200 || len(body["items"].([]any)) != 1 {
		t.Fatalf("people search: %d %v", code, body)
	}
	if code, _ := s.call("", "GET", "/sourcegraph/auth/users/search?q=ali", ""); code == 200 {
		t.Fatal("anonymous people search allowed")
	}
}

func TestServiceCredentialRegistersWhileUserRegistrationIsOff(t *testing.T) {
	admin := hexID("admin")
	s := newSite(t, map[string]string{"admin": admin}, admin)
	r := httptest.NewRequest("POST", "/sourcegraph/api/admin/v1/repos", strings.NewReader(register))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
	r.Header.Set("Idempotency-Key", "register-svc")
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("service registration: %d %s", w.Code, w.Body.String())
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
