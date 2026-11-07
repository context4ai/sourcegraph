package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
)

func visibilityFixture() *Service {
	private, public := false, true
	return &Service{DB: &collectionStore{repos: map[string]control.Repository{
		"org/private": {Name: "org/private", Enabled: true, CreatorID: "alice", PublicRead: &private},
		"org/managed": {Name: "org/managed", Enabled: true, CreatorID: "alice", ManagersSet: true, Managers: []string{"carol"}, PublicRead: &private},
		"org/public":  {Name: "org/public", Enabled: true, CreatorID: "alice", PublicRead: &public},
		"org/legacy":  {Name: "org/legacy", Enabled: true},
	}}}
}

func TestPrivateRepositoriesAreVisibleToCreatorManagersAndAdministrators(t *testing.T) {
	s := visibilityFixture()
	base := context.Background()
	all := []string{"org/legacy", "org/managed", "org/private", "org/public"}
	public := []string{"org/legacy", "org/public"}
	cases := []struct {
		name string
		ctx  context.Context
		want []string
	}{
		{"anonymous", identity.WithAnonymous(base, "anon"), public},
		{"other user session", identity.WithActor(base, "sso:bob"), public},
		{"other user token", identity.WithActor(base, "api-key:bob:k"), public},
		{"creator session", identity.WithActor(base, "sso:alice"), all},
		{"creator token", identity.WithActor(base, "api-key:alice:k"), all},
		{"manager token", identity.WithActor(base, "api-key:carol:k"), []string{"org/legacy", "org/managed", "org/public"}},
		{"administrator session", identity.WithAdmin(identity.WithActor(base, "sso:admin")), all},
		{"administrator token", identity.WithTokenOwnerAdmin(identity.WithActor(base, "api-key:admin:k")), public},
		{"service credential", identity.WithAdmin(base), all},
		{"background work", base, all},
	}
	for _, c := range cases {
		want := map[string]bool{}
		for _, name := range c.want {
			want[name] = true
		}
		rows, err := s.catalogRows(c.ctx, "")
		if err != nil || len(rows) != len(c.want) {
			t.Errorf("%s catalog: %d rows %v, want %v", c.name, len(rows), err, c.want)
		}
		for _, r := range rows {
			if !want[r.Name] {
				t.Errorf("%s catalog lists %s", c.name, r.Name)
			}
		}
		for _, name := range all {
			_, err := s.Repo(c.ctx, name)
			var ce *contract.Error
			if want[name] != (err == nil) {
				t.Errorf("%s repo %s: %v", c.name, name, err)
			} else if err != nil && (!errors.As(err, &ce) || ce.Code != "REPOSITORY_NOT_FOUND" || ce.Message != "Repository is not registered.") {
				t.Errorf("%s repo %s: hidden repository must look unregistered, got %v", c.name, name, err)
			}
		}
	}
}

func TestAdministratorTokenPreparesOnlyVisibleRepositories(t *testing.T) {
	s := visibilityFixture()
	base := context.Background()
	token := identity.WithTokenOwnerAdmin(identity.WithActor(base, "api-key:admin:k"))
	if err := s.AuthorizePrepare(token, "org/public"); err != nil {
		t.Fatalf("public repository: %v", err)
	}
	var ce *contract.Error
	if err := s.AuthorizePrepare(token, "org/private"); !errors.As(err, &ce) || ce.Code != "REPOSITORY_NOT_FOUND" {
		t.Fatalf("private repository: %v", err)
	}
	own := identity.WithTokenOwnerAdmin(identity.WithActor(base, "api-key:alice:k"))
	if err := s.AuthorizePrepare(own, "org/private"); err != nil {
		t.Fatalf("administrator's own repository: %v", err)
	}
	if err := s.AuthorizePrepare(identity.WithAdmin(identity.WithActor(base, "sso:admin")), "org/private"); err != nil {
		t.Fatalf("administrator session: %v", err)
	}
	if err := s.AuthorizePrepare(identity.WithActor(base, "api-key:bob:k"), "org/private"); !errors.As(err, &ce) || ce.Code != "REPOSITORY_NOT_FOUND" {
		t.Fatalf("other user's token: %v", err)
	}
}

func TestStatisticsOmitHiddenRepositoryNames(t *testing.T) {
	s := visibilityFixture()
	minute := time.Now().Unix() / 60 * 60
	s.metrics.buckets = map[int64]*Bucket{minute: {Minute: minute, ByOperation: map[string]Metric{"read": {Requests: 3}}, ByRepo: map[string]Metric{"org/private": {Requests: 1}, "org/public": {Requests: 1}, "org/guessed": {Requests: 1}, "_overflow": {Requests: 1}}}}
	names := func(ctx context.Context) map[string]bool {
		out := map[string]bool{}
		for _, b := range s.StatisticsFor(ctx).(map[string]any)["buckets"].([]Bucket) {
			for name := range b.ByRepo {
				out[name] = true
			}
		}
		return out
	}
	base := context.Background()
	if got := names(identity.WithAnonymous(base, "anon")); len(got) != 0 {
		t.Fatalf("anonymous: %v", got)
	}
	// A guessed unregistered name and a hidden private one look the same.
	if got := names(identity.WithActor(base, "sso:bob")); got["org/private"] || got["org/guessed"] || !got["org/public"] || !got["_overflow"] {
		t.Fatalf("other user: %v", got)
	}
	if got := names(identity.WithActor(base, "sso:alice")); !got["org/private"] {
		t.Fatalf("creator: %v", got)
	}
	if got := names(identity.WithAdmin(base)); !got["org/private"] || !got["org/guessed"] {
		t.Fatalf("administrator: %v", got)
	}
}
