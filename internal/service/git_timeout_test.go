package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/context4ai/sourcegraph/internal/control"
	"testing"
	"time"
)

func TestGitTimeoutInheritanceAndTaskSnapshot(t *testing.T) {
	s := &Service{}
	r := control.Repository{}
	if s.fetchOptions(r, "").Timeout != 15*time.Minute {
		t.Fatal("default timeout")
	}
	global := 30
	s.UseGitTimeoutDefault(func() int { return global })
	if s.fetchOptions(r, "").Timeout != 30*time.Minute {
		t.Fatal("inherit")
	}
	// Worker captures the resolved value in its private repository copy.
	captured := r
	captured.GitTimeoutMinutes = s.gitTimeoutMinutes(r)
	global = 60
	if s.fetchOptions(captured, "").Timeout != 30*time.Minute || s.fetchOptions(r, "").Timeout != 60*time.Minute {
		t.Fatal("task snapshot or live inheritance")
	}
	for _, n := range []int{15, 30, 45, 60} {
		r.GitTimeoutMinutes = n
		if s.fetchOptions(r, "").Timeout != time.Duration(n)*time.Minute {
			t.Fatalf("override %d", n)
		}
		if gitJobBudget(n, "sync") < s.fetchOptions(r, "").Timeout {
			t.Fatalf("outer budget clips %d", n)
		}
	}
}

func TestGitTimeoutUpdatePreservesWorkAndVersions(t *testing.T) {
	policy := control.Policy{Branches: []string{"main"}, Count: 10}
	db := &managementStore{repo: control.Repository{Name: "org/repo", Enabled: true, Revision: 4, PolicyRevision: 2, Policy: policy, Versions: []control.Version{{Commit: "1111111111111111111111111111111111111111", Generation: 3}}, Jobs: []control.Job{{State: "running", Kind: "sync"}}}}
	s := &Service{DB: db, Root: t.TempDir()}
	for _, n := range []int{30, 60, 0} {
		u := Update{Policy: policy, Enabled: true, GitTimeoutMinutes: &n}
		u.Preview = preview(db.repo, u)
		rev := db.repo.Revision
		_, err := s.Update(context.Background(), db.repo.Name, u, rev)
		if err != nil {
			t.Fatal(err)
		}
		if db.repo.GitTimeoutMinutes != n || db.repo.PolicyRevision != 2 || len(db.repo.Versions) != 1 || db.repo.Jobs[0].State != "running" {
			t.Fatal("timeout revoked data or work")
		}
		if _, err = s.Update(context.Background(), db.repo.Name, u, rev); err == nil {
			t.Fatal("stale CAS accepted")
		}
	}
	n := 60
	u := Update{Policy: policy, Enabled: true, GitTimeoutMinutes: &n}
	u.Preview = preview(db.repo, u)
	if _, err := s.Update(context.Background(), db.repo.Name, u, db.repo.Revision); err != nil {
		t.Fatal(err)
	}
	u = Update{Policy: policy, Enabled: true}
	u.Preview = preview(db.repo, u)
	if _, err := s.Update(context.Background(), db.repo.Name, u, db.repo.Revision); err != nil || db.repo.GitTimeoutMinutes != 60 {
		t.Fatal("omission must preserve")
	}
	for _, n := range []int{-1, 1, 16, 61, 90} {
		u.GitTimeoutMinutes = &n
		if _, err := s.Preview(context.Background(), db.repo.Name, u); err == nil {
			t.Fatalf("invalid %d", n)
		}
	}
	// Invalid timeout must not be hidden by a valid sync interval.
	interval, bad := 5, 61
	if validateUpdate(Update{Policy: policy, SyncIntervalMinutes: &interval, GitTimeoutMinutes: &bad}) == nil {
		t.Fatal("validation bypass")
	}
}

func TestWorkerUsesConfiguredGitBudget(t *testing.T) {
	for _, n := range []int{15, 30, 45, 60} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			now := time.Now().UTC()
			db := &managementStore{repo: control.Repository{Name: "org/repo", Enabled: true, Revision: 1, PolicyRevision: 1, Jobs: []control.Job{{ID: "job", Kind: "sync", State: "queued", Created: now, Updated: now}}}}
			job, ok := db.repo.Claim("test", now)
			if !ok {
				t.Fatal("claim")
			}
			s := &Service{DB: db, Root: t.TempDir()}
			global := n
			s.UseGitTimeoutDefault(func() int { return global })
			called := false
			s.executeClaimed(context.Background(), db.repo.Name, job, func(ctx context.Context, r control.Repository, j control.Job) error {
				called = true
				deadline, ok := ctx.Deadline()
				want := gitJobBudget(n, "sync")
				if !ok || time.Until(deadline) > want || time.Until(deadline) < want-time.Second {
					t.Fatalf("wrong budget: %v", time.Until(deadline))
				}
				global = 15
				if s.fetchOptions(r, "").Timeout != time.Duration(n)*time.Minute {
					t.Fatal("task timeout changed after start")
				}
				return errors.New("test preparation stopped")
			})
			if !called || db.repo.GitTimeoutMinutes != 0 || db.repo.Jobs[0].State != "failed" {
				t.Fatal("task snapshot persisted or worker did not finish")
			}
		})
	}
}

func TestGitTimeoutRegistrationIdempotency(t *testing.T) {
	db := &sourceStore{repos: map[string]control.Repository{}}
	s := &Service{DB: db, Root: t.TempDir()}
	policy := control.Policy{Branches: []string{"main"}, Count: 10}
	register := func(n int) (control.Repository, error) {
		return s.RegisterWithGitTimeout(context.Background(), "org/repo", policy, 5, "full", nil, nil, "github", n, "timeout-registration")
	}
	first, err := register(30)
	if err != nil || first.GitTimeoutMinutes != 30 {
		t.Fatalf("register: %v", err)
	}
	replay, err := register(30)
	if err != nil || replay.Jobs[0].ID != first.Jobs[0].ID {
		t.Fatalf("replay: %v", err)
	}
	if _, err = register(60); err == nil {
		t.Fatal("different timeout replay accepted")
	}
	if _, err = register(0); err == nil {
		t.Fatal("inherit replay accepted")
	}
	if _, err = register(61); err == nil {
		t.Fatal("invalid timeout accepted")
	}
}
