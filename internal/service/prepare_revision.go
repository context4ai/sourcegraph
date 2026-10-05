package service

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
)

var prepareSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// PrepareRevision records durable intent only. Git and index I/O run in the worker.
func (s *Service) PrepareRevision(ctx context.Context, name, revision, group, key string) (control.Job, error) {
	if identity.IsAnonymous(ctx) {
		return control.Job{}, contract.Fail("ACCESS_DENIED", "Index preparation requires authorization.", 403)
	}
	revision = strings.TrimPrefix(revision, "refs/heads/")
	k, err := requestKey(identity.Actor(ctx)+":prepare", key)
	if err != nil {
		return control.Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var job control.Job
	_, err = control.Mutate(ctx, s.DB, name, func(r *control.Repository) error {
		if group != "" {
			if _, ok := r.GroupPaths(group); !ok {
				return contract.Fail("UNKNOWN_PATH_GROUP", "Choose a registered group.", 404)
			}
		}
		if _, e := prepareBranch(*r, revision, true); e != nil {
			return e
		}
		var e error
		job, e = r.QueueScoped("prepare", revision, group, k, digest([]string{revision, group}), identity.Actor(ctx), control.SourceManual, time.Now().UTC())
		return e
	})
	return job, err
}

// Empty means discover the remote default, only when automatic default tracking is enabled.
func prepareBranch(r control.Repository, revision string, beforeSync bool) (string, error) {
	if prepareSHA.MatchString(revision) {
		return revision, nil
	}
	if revision == "" && r.Policy.DefaultBranch {
		if beforeSync {
			return "", nil
		}
		if r.DefaultBranch != "" {
			return r.DefaultBranch, nil
		}
		return "", contract.Fail("DEFAULT_BRANCH_NOT_OBSERVED", "Remote default branch could not be discovered.", 409)
	}
	branches := monitoredBranches(r)
	if revision == "" {
		var defaults []string
		for _, b := range branches {
			if b == "master" || b == "main" {
				defaults = append(defaults, b)
			}
		}
		if len(defaults) != 1 {
			return "", contract.Fail("DEFAULT_BRANCH_UNDETERMINED", "Choose a monitored branch: "+strings.Join(branches, ", "), 409)
		}
		return defaults[0], nil
	}
	for _, b := range branches {
		if revision == b {
			return b, nil
		}
	}
	// First sync discovers whether an explicitly requested branch is the remote default.
	if beforeSync && r.Policy.DefaultBranch && r.DefaultBranch == "" && (revision == "master" || revision == "main") {
		return revision, nil
	}
	return "", contract.Fail("REVISION_NOT_ELIGIBLE", "Choose a monitored branch or a full lowercase SHA within retention.", 400)
}

func (s *Service) prepareRevision(ctx context.Context, r control.Repository, j control.Job) error {
	groups := r.WorkGroups()
	if j.Group != "" {
		groups = []string{j.Group}
	}
	if len(groups) == 0 {
		return contract.Fail("UNKNOWN_PATH_GROUP", "No registered groups.", 409)
	}
	// A recovered job retains its pinned SHA; it never silently follows a moved branch.
	target := j.Commit
	if target == "" && prepareSHA.MatchString(j.RequestedRevision) {
		target = j.RequestedRevision
	}
	observed := target != "" && !s.markStorage(r).StorageMissing
	for _, group := range groups {
		_, ok := r.ScopedVersion(group, target)
		observed = observed && ok
	}
	if !observed {
		for _, group := range groups {
			part := j
			part.Group = group
			if err := s.sync(ctx, r, part); err != nil {
				return err
			}
			var err error
			r, err = s.DB.Get(ctx, r.Name)
			if err != nil {
				return err
			}
		}
	}
	if target == "" {
		branch, err := prepareBranch(r, j.RequestedRevision, false)
		if err != nil {
			return err
		}
		target = r.Scope(groups[0]).Heads[branch]
	}
	if target == "" {
		return contract.Fail("REVISION_NOT_ELIGIBLE", "Requested head is not observed.", 404)
	}
	// Validate every group before publishing any index. All scopes use the same SHA.
	for _, group := range groups {
		if _, ok := r.ScopedVersion(group, target); !ok {
			return contract.Fail("REVISION_NOT_ELIGIBLE", "Commit is outside a selected group's synchronized retention window.", 404)
		}
	}
	_, err := control.Mutate(ctx, s.DB, r.Name, func(v *control.Repository) error {
		owned, e := v.Owned(j, time.Now().UTC())
		if e != nil {
			return e
		}
		owned.Commit = target
		owned.Stage = "indexing"
		return nil
	})
	if err != nil {
		return err
	}
	for _, group := range groups {
		part := j
		part.Group, part.Commit = group, target
		v, _ := r.ScopedVersion(group, target)
		if v.Generation == 0 {
			if err := s.build(ctx, r, part); err != nil {
				return err
			}
		}
	}
	return nil
}
