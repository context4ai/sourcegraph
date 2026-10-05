package httpserver

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/config"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/service"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type pluginRepoStore struct {
	service.Store
	public bool
}

func (s *pluginRepoStore) Get(context.Context, string) (control.Repository, error) {
	return control.Repository{Name: "org/repo", PublicRead: &s.public, Enabled: true, Policy: control.Policy{Branches: []string{"main"}}, Heads: map[string]string{"main": strings.Repeat("a", 40)}, Versions: []control.Version{{Commit: strings.Repeat("a", 40)}}}, nil
}
func TestPluginDiscoveryRouteAndVisibility(t *testing.T) {
	store := &pluginRepoStore{public: true}
	s, e := service.New(store, t.TempDir(), "", "")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := Runtime(config.Defaults(), slog.New(slog.NewTextHandler(io.Discard, nil)), s, "")
	for _, tc := range []struct {
		query  string
		public bool
		status int
	}{{"&revision=main", true, 200}, {"&revision=main&revision=main", true, 400}, {"&revision=main", false, 404}} {
		store.public = tc.public
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/sourcegraph/v1/repo/plugins?repo=org/repo"+tc.query, nil))
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.query, w.Code, w.Body.String())
		}
	}
}
