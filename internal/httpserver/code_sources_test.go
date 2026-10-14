package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/codesources"
	"github.com/context4ai/sourcegraph/internal/config"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
)

type sourceRepoStore struct{ service.Store }

func (sourceRepoStore) List(_ context.Context, after string, _ int) ([]control.Repository, error) {
	if after != "" {
		return nil, nil
	}
	return []control.Repository{{ID: "1", Name: "group/web", CodeProvider: "git", CodeHost: "https://git.example.com"}}, nil
}

func TestCodeSourceSaveRejectsMovingAHostInUse(t *testing.T) {
	s, err := service.New(sourceRepoStore{}, t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	settings, err := sitesettings.New(context.Background(), sitesettings.NewMemoryStore(), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = settings.Update(context.Background(), sitesettings.CodeSourceName, 1, []byte(`{"name":"","host":"https://git.example.com","username":""}`), "admin"); err != nil {
		t.Fatal(err)
	}
	codesources.UseSettings(settings.CodeSourceOverride)
	t.Cleanup(func() { codesources.UseSettings(func() codesources.Override { return codesources.Override{} }) })
	w := httptest.NewRecorder()
	body := `{"revision":2,"values":{"name":"","host":"https://other.example.com","username":""}}`
	codeSourceSettings(w, httptest.NewRequest("PATCH", sitesettings.AdminPath+"/code_source", strings.NewReader(body)), s, settings)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "CODE_SOURCE_IN_USE") {
		t.Fatalf("moving a host in use: %d %s", w.Code, w.Body.String())
	}

	h := Runtime(config.Defaults(), slog.New(slog.NewTextHandler(io.Discard, nil)), s, "")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sourcegraph/v1/code-sources", nil))
	var got []codesources.Source
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got) != 2 || got[0].ID != "github" {
		t.Fatalf("code sources: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "token_env") {
		t.Fatal("token variable names are not part of the public response")
	}
}
