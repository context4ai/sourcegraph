package service

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/wasmplugin"
)

type pluginSnapshot struct {
	catalog   PluginCatalog
	artifacts map[string][]byte
	bytes     int
	used      time.Time
}
type pluginPreparation struct{ name, sha string }
type pluginCacheState struct {
	mu      sync.Mutex
	entries map[string]*pluginSnapshot
	loading map[string]chan struct{}
	once    sync.Once
	wake    chan pluginPreparation
}

func (s *Service) pluginSignal() chan pluginPreparation {
	s.pluginCache.once.Do(func() { s.pluginCache.wake = make(chan pluginPreparation, 256) })
	return s.pluginCache.wake
}
func (s *Service) wakePlugins(name, sha string) {
	select {
	case s.pluginSignal() <- pluginPreparation{name, sha}:
	default:
	}
}

// Cache identity includes readiness of every scope at this SHA. Completing a
// second monorepo group cannot leave the first group's partial catalog cached.
func (s *Service) pluginCatalog(ctx context.Context, name, sha string, r control.Repository) (*pluginSnapshot, error) {
	if _, err := eligible(r, sha); err != nil {
		return nil, err
	}
	generations := map[string]uint32{}
	for _, group := range r.PathGroups {
		if v, ok := r.ScopedVersion(group.Name, sha); ok {
			generations[group.Name] = v.Generation
		}
	}
	key := digest([]any{s.gitRoot(), r.ID, name, sha, r.EffectiveIndexMode(), r.PathGroups, generations})
	c := &s.pluginCache
	for {
		c.mu.Lock()
		if hit := c.entries[key]; hit != nil {
			hit.used = time.Now()
			c.mu.Unlock()
			return hit, nil
		}
		if done := c.loading[key]; done != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}
		if c.loading == nil {
			c.loading = map[string]chan struct{}{}
		}
		done := make(chan struct{})
		c.loading[key] = done
		c.mu.Unlock()
		snapshot := s.scanPlugins(ctx, name, sha, r)
		c.mu.Lock()
		// Transient discovery/blob failures are retried, never cached as absence.
		if ctx.Err() == nil && len(snapshot.catalog.Issues) == 0 {
			if c.entries == nil {
				c.entries = map[string]*pluginSnapshot{}
			}
			snapshot.used = time.Now()
			c.entries[key] = snapshot
			total := 0
			for _, v := range c.entries {
				total += v.bytes
			}
			for len(c.entries) > 64 || total > 128<<20 {
				oldest := ""
				var used time.Time
				for k, v := range c.entries {
					if oldest == "" || v.used.Before(used) {
						oldest = k
						used = v.used
					}
				}
				total -= c.entries[oldest].bytes
				delete(c.entries, oldest)
			}
		}
		delete(c.loading, key)
		close(done)
		c.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return snapshot, nil
	}
}
func (s *Service) scanPlugins(ctx context.Context, name, sha string, r control.Repository) *pluginSnapshot {
	out := &pluginSnapshot{catalog: PluginCatalog{Commit: sha, Plugins: []PluginInfo{}}, artifacts: map[string][]byte{}}
	seen := map[string]bool{}
	for _, root := range pluginRoots(r) {
		scope, err := readScope(r, sha, path.Join(root, "plugin-probe"))
		if err != nil {
			out.catalog.Issues = append(out.catalog.Issues, pluginDiscoveryIssue(root, err))
			continue
		}
		g, err := s.scopeStore(name, scope)
		if err != nil {
			out.catalog.Issues = append(out.catalog.Issues, pluginDiscoveryIssue(root, err))
			continue
		}
		paths, err := g.PluginPaths(ctx, name, sha, root, wasmplugin.Suffix)
		if err != nil {
			out.catalog.Issues = append(out.catalog.Issues, pluginDiscoveryIssue(root, err))
			continue
		}
		for _, p := range paths {
			if seen[p] {
				continue
			}
			seen[p] = true
			pluginName := strings.TrimSuffix(path.Base(p), wasmplugin.Suffix)
			if !wasmplugin.ValidName(pluginName) {
				continue
			}
			dir := path.Dir(p)
			if dir == "." {
				dir = ""
			}
			info := PluginInfo{Name: pluginName, Root: dir, Path: p}
			b, err := s.pluginBlob(ctx, name, sha, dir, path.Base(p), s.Plugins.Config.ArtifactBytes)
			if err != nil {
				out.catalog.Issues = append(out.catalog.Issues, wasmplugin.Issue{Code: "PLUGIN_UNAVAILABLE", Message: "Cannot read plugin: " + p})
			} else {
				info.Metadata, err = wasmplugin.Inspect(b, s.Plugins.Config.MetadataBytes)
				if err == nil {
					out.artifacts[p] = b
					out.bytes += len(b)
				}
			}
			if err != nil {
				info.Issues = []wasmplugin.Issue{{Code: "PLUGIN_UNAVAILABLE", Message: "Cannot read valid Wasm metadata at this path."}}
			}
			out.catalog.Plugins = append(out.catalog.Plugins, info)
		}
	}
	sort.Slice(out.catalog.Plugins, func(i, j int) bool { return out.catalog.Plugins[i].Path < out.catalog.Plugins[j].Path })
	return out
}

