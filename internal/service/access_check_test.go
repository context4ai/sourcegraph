package service

import (
	"context"
	"os"
	"testing"

	"github.com/context4ai/sourcegraph/internal/codesources"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
)

func TestCheckAccessUsesRegistrationSourceAndLeavesNoState(t *testing.T) {
	s, _, _ := refService(t)
	if err := codesources.Configure(map[string]codesources.Override{defaultCodeProvider: {Host: "https://fixture.example"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = codesources.Configure(nil) })
	ctx := context.Background()
	if err := s.CheckAccess(ctx, "org/repo", ""); err != nil {
		t.Fatalf("readable repository: %v", err)
	}
	if err := s.CheckAccess(ctx, "org/missing", ""); err == nil {
		t.Fatal("missing repository passed")
	}
	if got := code(s.CheckAccess(ctx, "org/repo", "unknown")); got != "INVALID_CODE_PROVIDER" {
		t.Fatalf("unknown provider: %s", got)
	}
	if got := code(s.CheckAccess(ctx, "not a repository", "")); got == "" {
		t.Fatal("invalid name passed")
	}
	entries, err := os.ReadDir(refsRoot(s.Root))
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch directories left: %v %v", entries, err)
	}
	if len(s.refState.repoSlots) != 0 {
		t.Fatalf("per-name state kept: %v", s.refState.repoSlots)
	}
}

// reachable stands in for the remote probe in registration tests.
func reachable(context.Context, string, string) error { return nil }

func TestRegisterChecksAccessOnlyForNewNames(t *testing.T) {
	db := &sourceStore{repos: map[string]control.Repository{}}
	calls := 0
	var answer error
	s := &Service{DB: db, Root: t.TempDir(), AccessCheck: func(context.Context, string, string) error { calls++; return answer }}
	policy := control.Policy{Branches: []string{"main"}, Count: 10}
	answer = contract.Fail("GIT_ACCESS_DENIED", "denied", 403)
	if _, err := s.RegisterWithProvider(context.Background(), "context4ai/agent-graph", policy, 60, "full", nil, nil, "github", "register-k1"); code(err) != "GIT_ACCESS_DENIED" || len(db.repos) != 0 {
		t.Fatalf("unfetchable registration: %v %v", err, db.repos)
	}
	answer = nil
	if _, err := s.RegisterWithProvider(context.Background(), "context4ai/agent-graph", policy, 60, "full", nil, nil, "github", "register-k1"); err != nil || len(db.repos) != 1 {
		t.Fatalf("retry after binding a token: %v", err)
	}
	// A replay or a taken name is answered without contacting the code host.
	before := calls
	answer = contract.Fail("GIT_ACCESS_DENIED", "denied", 403)
	if _, err := s.RegisterWithProvider(context.Background(), "context4ai/agent-graph", policy, 60, "full", nil, nil, "github", "register-k1"); err != nil || calls != before {
		t.Fatalf("replay: %v calls=%d", err, calls-before)
	}
}
