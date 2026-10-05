package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/wasmplugin"
)

type EnhancedRead struct {
	Attachments []PluginAttachment         `json:"-"`
	Issues      []wasmplugin.Issue         `json:"-"`
	Result      any                        `json:"result"`
	Extensions  map[string]json.RawMessage `json:"extensions,omitempty"`
}
type PluginInfo struct {
	Name     string              `json:"name"`
	Root     string              `json:"root"`
	Path     string              `json:"path"`
	Metadata wasmplugin.Metadata `json:"metadata"`
	Issues   []wasmplugin.Issue  `json:"issues,omitempty"`
}
type PluginCatalog struct {
	Commit  string             `json:"commit"`
	Plugins []PluginInfo       `json:"plugins"`
	Issues  []wasmplugin.Issue `json:"issues,omitempty"`
}

func lockPluginRead(ctx context.Context, mu *sync.RWMutex) error {
	for !mu.TryRLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return nil
}
func (s *Service) pluginLease(ctx context.Context) (func(), error) {
	if e := lockPluginRead(ctx, &s.preparation); e != nil {
		return nil, e
	}
	if e := lockPluginRead(ctx, &s.pluginSnapshots); e != nil {
		s.preparation.RUnlock()
		return nil, e
	}
	return func() { s.pluginSnapshots.RUnlock(); s.preparation.RUnlock() }, nil
}
func validatePlugins(requests []wasmplugin.Request) error {
	seen := map[string]bool{}
	for _, p := range requests {
		if !wasmplugin.ValidName(p.Name) || seen[p.Name] {
			return contract.Fail("INVALID_PLUGINS", "Use distinct plugin names containing lowercase letters, digits and hyphens (1..64 characters).", 400)
		}
		seen[p.Name] = true
	}
	if len(requests) > 16 {
		return contract.Fail("INVALID_PLUGINS", "Use at most 16 plugins in one read.", 400)
	}
	return nil
}
func (s *Service) ReadWithPlugins(ctx context.Context, name string, q contract.ReadRequest, plugins []wasmplugin.Request) (EnhancedRead, error) {
	if err := validatePlugins(plugins); err != nil {
		return EnhancedRead{}, err
	}
	if plugins != nil && len(plugins) == 0 {
		f, e := s.Read(ctx, name, q)
		return EnhancedRead{Result: f}, e
	}
	release, e := s.pluginLease(ctx)
	if e != nil {
		return EnhancedRead{}, e
	}
	defer release()
	f, e := s.Read(ctx, name, q)
	if e != nil {
		return EnhancedRead{}, e
	}
	out := EnhancedRead{Result: f}
	out.Extensions = s.enhance(ctx, name, f.Commit, "read", []pluginReadFile{{File: f, ItemID: "read-0"}}, plugins, &out)
	return out, nil
}
func (s *Service) ReadManyWithPlugins(ctx context.Context, name, revision string, files []contract.ReadItemRequest, plugins []wasmplugin.Request) (EnhancedRead, error) {
	if e := validatePlugins(plugins); e != nil {
		return EnhancedRead{}, e
	}
	if plugins != nil && len(plugins) == 0 {
		r, e := s.ReadMany(ctx, name, revision, files)
		return EnhancedRead{Result: r}, e
	}
	release, e := s.pluginLease(ctx)
	if e != nil {
		return EnhancedRead{}, e
	}
	defer release()
	r, e := s.ReadMany(ctx, name, revision, files)
	if e != nil {
		return EnhancedRead{}, e
	}
	var successful []pluginReadFile
	for i, item := range r.Items {
		if item.File != nil {
			successful = append(successful, pluginReadFile{File: *item.File, ItemID: fmt.Sprintf("read-%d", i)})
		}
	}
	out := EnhancedRead{Result: r}
	out.Extensions = s.enhance(ctx, name, r.Commit, "read_many", successful, plugins, &out)
	return out, nil
}
func pluginRoots(r control.Repository) []string {
	if r.EffectiveIndexMode() == control.IndexModeFull {
		return []string{""}
	}
	roots := []string{}
	for _, g := range r.PathGroups {
		roots = append(roots, g.Paths...)
	}
	sort.Strings(roots)
	return roots
}
func (s *Service) pluginBlob(ctx context.Context, name, sha, root, relative string, limit int) ([]byte, error) {
	if e := gitstore.ValidatePath(relative, false); e != nil {
		return nil, e
	}
	// Validate before joining: cleaning an untrusted path must not erase traversal.
	p := relative
	if root != "" {
		p = root + "/" + relative
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, e := s.Repo(ctx, name)
	if e != nil {
		return nil, e
	}
	if _, e = eligible(r, sha); e != nil {
		return nil, e
	}
	valid := false
	for _, candidate := range pluginRoots(r) {
		valid = valid || candidate == "" || candidate == root || strings.HasPrefix(root, candidate+"/")
	}
	if !valid {
		return nil, contract.Fail("PLUGIN_SCOPE_CHANGED", "Plugin scope is no longer registered.", 409)
	}
	scope, e := readScope(r, sha, p)
	if e != nil {
		return nil, e
	}
	g, e := s.scopeStore(name, scope)
	if e != nil {
		return nil, e
	}
	return g.ReadBlobPath(ctx, name, sha, p, limit)
}
func (s *Service) enhance(ctx context.Context, name, sha, operation string, files []pluginReadFile, requests []wasmplugin.Request, delivery *EnhancedRead) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	failAll := func(code, msg string) map[string]json.RawMessage {
		for _, p := range requests {
			out[p.Name] = wasmplugin.Failure(code, msg)
			delivery.Issues = append(delivery.Issues, wasmplugin.Issue{Code: code, Message: msg})
		}
		return out
	}
	if s.Plugins == nil || s.Plugins.Config.Disabled {
		return failAll("PLUGINS_DISABLED", "Reading enhancements are disabled.")
	}
	ctx, cancel := context.WithTimeout(ctx, s.Plugins.Config.Timeout)
	defer cancel()
	r, e := s.Repo(ctx, name)
	if e != nil {
		return failAll("PLUGIN_UNAVAILABLE", "Repository is no longer readable.")
	}
	readable := false
	for _, f := range files {
		readable = readable || (f.Kind == "file" && !f.IsLFSPointer && f.ReturnedStartLine != nil && f.ReturnedEndLine != nil)
	}
	if !readable {
		return failAll("PLUGIN_NO_READABLE_FILES", "No regular text files were returned to enhance.")
	}
	snapshot, err := s.pluginCatalog(ctx, name, sha, r)
	if err != nil {
		if requests == nil {
			out["_discovery"] = wasmplugin.Failure("PLUGIN_UNAVAILABLE", "Cannot discover plugins at this commit.")
			delivery.Issues = append(delivery.Issues, wasmplugin.Issue{Code: "PLUGIN_UNAVAILABLE", Message: "Cannot discover plugins at this commit."})
			return out
		}
		return failAll("PLUGIN_UNAVAILABLE", "Cannot discover plugins at this commit.")
	}
	automatic := requests == nil
	if automatic {
		seen := map[string]bool{}
		for _, info := range snapshot.catalog.Plugins {
			if !seen[info.Name] {
				seen[info.Name] = true
				requests = append(requests, wasmplugin.Request{Name: info.Name})
			}
		}
	}
	issues := append([]wasmplugin.Issue(nil), snapshot.catalog.Issues...)
	for _, info := range snapshot.catalog.Plugins {
		issues = append(issues, info.Issues...)
	}
	delivery.Issues = append(delivery.Issues, issues...)
	if len(issues) != 0 {
		out["_discovery"], _ = json.Marshal(map[string]any{"issues": issues})
	}
	selectedCount := 0
	for _, request := range requests {
		selected := map[string][]map[string]any{}
		infos := map[string]PluginInfo{}
		for _, f := range files {
			if f.Kind != "file" || f.IsLFSPointer || f.ReturnedStartLine == nil || f.ReturnedEndLine == nil {
				continue
			}
			var nearest *PluginInfo
			for _, info := range snapshot.catalog.Plugins {
				if info.Name != request.Name || (info.Root != "" && !strings.HasPrefix(f.Path, info.Root+"/")) {
					continue
				}
				if nearest == nil || len(info.Root) > len(nearest.Root) {
					copy := info
					nearest = &copy
				}
			}
			if nearest == nil {
				continue
			}
			// Resolve inheritance before considering default_enabled: a disabled child
			// intentionally shadows the same-name parent.
			if automatic {
				allowed := false
				for _, op := range nearest.Metadata.Operations {
					allowed = allowed || op == operation
				}
				if !nearest.Metadata.DefaultEnabled || !allowed {
					continue
				}
			}
			root := nearest.Root
			relative := f.Path
			if root != "" {
				relative = strings.TrimPrefix(f.Path, root+"/")
			}
			selected[root] = append(selected[root], map[string]any{"item_id": f.ItemID, "path": relative, "repository_path": f.Path, "content": f.Content, "start_line": f.ReturnedStartLine, "end_line": f.ReturnedEndLine, "next_start_line": f.NextStartLine, "truncated": f.Truncated, "has_more": f.HasMore})
			infos[root] = *nearest
		}
		if len(selected) == 0 {
			if !automatic {
				out[request.Name] = wasmplugin.Failure("PLUGIN_FILE_NOT_FOUND", "No applicable plugin was found for the returned files.")
				delivery.Issues = append(delivery.Issues, wasmplugin.Issue{Code: "PLUGIN_FILE_NOT_FOUND", Message: "No applicable plugin was found for the returned files."})
			}
			continue
		}
		if automatic && selectedCount >= 16 {
			out["_discovery"] = wasmplugin.Failure("PLUGIN_LIMIT", "More than 16 default plugins; specify the desired plugins explicitly.")
			delivery.Issues = append(delivery.Issues, wasmplugin.Issue{Code: "PLUGIN_LIMIT", Message: "Too many default plugins."})
			break
		}
		selectedCount++
		active := []string{}
		for root := range selected {
			active = append(active, root)
		}
		sort.Strings(active)
		type scopedOutput struct {
			Root string          `json:"root"`
			Data json.RawMessage `json:"data"`
		}
		results := []scopedOutput{}
		for _, root := range active {
			info := infos[root]
			var runIssue *wasmplugin.Issue
			result := func() json.RawMessage {
				artifact := snapshot.artifacts[info.Path]
				if len(info.Issues) != 0 || artifact == nil {
					runIssue = &wasmplugin.Issue{Code: "PLUGIN_UNAVAILABLE", Message: "Cannot read valid Wasm at this path; see discovery diagnostics."}
					return wasmplugin.Failure(runIssue.Code, runIssue.Message)
				}
				input := map[string]any{"abi_version": 2, "operation": operation, "repository": name, "commit": sha, "root": root, "files": selected[root], "args": request.Args}
				if request.Args == nil {
					input["args"] = map[string]any{}
				}
				key := digest([]any{s.gitRoot(), name, sha, root, r.PathGroups, request.Args})
				raw, issue := s.Plugins.RunResult(ctx, artifact, key, input, func(ctx context.Context, p string) ([]byte, error) {
					return s.pluginBlob(ctx, name, sha, root, p, s.Plugins.Config.FileBytes)
				})
				runIssue = issue
				return raw
			}()
			if runIssue != nil {
				delivery.Issues = append(delivery.Issues, *runIssue)
			} else {
				delivery.acceptAttachments(result, selected[root])
			}
			results = append(results, scopedOutput{Root: root, Data: result})
		}
		if len(results) == 1 && results[0].Root == "" {
			out[request.Name] = results[0].Data
		} else {
			out[request.Name], _ = json.Marshal(map[string]any{"scopes": results})
		}
	}
	return out
}

// DiscoverPlugins returns the cached recursive catalog. No execution occurs.
func (s *Service) DiscoverPlugins(ctx context.Context, name, revision string) (PluginCatalog, error) {
	out := PluginCatalog{Plugins: []PluginInfo{}}
	if s.Plugins == nil {
		return out, contract.Fail("PLUGINS_DISABLED", "Plugin host is unavailable.", 503)
	}
	ctx, cancel := context.WithTimeout(ctx, s.Plugins.Config.Timeout)
	defer cancel()
	release, e := s.pluginLease(ctx)
	if e != nil {
		return out, e
	}
	defer release()
	r, e := s.Repo(ctx, name)
	if e != nil {
		return out, e
	}
	v, e := eligible(r, revision)
	if e != nil {
		return out, e
	}
	snapshot, err := s.pluginCatalog(ctx, name, v.Commit, r)
	if err != nil {
		return out, err
	}
	// Return a copy: callers must not mutate the shared immutable catalog.
	b, _ := json.Marshal(snapshot.catalog)
	_ = json.Unmarshal(b, &out)
	return out, nil
}
