package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
)

func TestEligibleRevisionDiagnostics(t *testing.T) {
	sha := strings.Repeat("a", 40)
	repo := control.Repository{
		Enabled:  true,
		Policy:   control.Policy{Branches: []string{"main", "pending", "deadbee", strings.Repeat("b", 40)}},
		Heads:    map[string]string{"main": sha, "cafe123": sha, "empty": ""},
		Versions: []control.Version{{Commit: sha}},
	}
	for _, tc := range []struct {
		revision, code string
		status         int
	}{
		{"pending", "BRANCH_NOT_OBSERVED", 409},
		{"refs/heads/pending", "BRANCH_NOT_OBSERVED", 409},
		{"empty", "BRANCH_NOT_OBSERVED", 409},
		{"deadbee", "BRANCH_NOT_OBSERVED", 409},
		{strings.Repeat("b", 40), "BRANCH_NOT_OBSERVED", 409},
		{"refs/heads/deadbee", "BRANCH_NOT_OBSERVED", 409},
		{"beef123", "SHORT_SHA_UNSUPPORTED", 400},
		{"BEEF123", "SHORT_SHA_UNSUPPORTED", 400},
		{"a", "SHORT_SHA_UNSUPPORTED", 400},
		{strings.Repeat("c", 39), "SHORT_SHA_UNSUPPORTED", 400},
		{strings.Repeat("c", 40), "REVISION_NOT_ELIGIBLE", 404},
		{"feature", "BRANCH_NOT_MONITORED", 404},
		{"refs/heads/feature", "BRANCH_NOT_MONITORED", 404},
		{"refs/heads/beef123", "BRANCH_NOT_MONITORED", 404},
		{"HEAD", "INVALID_REVISION", 400},
		{"refs/tags/v1", "INVALID_REVISION", 400},
		{"refs/merge-requests/1/head", "INVALID_REVISION", 400},
		{strings.Repeat("A", 40), "INVALID_REVISION", 400},
		{strings.Repeat("a", 41), "INVALID_REVISION", 400},
		{" main ", "INVALID_REVISION", 400},
		{"main~1", "INVALID_REVISION", 400},
	} {
		t.Run(tc.revision, func(t *testing.T) {
			v, err := eligible(repo, tc.revision)
			var ce *contract.Error
			if !errors.As(err, &ce) || ce.Code != tc.code || ce.HTTPStatus != tc.status || v.Commit != "" {
				t.Fatalf("wrong version diagnostic: version=%+v err=%v want=%s/%d", v, err, tc.code, tc.status)
			}
		})
	}
}

func TestEligibleKeepsExactCommitAndBranchPriority(t *testing.T) {
	sha, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	repo := control.Repository{
		Enabled: true, Policy: control.Policy{Branches: []string{"main", "deadbee", sha}},
		Heads:    map[string]string{"main": sha, "deadbee": other, sha: other},
		Versions: []control.Version{{Commit: sha}, {Commit: other}},
	}
	for _, tc := range []struct{ revision, commit string }{
		{"", sha}, {"main", sha}, {"refs/heads/main", sha},
		{"deadbee", other}, {"refs/heads/deadbee", other},
		{sha, other}, {"refs/heads/" + sha, other}, {other, other},
	} {
		v, err := eligible(repo, tc.revision)
		if err != nil || v.Commit != tc.commit {
			t.Fatalf("revision %q resolved to %+v, %v; want %s", tc.revision, v, err, tc.commit)
		}
	}
}

func TestEligibleDefaultAndRepositoryState(t *testing.T) {
	sha := strings.Repeat("a", 40)
	base := control.Repository{Enabled: true, Policy: control.Policy{Branches: []string{"main"}}, Heads: map[string]string{"main": sha}, Versions: []control.Version{{Commit: sha}}}
	for _, tc := range []struct {
		name, code string
		change     func(*control.Repository)
	}{
		{"storage missing", "REPOSITORY_STORAGE_MISSING", func(r *control.Repository) { r.StorageMissing = true }},
		{"disabled", "REPOSITORY_DISABLED", func(r *control.Repository) { r.Enabled = false }},
		{"deleted", "REPOSITORY_DISABLED", func(r *control.Repository) { r.Deleted = true }},
		{"ambiguous default", "DEFAULT_BRANCH_UNDETERMINED", func(r *control.Repository) { r.Policy.Branches = []string{"main", "master"} }},
		{"no default", "DEFAULT_BRANCH_UNDETERMINED", func(r *control.Repository) { r.Policy.Branches = []string{"feature"} }},
		{"default not observed", "DEFAULT_BRANCH_NOT_OBSERVED", func(r *control.Repository) { r.Heads = nil }},
		{"automatic default unknown", "DEFAULT_BRANCH_NOT_OBSERVED", func(r *control.Repository) { r.Policy.DefaultBranch = true }},
		{"automatic default not observed", "BRANCH_NOT_OBSERVED", func(r *control.Repository) { r.Policy.DefaultBranch = true; r.DefaultBranch = "develop" }},
		{"stale head", "REVISION_NOT_ELIGIBLE", func(r *control.Repository) { r.Versions = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := base
			tc.change(&repo)
			_, err := eligible(repo, "")
			var ce *contract.Error
			if !errors.As(err, &ce) || ce.Code != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
	base.Policy.DefaultBranch = true
	base.DefaultBranch = "develop"
	base.Heads = map[string]string{"develop": sha}
	v, err := eligible(base, "")
	if err != nil || v.Commit != sha {
		t.Fatalf("automatic default changed: %+v %v", v, err)
	}
}
