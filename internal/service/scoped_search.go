package service

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"

	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
)

type selectedScope struct {
	name    string
	paths   []string
	version control.Version
}

func queryPaths(paths []string) ([]string, error) {
	if len(paths) > 64 {
		return nil, contract.Fail("INVALID_PATHS", "Choose at most 64 relative directories.", 400)
	}
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		if gitstore.ValidatePath(p, false) != nil {
			return nil, contract.Fail("INVALID_PATHS", "Use repository-relative directories.", 400)
		}
		for _, part := range strings.Split(p, "/") {
			if strings.EqualFold(part, ".git") {
				return nil, contract.Fail("INVALID_PATHS", "Git metadata cannot be queried.", 400)
			}
		}
		if !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	sort.Strings(out)
	return out, nil
}

// Paths narrow registered coverage; a broader parent selects its registered children.
// Every requested path must intersect coverage so typos cannot silently appear empty.
func selectScopes(r control.Repository, sha string, paths []string) ([]selectedScope, error) {
	paths, err := queryPaths(paths)
	if err != nil {
		return nil, err
	}
	if r.EffectiveIndexMode() == control.IndexModeFull {
		v, ok := r.Version(sha)
		if !ok {
			return nil, contract.Fail("REVISION_NOT_ELIGIBLE", "Commit is outside the current policy.", 404)
		}
		return []selectedScope{{paths: paths, version: v}}, nil
	}
	matched := make([]bool, len(paths))
	out := []selectedScope{}
	for _, group := range r.PathGroups {
		registeredPaths := group.Paths
		if v, ok := r.ScopedVersion(group.Name, sha); ok && v.Links != nil {
			registeredPaths = v.Links.Paths
		}
		coverage := []string{}
		if len(paths) == 0 {
			coverage = append(coverage, registeredPaths...)
		} else {
			for i, p := range paths {
				for _, registered := range registeredPaths {
					if control.PathContains(registered, p) {
						coverage = append(coverage, p)
						matched[i] = true
					} else if control.PathContains(p, registered) {
						coverage = append(coverage, registered)
						matched[i] = true
					}
				}
			}
		}
		if len(coverage) == 0 {
			continue
		}
		v, ok := r.ScopedVersion(group.Name, sha)
		if !ok {
			return nil, contract.Fail("SCOPE_NOT_READY", "Group "+group.Name+" has not synchronized commit "+sha+"; synchronize that group first.", 409)
		}
		out = append(out, selectedScope{name: group.Name, paths: coverage, version: v})
	}
	for i, ok := range matched {
		if !ok {
			for _, group := range r.PathGroups {
				if v, exists := r.ScopedVersion(group.Name, sha); exists && v.LinkUnavailable {
					return nil, contract.Fail("SCOPE_NOT_READY", "A snapshot manifest is unavailable; expanded path coverage is unknown.", 409)
				}
			}
			return nil, contract.Fail("PATH_NOT_REGISTERED", "Path is outside registered coverage: "+paths[i], 400)
		}
	}
	if len(out) == 0 {
		return nil, contract.Fail("PATH_NOT_REGISTERED", "No registered groups cover this request.", 400)
	}
	return out, nil
}
func pathFilter(q string, paths []string) string {
	if len(paths) == 0 {
		return q
	}
	parts := []string{}
	for _, p := range paths {
		parts = append(parts, "file:"+zoektclient.QuoteQuery("^"+regexp.QuoteMeta(p)+"(/|$)"))
	}
	return "( " + q + " ) ( case:yes ( " + strings.Join(parts, " or ") + " ) )"
}

