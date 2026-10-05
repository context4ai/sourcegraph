package service

import (
	"fmt"
	"path/filepath"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
)

// contentRepository is a request-local view. It never persists expanded ranges
// as user policy, and is deliberately not used by diff or plugin discovery.
func (s *Service) contentRepository(r control.Repository, sha string) (control.Repository, error) {
	load := func(v control.Version, group string) (control.Version, error) {
		if v.Commit != sha || v.Generation == 0 || v.LinkFormat == 0 {
			return v, nil
		}
		name := filepath.Join(s.Root, "manifests", fmt.Sprintf("%d.json", v.Generation))
		a, err := s.linkManifests.load(name)
		if err != nil {
			return v, err
		}
		if a.Links.Format != v.LinkFormat || a.Target.Repo != r.Name || a.Target.Commit != sha || a.Target.Group != group || a.Target.RepositoryID != v.Generation {
			return v, contract.Fail("INDEX_NOT_READY", "Snapshot link manifest does not match this generation.", 409)
		}
		v.Links = a.Links
		return v, nil
	}
	r.Versions = append([]control.Version(nil), r.Versions...)
	for i, v := range r.Versions {
		n, err := load(v, "")
		if err != nil {
			return r, err
		}
		r.Versions[i] = n
	}
	scopes := map[string]control.ScopeState{}
	for name, state := range r.Scopes {
		state.Versions = append([]control.Version(nil), state.Versions...)
		for i, v := range state.Versions {
			n, err := load(v, name)
			if err != nil {
				n = v
				n.LinkUnavailable = true
			}
			state.Versions[i] = n
		}
		scopes[name] = state
	}
	r.Scopes = scopes
	return r, nil
}

func scopeGenerations(scopes []selectedScope) []uint32 {
	out := []uint32{}
	for _, s := range scopes {
		out = append(out, s.version.Generation)
	}
	return out
}
