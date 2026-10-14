package service

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
)

type sourceStore struct {
	Store
	repos map[string]control.Repository
}

func (db *sourceStore) Get(_ context.Context, name string) (control.Repository, error) {
	if r, ok := db.repos[name]; ok {
		return r, nil
	}
	return control.Repository{}, contract.Fail("REPOSITORY_NOT_FOUND", "missing", 404)
}
func (db *sourceStore) Create(_ context.Context, r control.Repository) error {
	db.repos[r.Name] = r
	return nil
}
func (db *sourceStore) Replace(_ context.Context, r control.Repository, _ int64) error {
	db.repos[r.Name] = r
	return nil
}
func (db *sourceStore) List(_ context.Context, after string, limit int) ([]control.Repository, error) {
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

func TestRegistrationPinsTheSourceHost(t *testing.T) {
	db := &sourceStore{repos: map[string]control.Repository{}}
	s := &Service{DB: db, Root: t.TempDir()}
	policy := control.Policy{Branches: []string{"main"}, Count: 10}
	r, err := s.RegisterWithProvider(context.Background(), "context4ai/agent-graph", policy, 60, "full", nil, nil, "github", "register-github")
	if err != nil || r.CodeHost != "https://github.com" || db.repos["context4ai/agent-graph"].CodeHost != "https://github.com" {
		t.Fatalf("registered = %#v, %v", r, err)
	}
	for provider, code := range map[string]string{"git": "CODE_SOURCE_NOT_CONFIGURED", "gitlab": "INVALID_CODE_PROVIDER"} {
		_, err = s.RegisterWithProvider(context.Background(), "group/"+provider, policy, 60, "full", nil, nil, provider, "register-"+provider)
		var ce *contract.Error
		if !errors.As(err, &ce) || ce.Code != code {
			t.Fatalf("%s: %v", provider, err)
		}
	}
}

func TestPinCodeHostsKeepsExistingRepositoriesOnTheirHost(t *testing.T) {
	db := &sourceStore{repos: map[string]control.Repository{
		"group/old":   {ID: "1", Name: "group/old", Revision: 1},
		"group/moved": {ID: "2", Name: "group/moved", CodeProvider: "git", CodeHost: "https://old.example.com", Revision: 1},
	}}
	s := &Service{DB: db, Root: t.TempDir()}
	s.PinCodeHosts(context.Background(), nil)
	if db.repos["group/old"].CodeHost != "https://github.com" {
		t.Fatalf("unpinned repository = %#v", db.repos["group/old"])
	}
	if db.repos["group/moved"].CodeHost != "https://old.example.com" {
		t.Fatal("a pinned host must never be rewritten")
	}
	if o := s.fetchOptions(db.repos["group/moved"], ""); o.Host != "https://old.example.com" || o.Anonymous {
		t.Fatalf("fetch options = %#v", o)
	}
	if o := s.fetchOptions(db.repos["group/old"], ""); !o.Anonymous {
		t.Fatal("github repositories may fetch anonymously")
	}
	if used, err := s.CodeSourceInUse(context.Background(), "git"); err != nil || !used {
		t.Fatalf("git in use = %v, %v", used, err)
	}
}