// One bounded worker owns prewarming in both local and production runtimes.
// Compilation does not instantiate a module or call repository business code.
func (s *Service) runPlugins(ctx context.Context) {
	if s.Plugins == nil || s.Plugins.Config.Disabled {
		return
	}
	prepare := func(name, sha string) {
		budget, cancel := context.WithTimeout(ctx, s.Plugins.Config.Timeout)
		defer cancel()
		release, err := s.pluginLease(budget)
		if err != nil {
			return
		}
		defer release()
		r, err := s.Repo(budget, name)
		if err != nil {
			return
		}
		snapshot, err := s.pluginCatalog(budget, name, sha, r)
		if err != nil {
			return
		}
		for _, info := range snapshot.catalog.Plugins {
			if info.Metadata.DefaultEnabled && len(info.Issues) == 0 {
				_ = s.Plugins.Precompile(budget, snapshot.artifacts[info.Path])
			}
		}
	}
	prepareHeads := func(r control.Repository) {
		if r.Deleted || !r.Enabled {
			return
		}
		heads := map[string]bool{}
		for _, sha := range r.Heads {
			heads[sha] = true
		}
		for _, scope := range r.Scopes {
			for _, sha := range scope.Heads {
				heads[sha] = true
			}
		}
		for sha := range heads {
			prepare(r.Name, sha)
		}
	}
	scan := func() {
		s.pluginCache.mu.Lock()
		for key, entry := range s.pluginCache.entries {
			if time.Since(entry.used) > 10*time.Minute {
				delete(s.pluginCache.entries, key)
			}
		}
		s.pluginCache.mu.Unlock()
		after := ""
		for ctx.Err() == nil {
			rows, err := s.DB.List(ctx, after, 100)
			if err != nil {
				return
			}
			for _, r := range rows {
				after = r.ID
				prepareHeads(r)
			}
			if len(rows) < 100 {
				return
			}
		}
	}
	scan()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-s.pluginSignal():
			if p.sha != "" {
				prepare(p.name, p.sha)
			} else if r, err := s.DB.Get(ctx, p.name); err == nil {
				prepareHeads(r)
			}
		case <-tick.C:
			scan()
		}
	}
}

// Expose a stable reason, not raw subprocess errors or private host paths.
func pluginDiscoveryIssue(root string, err error) wasmplugin.Issue {
	if root == "" {
		root = "."
	}
	reason := "GIT_OPERATION_FAILED"
	var problem *contract.Error
	if errors.As(err, &problem) {
		reason = problem.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		reason = "CANCELED"
	}
	return wasmplugin.Issue{Code: "PLUGIN_DISCOVERY_FAILED", Message: "Plugin discovery could not complete in scope " + root + " (" + reason + "). Refresh after repository synchronization completes."}
}
