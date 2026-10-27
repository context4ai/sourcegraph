package sso

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"golang.org/x/oauth2"
)

func TestLoginStartsOnTheSiteAddress(t *testing.T) {
	db, err := sqlstore.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{config: Config{Enabled: true, Origin: "https://context4ai.org"}, store: &SQLStore{DB: db}, Provider: "github",
		oauth: oauth2.Config{ClientID: "id", RedirectURL: "https://context4ai.org/sourcegraph/auth/github/callback", Endpoint: oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize"}}}
	start := func(host, forwarded string) (int, string, string) {
		r := httptest.NewRequest("GET", "http://"+host+"/sourcegraph/auth/login?return_to=%2Fsourcegraph%2Frepos", nil)
		r.Host = host
		if forwarded != "" {
			r.Header.Set("X-Forwarded-Host", forwarded)
		}
		w := httptest.NewRecorder()
		h.login(w, r)
		return w.Code, w.Header().Get("Location"), w.Header().Get("Set-Cookie")
	}
	// The Fly hostname and aliases send the browser to the sign-in page on the site.
	for _, host := range [][2]string{{"context4ai-sourcegraph.fly.dev", ""}, {"context4ai-sourcegraph.internal:8080", "www.context4ai.org"}} {
		code, location, cookie := start(host[0], host[1])
		if code != 302 || location != "https://context4ai.org/sourcegraph/login?return_to=%2Fsourcegraph%2Frepos" || cookie != "" {
			t.Fatalf("%v: %d %s %q", host, code, location, cookie)
		}
	}
	// Through the site's proxy (and on the site itself) the login starts at the provider.
	for _, host := range [][2]string{{"context4ai-sourcegraph.internal:8080", "context4ai.org"}, {"context4ai.org", ""}} {
		code, location, cookie := start(host[0], host[1])
		if code != 302 || !strings.HasPrefix(location, "https://github.com/login/oauth/authorize") || !strings.Contains(cookie, flowCookie+"=") {
			t.Fatalf("%v: %d %s %q", host, code, location, cookie)
		}
	}
}
