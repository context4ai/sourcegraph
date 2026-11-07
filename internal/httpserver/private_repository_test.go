package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"github.com/context4ai/sourcegraph/internal/sso"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport string

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

// personalToken stores an API key for user and returns the raw token.
func (s *site) personalToken(user string) string {
	raw := "sgk_" + hexID("key-" + user)[:43]
	h := sha256.Sum256([]byte(raw))
	s.people.mu.Lock()
	defer s.people.mu.Unlock()
	if s.people.keys == nil {
		s.people.keys = map[string]sso.APIKey{}
	}
	s.people.keys[hex.EncodeToString(h[:])] = sso.APIKey{ID: "k-" + user[:8], UserID: user, Hash: hex.EncodeToString(h[:]), Created: time.Now()}
	return raw
}

// mcpSees reports whether the repositories and availability tools expose repo.
func mcpSees(t *testing.T, endpoint, token, repo string) (listed, available bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint}
	if token != "" {
		transport.HTTPClient = &http.Client{Transport: bearerTransport(token)}
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "visibility-test", Version: "1"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "repositories", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("repositories: %v %v", err, res)
	}
	body, _ := json.Marshal(res)
	listed = strings.Contains(string(body), repo)
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "availability", Arguments: map[string]any{"repo": repo}})
	if err != nil {
		t.Fatal(err)
	}
	return listed, !res.IsError
}

