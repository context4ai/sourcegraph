package httpserver

import (
	"context"
	"encoding/json"
	"github.com/context4ai/sourcegraph/internal/config"
	"github.com/context4ai/sourcegraph/internal/querylog"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"github.com/context4ai/sourcegraph/internal/sso"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type logAuthStore struct {
	sso.Store
	admin bool
}

func (s *logAuthStore) GetAccess(context.Context) (sso.AccessControl, error) {
	return sso.AccessControl{ID: "website", Revision: 1, AdminIDs: []string{"admin"}}, nil
}
func (s *logAuthStore) GetSession(context.Context, string) (sso.Session, error) {
	return sso.Session{UserID: "current", CSRF: "fixture-csrf", Expires: time.Now().Add(time.Hour)}, nil
}
func (s *logAuthStore) GetUser(context.Context, string) (sso.User, error) {
	id := "ordinary"
	if s.admin {
		id = "admin"
	}
	return sso.User{ID: id, Enabled: true}, nil
}
func TestQueryLogAdminOnly(t *testing.T) {
	var origin string
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": origin, "authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token", "jwks_uri": origin + "/keys"})
	}))
	defer discovery.Close()
	origin = discovery.URL
	old := sso.Issuer
	sso.Issuer = origin
	defer func() { sso.Issuer = old }()
	store := &logAuthStore{}
	auth, err := sso.New(context.Background(), sso.Config{Enabled: true, Origin: origin, ClientID: "fixture", SigningAlgorithm: "RS256"}, "fixture-secret", store)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := sitesettings.New(context.Background(), sitesettings.NewMemoryStore(), true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = settings.Update(context.Background(), "access", 1, []byte(`{"public_read":true,"allow_registration":true}`), "admin")
	if err != nil {
		t.Fatal(err)
	}
	auth.Settings = settings
	logs := &querylog.FileStore{Path: filepath.Join(t.TempDir(), "logs.json")}
	if err := logs.Append(context.Background(), []querylog.Entry{{ID: "log1", Context: "private query", SuggestedQuery: "content:recovery", QueryFragment: "content:(original)", Code: "INVALID_QUERY", Time: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	s := &service.Service{QueryLogs: querylog.New(logs)}
	handler := Runtime(config.Config{RequestTimeoutSeconds: 10}, slog.New(slog.NewTextHandler(io.Discard, nil)), s, strings.Repeat("a", 32), auth)
	for _, test := range []struct {
		name, token   string
		cookie, admin bool
		want          int
	}{
		{"anonymous", "", false, false, 401}, {"ordinary user", "", true, false, 403}, {"service token", strings.Repeat("a", 32), false, false, 403}, {"personal token", "sgk_example", false, false, 403}, {"admin", "", true, true, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			store.admin = test.admin
			r := httptest.NewRequest("GET", "/sourcegraph/api/admin/settings/query-errors", nil)
			if test.cookie {
				r.AddCookie(&http.Cookie{Name: "sourcegraph_session", Value: "fixture"})
			}
			if test.token != "" {
				r.Header.Set("Authorization", "Bearer "+test.token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private query") || strings.Contains(w.Body.String(), "content:recovery") || strings.Contains(w.Body.String(), "content:(original)") {
				t.Fatal("list leaked detailed context")
			}
		})
	}
	store.admin = true
	for _, test := range []struct {
		query    string
		want     int
		contains string
	}{{"?id=log1", 200, "private query"}, {"?id=log1", 200, "content:recovery"}, {"?id=log1", 200, "content:(original)"}, {"?id=expired", 404, "ERROR_LOG_NOT_FOUND"}, {"?after=expired", 409, "ERROR_LOG_LIST_CHANGED"}} {
		r := httptest.NewRequest("GET", "/sourcegraph/api/admin/settings/query-errors"+test.query, nil)
		r.AddCookie(&http.Cookie{Name: "sourcegraph_session", Value: "fixture"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.want || !strings.Contains(w.Body.String(), test.contains) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		name                string
		admin, cookie, csrf bool
		token               string
		want                int
	}{
		{name: "anonymous delete", want: 401},
		{name: "ordinary delete", cookie: true, csrf: true, want: 403},
		{name: "token delete", token: strings.Repeat("a", 32), want: 403},
		{name: "admin without csrf", admin: true, cookie: true, want: 403},
		{name: "admin clear", admin: true, cookie: true, csrf: true, want: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.admin = tc.admin
			r := httptest.NewRequest("DELETE", "/sourcegraph/api/admin/settings/query-errors", nil)
			if tc.cookie {
				r.AddCookie(&http.Cookie{Name: "sourcegraph_session", Value: "fixture"})
			}
			if tc.csrf {
				r.Header.Set("Origin", origin)
				r.Header.Set("X-CSRF-Token", "fixture-csrf")
			}
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			rows, err := logs.Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if (len(rows) == 0) != (tc.want == 200) {
				t.Fatalf("clear result: %v", rows)
			}
		})
	}

}

func TestMCPFailurePersistsRequestContext(t *testing.T) {
	store := &querylog.FileStore{Path: filepath.Join(t.TempDir(), "errors.json")}
	s := &service.Service{QueryLogs: querylog.New(store)}
	c := config.Defaults()
	c.WebsitePublicRead = true
	h := Runtime(c, slog.New(slog.NewTextHandler(io.Discard, nil)), s, "")
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "log-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/sourcegraph/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"repo": "invalid", "pattern": "needle"}})
	if err != nil || !result.IsError {
		t.Fatalf("expected tool failure: %v %+v", err, result)
	}
	bg, stop := context.WithCancel(context.Background())
	stop()
	s.QueryLogs.Run(bg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rows, err := store.Read(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("missing failure: %d %v", len(rows), err)
	}
	if rows[0].RequestID == "" || rows[0].Transport != "mcp" || !strings.Contains(rows[0].Context, "needle") {
		t.Fatalf("missing context: %+v", rows[0])
	}
}