// Search runs an rg-style search at one commit; the engine query is generated
// here, never supplied by the caller.
func (s *Service) Search(ctx context.Context, name string, req contract.SearchRequest) (out any, err error) {
	start := time.Now()
	revision := req.Revision
	defer func() {
		s.metrics.Observe(name, "search", start, err != nil)
		s.logQuery(ctx, name, "search", start, map[string]any{"revision": revision, "pattern": req.Pattern, "fixed_strings": req.FixedStrings, "ignore_case": req.IgnoreCase, "glob": req.Glob, "paths": req.Paths, "output": req.Output}, err)
	}()
	if req.Output != "" && req.Output != "content" && req.Output != "files" {
		return nil, contract.Fail("INVALID_OUTPUT", "output must be content or files.", 400)
	}
	pattern := zoektclient.Pattern{Text: req.Pattern, FixedStrings: req.FixedStrings, IgnoreCase: req.IgnoreCase, Globs: req.Glob}
	options := zoektclient.DisplayOptions{FilesOnly: req.Output == "files", Pattern: &pattern}
	paths, e := queryPaths(req.Paths)
	if e != nil {
		return nil, e
	}
	q, e := zoektclient.PatternQuery(pattern)
	if e != nil {
		return nil, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, e := s.Repo(ctx, name)
	if e != nil {
		return nil, e
	}
	v, e := eligible(r, revision)
	if e != nil {
		return nil, e
	}
	r, e = s.contentRepository(r, v.Commit)
	if e != nil {
		return nil, e
	}
	scopes, e := selectScopes(r, v.Commit, paths)
	if e != nil {
		return nil, e
	}
	targets := make([]zoektclient.Target, 0, len(scopes))
	coverage := []string{}
	groups := []string{}
	docs := 0
	for _, scope := range scopes {
		if scope.version.Generation == 0 || scope.version.LinkUnavailable {
			return nil, contract.Fail("INDEX_NOT_READY", "Group "+scope.name+" is not indexed at "+v.Commit+"; request preparation for this commit.", 409)
		}
		target := zoektclient.Target{Repo: name, Commit: v.Commit, RepositoryID: scope.version.Generation, Group: scope.name}
		loaded, e := s.Engine.Loaded(ctx, target, scope.version.Shards, scope.version.Files)
		if e != nil {
			return nil, e
		}
		if !loaded {
			return nil, contract.Fail("INDEX_NOT_READY", "A selected group's complete index is not loaded.", 409)
		}
		targets = append(targets, target)
		coverage = append(coverage, scope.paths...)
		groups = append(groups, scope.name)
		docs += scope.version.Files
	}
	// Monorepo snapshots are already physically restricted. Only add a query
	// predicate for an explicit narrower request, preserving the query budget.
	if len(paths) > 0 {
		q = pathFilter(q, coverage)
	}
	result, e := s.Engine.SearchManyWithOptions(ctx, targets, q, options)
	if e != nil {
		return nil, e
	}
	var native map[string]any
	if e = json.Unmarshal(result, &native); e != nil {
		return nil, e
	}
	if files, ok := native["Files"].([]any); ok {
		for _, f := range files {
			m := f.(map[string]any)
			fileCoverage := coverage
			engineName, _ := m["Repository"].(string)
			for i, target := range targets {
				if target.EngineName() == engineName {
					fileCoverage = scopes[i].paths
					break
				}
			}
			if len(fileCoverage) > 0 {
				path, _ := m["FileName"].(string)
				allowed := false
				for _, p := range fileCoverage {
					if control.PathContains(p, path) {
						allowed = true
						break
					}
				}
				if !allowed {
					return nil, contract.Fail("ENGINE_SCOPE_MISMATCH", "Engine returned a file outside selected paths.", 502)
				}
			}
			// Preserve engine identity until canonicalization.
		}
	}
	num := func(k string) float64 { n, _ := native[k].(float64); return n }
	partial := num("Crashes") > 0 || num("FilesSkipped") > 0 || num("ShardsSkipped") > 0 || num("MatchCount") >= 10000
	displayed := 0
	if files, ok := native["Files"].([]any); ok {
		displayed = len(files)
	}
	responseTruncated, _ := native["ResponseTruncated"].(bool)
	filesOnly, _ := native["ResponseFilesOnly"].(bool)
	delete(native, "ResponseTruncated")
	delete(native, "ResponseFilesOnly")
	truncated := responseTruncated || num("FileCount") > float64(displayed) || num("MatchCount") > 1000
	state := "ok"
	if partial {
		state = "partial"
	}
	for _, key := range []string{"RepoURLs", "LineFragments"} {
		if values, ok := native[key].(map[string]any); ok {
			for _, target := range targets {
				if value, exists := values[target.EngineName()]; exists {
					native[key] = map[string]any{name: value}
					break
				}
			}
		}
	}
	meta := map[string]any{"Protocol": "zoekt-single-repo-v1", "Status": state, "Partial": partial, "Truncated": truncated, "Commit": v.Commit, "Repository": name, "Coverage": map[string]any{"Profile": "text-no-ctags", "IndexedDocuments": docs, "CompleteRepository": false, "IndexMode": r.EffectiveIndexMode(), "Paths": coverage, "Groups": groups}}
	for k, value := range linkCoverage(scopes) {
		meta["Coverage"].(map[string]any)[k] = value
	}
	if e := canonicalSearchFiles(native, targets, scopes); e != nil {
		return nil, e
	}
	if filesOnly {
		meta["FilesOnly"] = true
	}
	if responseTruncated {
		meta["TruncationReason"] = "response_bytes"
		meta["FilesOnly"] = filesOnly
	}
	return map[string]any{"Result": native, "Meta": meta}, nil
}