func TestPrivateRepositoryVisibility(t *testing.T) {
	admin, alice, bob, carol := hexID("admin"), hexID("alice"), hexID("bob"), hexID("carol")
	s := newSite(t, map[string]string{"admin": admin, "alice": alice, "bob": bob, "carol": carol}, admin)
	rev := s.settings.Snapshot().Access.Revision
	if _, err := s.settings.Update(context.Background(), sitesettings.AccessName, rev, []byte(`{"public_read":true,"allow_registration":true,"allow_user_repositories":true}`), "sso:admin"); err != nil {
		t.Fatal(err)
	}
	private := `{"name":"context4ai/private","policy":{"branches":["main"],"count":10},"code_provider":"github","public_read":false}`
	if code, body := s.call(alice, "POST", "/sourcegraph/api/admin/v1/repos", private, "Idempotency-Key", "register-private"); code != 202 {
		t.Fatalf("register: %d %v", code, body)
	}
	// A taken name answers 409 with its registrant, so others can ask to be added as managers.
	code, conflict := s.call(bob, "POST", "/sourcegraph/api/admin/v1/repos", private, "Idempotency-Key", "register-bob")
	creator, _ := conflict["creator"].(map[string]any)
	if code != 409 || errorCode(conflict) != "REPOSITORY_EXISTS" || creator["name"] != "alice" || creator["email"] != "alice@example.com" {
		t.Fatalf("taken name: %d %v", code, conflict)
	}
	s.db.mu.Lock()
	r := s.db.repos["context4ai/private"]
	r.ManagersSet, r.Managers = true, []string{alice, carol}
	s.db.repos[r.Name] = r
	s.db.mu.Unlock()
	// Registered before per-repository visibility: no public_read, still public.
	legacy := "context4ai/legacy"
	if err := s.db.Create(context.Background(), control.Repository{ID: control.ID(legacy), Name: legacy, Enabled: true, Revision: 1, Heads: map[string]string{}, Versions: []control.Version{}}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(s.handler)
	defer server.Close()
	endpoint := server.URL + "/sourcegraph/mcp"
	catalog := func(cookie, bearer string) string {
		r := httptest.NewRequest("GET", "/sourcegraph/v1/repos", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "sourcegraph_session", Value: "cookie-" + cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("catalog: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	availability := "/sourcegraph/v1/repo/availability?repo=context4ai%2Fprivate"
	service := strings.Repeat("t", 32)

	// Website: the creator and site administrators see it, other users and anonymous visitors do not.
	for _, c := range []struct {
		name, user string
		want       bool
	}{{"creator", alice, true}, {"manager", carol, true}, {"administrator", admin, true}, {"other user", bob, false}, {"anonymous", "", false}} {
		code, _ := s.call(c.user, "GET", availability, "")
		listed := strings.Contains(catalog(c.user, ""), "context4ai/private")
		if (code == 200) != c.want || listed != c.want {
			t.Errorf("website %s: availability %d, listed %v, want %v", c.name, code, listed, c.want)
		}
		if !c.want && code != 404 {
			t.Errorf("website %s: hidden repository must answer 404, got %d", c.name, code)
		}
		if !strings.Contains(catalog(c.user, ""), legacy) {
			t.Errorf("website %s: legacy repository hidden", c.name)
		}
	}

	// MCP and HTTP: a personal token reads as its owner, an administrator's included.
	for _, c := range []struct {
		name, token string
		want        bool
	}{{"anonymous", "", false}, {"creator token", s.personalToken(alice), true}, {"other user token", s.personalToken(bob), false}, {"administrator token", s.personalToken(admin), false}, {"service credential", service, true}} {
		listed, available := mcpSees(t, endpoint, c.token, "context4ai/private")
		if listed != c.want || available != c.want {
			t.Errorf("MCP %s: listed %v, available %v, want %v", c.name, listed, available, c.want)
		}
		if got := strings.Contains(catalog("", c.token), "context4ai/private"); got != c.want {
			t.Errorf("HTTP %s: listed %v, want %v", c.name, got, c.want)
		}
		if listed, available := mcpSees(t, endpoint, c.token, legacy); !listed || !available {
			t.Errorf("MCP %s: legacy repository hidden", c.name)
		}
	}

	// Every repository route answers a hidden repository exactly like an unregistered name.
	send := func(cookie, bearer, method, path, body string) string {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "sourcegraph_session", Value: "cookie-" + cookie})
			r.Header.Set("Origin", testOrigin)
			r.Header.Set("X-CSRF-Token", "csrf-"+cookie)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		r.Header.Set("Idempotency-Key", "visibility-probe")
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return http.StatusText(w.Code) + " " + errorCode(out)
	}
	routes := []struct{ method, path, body string }{
		{"POST", "/sourcegraph/api/search", `{"Pattern":"x"}`},
		{"POST", "/sourcegraph/v1/repo/read", `{"Path":"README.md"}`},
		{"POST", "/sourcegraph/v1/repo/list", `{}`},
		{"POST", "/sourcegraph/v1/repo/diff", `{"Base":"main","Head":"main"}`},
		{"POST", "/sourcegraph/v1/repo/resolve", `{}`},
		{"GET", "/sourcegraph/v1/repo/plugins", ""},
		{"GET", "/sourcegraph/v1/repo/commits", ""},
		{"POST", "/sourcegraph/api/admin/v1/repo/prepare", `{"kind":"sync"}`},
	}
	for _, c := range []struct {
		name, cookie, bearer string
		want                 bool
	}{
		{"creator session", alice, "", true}, {"manager session", carol, "", true}, {"administrator session", admin, "", true},
		{"creator token", "", s.personalToken(alice), true}, {"manager token", "", s.personalToken(carol), true}, {"service credential", "", service, true},
		{"other user session", bob, "", false}, {"other user token", "", s.personalToken(bob), false}, {"administrator token", "", s.personalToken(admin), false}, {"anonymous", "", "", false},
	} {
		for _, route := range routes {
			private := send(c.cookie, c.bearer, route.method, route.path+"?repo=context4ai%2Fprivate", route.body)
			missing := send(c.cookie, c.bearer, route.method, route.path+"?repo=context4ai%2Fmissing", route.body)
			if c.want && strings.HasSuffix(private, "REPOSITORY_NOT_FOUND") {
				t.Errorf("%s %s: visible repository answered %s", c.name, route.path, private)
			}
			if !c.want && private != missing {
				t.Errorf("%s %s: hidden repository answered %q, unregistered name %q", c.name, route.path, private, missing)
			}
		}
	}
}
