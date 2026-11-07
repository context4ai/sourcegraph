package service

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
)

// visible reports whether the caller may list, search and read r. Repositories
// registered before per-repository visibility have no public_read and stay
// public. A private repository is visible to its creator and managers, site
// administrators and the deployment credential; a personal token reads as its
// owner, so an administrator's token sees only what the owner created or manages.
func visible(ctx context.Context, r control.Repository) bool {
	if r.AllowsAnonymous() || identity.ReadsAllRepositories(ctx) {
		return true
	}
	user := identity.UserID(ctx)
	return user != "" && !identity.IsAnonymous(ctx) && (r.CreatorID == user || r.IsManager(user))
}

// Fill a visible page, rather than making hidden rows truncate pagination.
func (s *Service) catalogRows(ctx context.Context, after string) ([]control.Repository, error) {
	if identity.ReadsAllRepositories(ctx) {
		return s.DB.List(ctx, after, 100)
	}
	out := []control.Repository{}
	for {
		rows, err := s.DB.List(ctx, after, 100)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if visible(ctx, r) {
				out = append(out, r)
				if len(out) == 100 {
					return out, nil
				}
			}
		}
		if len(rows) < 100 {
			return out, nil
		}
		after = rows[len(rows)-1].ID
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

// Site statistics must not disclose names of repositories the caller cannot
// see, nor whether a guessed name is registered. Callers that do not read every
// repository keep only rows of registered repositories they can see, plus the
// "_overflow" aggregate; anonymous callers get no per-repository rows.
func (s *Service) StatisticsFor(ctx context.Context) any {
	result := s.Statistics().(map[string]any)
	if identity.ReadsAllRepositories(ctx) {
		return result
	}
	shown := map[string]bool{}
	if !identity.IsAnonymous(ctx) {
		shown = s.visibleNames(ctx)
		shown["_overflow"] = true
	}
	buckets := result["buckets"].([]Bucket)
	for i := range buckets {
		for name := range buckets[i].ByRepo {
			if !shown[name] {
				delete(buckets[i].ByRepo, name)
			}
		}
	}
	result["buckets"] = buckets
	return result
}

// visibleNames returns the registered repositories the caller can see, or an
// empty set when the registry cannot be read completely.
func (s *Service) visibleNames(ctx context.Context) map[string]bool {
	names := map[string]bool{}
	err := s.eachRepository(ctx, func(r control.Repository) error {
		if visible(ctx, r) {
			names[r.Name] = true
		}
		return nil
	})
	if err != nil {
		return map[string]bool{}
	}
	return names
}
