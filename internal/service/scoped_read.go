package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

func (s *Service) scopeStore(repo string, scope selectedScope) (*gitstore.Store, error) {
	if scope.version.LinkUnavailable {
		return nil, contract.Fail("INDEX_NOT_READY", "Snapshot link manifest is unavailable.", 409)
	}
	g := s.Git
	var err error
	if scope.name != "" {
		g, err = g.ForGroup(repo, scope.name)
		if err != nil {
			return nil, err
		}
	}
	if scope.version.Links != nil {
		return g.WithCoverage(scope.version.Links.Paths, scope.version.Generation)
	}
	return g.WithPaths(scope.paths)
}
func (s *Service) Read(ctx context.Context, name string, q contract.ReadRequest) (file gitstore.File, err error) {
	start := time.Now()
	defer func() {
		s.metrics.Observe(name, "read", start, err != nil)
		s.logQuery(ctx, name, "read", start, q, err)
	}()
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, e := s.Repo(ctx, name)
	if e != nil {
		return file, e
	}
	v, e := eligible(r, q.Revision)
	if e != nil {
		return file, e
	}
	if r.EffectiveIndexMode() == control.IndexModeMonorepo {
		r, e = s.contentRepository(r, v.Commit)
		if e != nil {
			return file, e
		}
	}
	scope, e := readScope(r, v.Commit, q.Path)
	if e != nil {
		return file, e
	}
	g, e := s.scopeStore(name, scope)
	if e != nil {
		return file, e
	}
	return g.Read(ctx, name, v.Commit, q.Path, q.StartLine, q.EndLine)
}

// readScope is shared by individual and batch reads. Once selected, a group
// reader can be reused for its registered paths without widening its coverage.
func readScope(r control.Repository, commit, path string) (selectedScope, error) {
	if r.EffectiveIndexMode() == control.IndexModeFull {
		v, _ := r.Version(commit)
		return selectedScope{version: v}, nil
	}
	if err := gitstore.ValidatePath(path, false); err != nil {
		return selectedScope{}, err
	}
	scopes := []selectedScope{}
	covered := false
	for _, group := range r.PathGroups {
		v, ok := r.ScopedVersion(group.Name, commit)
		paths := group.Paths
		if v.Links != nil {
			paths = v.Links.Paths
		}
		for _, p := range paths {
			if control.PathContains(p, path) {
				covered = true
				if ok {
					scopes = append(scopes, selectedScope{name: group.Name, paths: paths, version: v})
				}
				break
			}
		}
	}
	if !covered {
		return selectedScope{}, contract.Fail("PATH_NOT_REGISTERED", "Path is outside this snapshot coverage.", 400)
	}
	// Prefer explicitly registered scopes, then a stable group name.
	explicit := func(scope selectedScope) bool {
		for _, g := range r.PathGroups {
			if g.Name == scope.name {
				for _, p := range g.Paths {
					if control.PathContains(p, path) {
						return true
					}
				}
			}
		}
		return false
	}
	sort.SliceStable(scopes, func(i, j int) bool {
		a, b := explicit(scopes[i]), explicit(scopes[j])
		if a != b {
			return a
		}
		return scopes[i].name < scopes[j].name
	})
	for _, scope := range scopes {
		if scope.version.Generation == 0 || scope.version.LinkUnavailable {
			continue
		}
		for _, group := range r.PathGroups {
			if group.Name == scope.name {
				scope.paths = group.Paths
				break
			}
		}
		if scope.version.Links != nil {
			scope.paths = scope.version.Links.Paths
		}
		return scope, nil
	}
	return selectedScope{}, contract.Fail("CONTENT_NOT_READY", "Prepare this scope at the requested commit before reading.", 409)
}

type aggregateCursor struct{ Identity, Last string }

