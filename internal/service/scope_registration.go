package service

import (
	"context"
	"sort"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
)

// Projection is read-only: physical generations always live in their own scope.
// A logical version is ready only when every configured group has the same SHA.
func projectRepository(r control.Repository) control.Repository {
	if r.CodeProvider == "" {
		r.CodeProvider = "github"
	}
	r.IndexMode = r.EffectiveIndexMode()
	r.Versions = append([]control.Version(nil), r.Versions...)
	for i := range r.Versions {
		r.Versions[i].Indexed = r.Versions[i].Generation != 0
	}
	copied := make(map[string]control.ScopeState, len(r.Scopes))
	for group, scope := range r.Scopes {
		scope.Versions = append([]control.Version(nil), scope.Versions...)
		for i := range scope.Versions {
			scope.Versions[i].Indexed = scope.Versions[i].Generation != 0
		}
		copied[group] = scope
	}
	r.Scopes = copied
	if r.IndexMode != control.IndexModeMonorepo {
		return r
	}
	r.Heads = map[string]string{}
	observed := map[string]time.Time{}
	versions := map[string]control.Version{}
	ready := map[string]int{}
	r.ObservedAt = time.Time{}
	r.LastIndexedAt = time.Time{}
	r.LastIndexDurationSeconds = 0
	for _, group := range r.PathGroups {
		scope := r.Scopes[group.Name]
		if scope.ObservedAt.After(r.ObservedAt) {
			r.ObservedAt = scope.ObservedAt
		}
		for branch, sha := range scope.Heads {
			if _, ok := observed[branch]; !ok || scope.ObservedAt.After(observed[branch]) {
				r.Heads[branch] = sha
				observed[branch] = scope.ObservedAt
			}
		}
		for _, v := range scope.Versions {
			merged, ok := versions[v.Commit]
			if !ok {
				merged = control.Version{Commit: v.Commit, Time: v.Time}
			}
			if v.Generation != 0 {
				ready[v.Commit]++
				merged.Generation = v.Generation
				merged.Files += v.Files
				merged.Shards += v.Shards
			}
			versions[v.Commit] = merged
		}
	}
	r.Versions = make([]control.Version, 0, len(versions))
	for sha, v := range versions {
		if ready[sha] != len(r.PathGroups) {
			v.Generation = 0
			v.Files = 0
			v.Shards = 0
		}
		v.Indexed = v.Generation != 0
		r.Versions = append(r.Versions, v)
	}
	sort.Slice(r.Versions, func(i, j int) bool {
		if r.Versions[i].Time.Equal(r.Versions[j].Time) {
			return r.Versions[i].Commit < r.Versions[j].Commit
		}
		return r.Versions[i].Time.After(r.Versions[j].Time)
	})
	return r
}

func (s *Service) QueueGroup(ctx context.Context, name, kind, commit, group, key string) (control.Job, error) {
	jobs, err := s.queueGroups(ctx, name, kind, commit, group, key, false)
	if err != nil {
		return control.Job{}, err
	}
	return jobs[0], nil
}

// Legacy full-repository requests retain the original single-job response.
func (s *Service) QueueSelection(ctx context.Context, name, kind, commit, group, key string) (any, error) {
	jobs, err := s.queueGroups(ctx, name, kind, commit, group, key, true)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 1 {
		return jobs[0], nil
	}
	return map[string]any{"jobs": jobs, "status": "accepted"}, nil
}
func (s *Service) queueGroups(ctx context.Context, name, kind, commit, group, key string, all bool) ([]control.Job, error) {
	if kind != "sync" && kind != "index" {
		return nil, contract.Fail("INVALID_ACTION", "Unknown preparation action.", 400)
	}
	k, err := requestKey(identity.Actor(ctx)+":"+kind, key)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var jobs []control.Job
	_, err = control.Mutate(ctx, s.DB, name, func(r *control.Repository) error {
		groups := []string{group}
		if group == "" && r.EffectiveIndexMode() == control.IndexModeMonorepo {
			if !all {
				return contract.Fail("GROUP_REQUIRED", "Select a registered path group.", 400)
			}
			groups = r.WorkGroups()
		}
		jobs = nil // CAS retries rebuild the response from the accepted state.
		for _, g := range groups {
			if _, ok := r.GroupPaths(g); !ok {
				return contract.Fail("UNKNOWN_PATH_GROUP", "Path group is not registered.", 404)
			}
			if kind == "index" {
				if _, ok := r.ScopedVersion(g, commit); !ok {
					return contract.Fail("REVISION_NOT_ELIGIBLE", "Commit is outside a selected group's observed window; synchronize it first.", 404)
				}
			}
			groupKey := k
			body := digest([]string{kind, commit})
			if g != "" {
				groupKey = digest([]string{k, g})
				body = digest([]string{kind, commit, g})
			}
			j, e := r.QueueScoped(kind, commit, g, groupKey, body, identity.Actor(ctx), control.SourceManual, time.Now().UTC())
			if e != nil {
				return e
			}
			jobs = append(jobs, j)
		}
		return nil
	})
	return jobs, err
}