func decodeAggregateCursor(cursor, identity string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, e := base64.RawURLEncoding.DecodeString(cursor)
	var c aggregateCursor
	if e != nil || len(raw) > 8192 || contract.DecodeObject(raw, &c) != nil || c.Identity != identity || c.Last == "" {
		return "", contract.Fail("INVALID_CURSOR", "Cursor does not match this commit and registered coverage.", 400)
	}
	return c.Last, nil
}
func encodeAggregateCursor(identity, last string) string {
	b, _ := json.Marshal(aggregateCursor{identity, last})
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Service) List(ctx context.Context, name string, q contract.ListRequest) (gitstore.Tree, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, e := s.Repo(ctx, name)
	if e != nil {
		return gitstore.Tree{}, e
	}
	v, e := eligible(r, q.Revision)
	if e != nil {
		return gitstore.Tree{}, e
	}
	if r.EffectiveIndexMode() == control.IndexModeFull {
		return s.Git.List(ctx, name, v.Commit, q.Path, q.First, q.After)
	}
	r, e = s.contentRepository(r, v.Commit)
	if e != nil {
		return gitstore.Tree{}, e
	}
	if q.First < 1 || q.First > 1000 || gitstore.ValidatePath(q.Path, true) != nil {
		return gitstore.Tree{}, contract.Fail("INVALID_REQUEST", "Use a relative directory and page size 1..1000.", 400)
	}
	paths := []string{}
	if q.Path != "" {
		paths = append(paths, q.Path)
	}
	scopes, e := selectScopes(r, v.Commit, paths)
	if e != nil {
		return gitstore.Tree{}, e
	}
	identity := digest([]any{"list", name, v.Commit, q.Path, r.PathGroups, scopeGenerations(scopes)})
	last, e := decodeAggregateCursor(q.After, identity)
	if e != nil {
		return gitstore.Tree{}, e
	}
	merged := map[string]gitstore.Entry{}
	out := gitstore.Tree{Commit: v.Commit, Entries: []gitstore.Entry{}}
	seen := 0
	for _, scope := range scopes {
		g, e := s.scopeStore(name, scope)
		if e != nil {
			return out, e
		}
		after := ""
		for {
			tree, e := g.List(ctx, name, v.Commit, q.Path, 1000, after)
			if e != nil {
				return out, e
			}
			out.TreeOID = tree.TreeOID
			out.Via = tree.Via
			for _, entry := range tree.Entries {
				merged[entry.Path] = entry
				seen++
			}
			if seen > 20000 {
				return out, contract.Fail("DIRECTORY_BUDGET_EXCEEDED", "Narrow to a smaller registered directory.", 422)
			}
			if !tree.HasMoreResults {
				break
			}
			if tree.NextCursor == "" || tree.NextCursor == after {
				return out, contract.Fail("INVALID_GIT_RESULT", "Directory pagination did not advance.", 500)
			}
			after = tree.NextCursor
		}
	}
	entries := make([]gitstore.Entry, 0, len(merged))
	found := last == ""
	for _, entry := range merged {
		if entry.Path == last {
			found = true
		}
		if entry.Path > last {
			entries = append(entries, entry)
		}
	}
	if !found {
		return out, contract.Fail("INVALID_CURSOR", "Cursor entry is outside registered coverage.", 400)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if len(entries) > q.First {
		out.HasMoreResults = true
		entries = entries[:q.First]
		out.NextCursor = encodeAggregateCursor(identity, entries[len(entries)-1].Path)
	}
	out.Entries = entries
	return out, nil
}
func (s *Service) Diff(ctx context.Context, name string, q contract.DiffRequest) (gitstore.Diff, error) {
	out := gitstore.Diff{Changes: []gitstore.Change{}}
	if q.Base == "" || q.Head == "" {
		return out, contract.Fail("INVALID_REVISION", "Diff requires explicit base and head.", 400)
	}
	if q.First < 1 || q.First > 100 {
		return out, contract.Fail("INVALID_LIMIT", "Diff limit must be in 1..100.", 400)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, e := s.Repo(ctx, name)
	if e != nil {
		return out, e
	}
	base, e := eligible(r, q.Base)
	if e != nil {
		return out, e
	}
	head, e := eligible(r, q.Head)
	if e != nil {
		return out, e
	}
	scopes, e := selectScopes(r, head.Commit, q.Paths)
	if e != nil {
		return out, e
	}
	baseScopes, e := selectScopes(r, base.Commit, q.Paths)
	if e != nil {
		return out, e
	}
	if r.EffectiveIndexMode() == control.IndexModeMonorepo {
		for _, scope := range append(baseScopes, scopes...) {
			if scope.version.Generation == 0 {
				return out, contract.Fail("CONTENT_NOT_READY", "Prepare both commits for each selected group before comparing partial-clone content.", 409)
			}
		}
	}
	if r.EffectiveIndexMode() == control.IndexModeFull {
		g, e := s.scopeStore(name, scopes[0])
		if e != nil {
			return out, e
		}
		return g.Compare(ctx, name, base.Commit, head.Commit, q.First, q.After)
	}
	identity := digest([]any{"diff", name, base.Commit, head.Commit, q.Paths, r.PathGroups})
	last, e := decodeAggregateCursor(q.After, identity)
	if e != nil {
		return out, e
	}
	out.BaseCommit = base.Commit
	out.HeadCommit = head.Commit
	type scopedChange struct {
		change gitstore.Change
		store  *gitstore.Store
	}
	candidates := []scopedChange{}
	found := last == ""
	for _, scope := range scopes {
		g, e := s.scopeStore(name, scope)
		if e != nil {
			return out, e
		}
		presence, e := g.DiffPathStatus(ctx, name, base.Commit, head.Commit)
		if e != nil {
			return out, e
		}
		out.PathStatus = append(out.PathStatus, presence...)
		metadata, e := g.Changes(ctx, name, base.Commit, head.Commit)
		if e != nil {
			return out, e
		}
		// Each group's metadata is sorted. Only its first page candidates can
		// appear in the aggregate page; later records never require blob reads.
		kept := 0
		for _, change := range metadata {
			if change.Path == last {
				found = true
			}
			if change.Path > last && kept < q.First+1 {
				candidates = append(candidates, scopedChange{change, g})
				kept++
			}
		}
	}
	if !found {
		return out, contract.Fail("INVALID_CURSOR", "Cursor entry is outside registered coverage.", 400)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].change.Path < candidates[j].change.Path })
	if len(candidates) > q.First {
		out.HasMoreResults = true
		candidates = candidates[:q.First]
		out.NextCursor = encodeAggregateCursor(identity, candidates[len(candidates)-1].change.Path)
	}
	changes := make([]gitstore.Change, 0, len(candidates))
	remaining := gitstore.MaxPatchBytes
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && candidates[end].store == candidates[start].store {
			end++
		}
		page := make([]gitstore.Change, 0, end-start)
		for _, candidate := range candidates[start:end] {
			page = append(page, candidate.change)
		}
		patched, budget, e := candidates[start].store.PatchChanges(ctx, name, base.Commit, head.Commit, page, remaining)
		if e != nil {
			return out, e
		}
		remaining = budget
		changes = append(changes, patched...)
		start = end
	}
	out.Changes = changes
	return out, nil
}
