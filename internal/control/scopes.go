package control

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

const (
	IndexModeFull     = "full"
	IndexModeMonorepo = "monorepo"
	DefaultPathGroup  = "default"
	MaxPathGroups     = 16
	MaxListenPaths    = 64
)

// PathGroup is a separately prepared snapshot of one or more repository directories.
// Names are safe durable map keys; paths always remain relative Git tree paths.
type PathGroup struct {
	Name  string   `json:"name" bson:"name"`
	Paths []string `json:"paths" bson:"paths"`
}

type ScopeState struct {
	Heads                    map[string]string `json:"heads" bson:"heads"`
	Versions                 []Version         `json:"versions" bson:"versions"`
	ObservedAt               time.Time         `json:"observed_at" bson:"observed_at"`
	LastIndexedAt            time.Time         `json:"last_indexed_at,omitempty,omitzero" bson:"last_indexed_at,omitempty"`
	LastIndexDurationSeconds float64           `json:"last_index_duration_seconds,omitempty" bson:"last_index_duration_seconds,omitempty"`
}

var groupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func NormalizeIndexConfig(mode string, groups []PathGroup) (string, []PathGroup, error) {
	if mode == "" {
		mode = IndexModeFull
	}
	if mode != IndexModeFull && mode != IndexModeMonorepo {
		return "", nil, contract.Fail("INVALID_INDEX_MODE", "Choose full or monorepo indexing.", 400)
	}
	if mode == IndexModeFull {
		if len(groups) != 0 {
			return "", nil, contract.Fail("INVALID_PATH_GROUPS", "Full indexing does not accept path groups.", 400)
		}
		return mode, nil, nil
	}
	if len(groups) < 1 || len(groups) > MaxPathGroups {
		return "", nil, contract.Fail("INVALID_PATH_GROUPS", "Choose 1..16 path groups.", 400)
	}
	out := make([]PathGroup, 0, len(groups))
	names := map[string]bool{}
	total := 0
	for _, group := range groups {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			name = DefaultPathGroup
		}
		if !groupName.MatchString(name) || names[name] {
			return "", nil, contract.Fail("INVALID_PATH_GROUPS", "Use unique group names containing letters, digits, underscores or hyphens (up to 64 characters).", 400)
		}
		names[name] = true
		total += len(group.Paths)
		if total > MaxListenPaths {
			return "", nil, contract.Fail("INVALID_PATH_GROUPS", "Choose at most 64 listen directories.", 400)
		}
		paths := make([]string, 0, len(group.Paths))
		for _, input := range group.Paths {
			p := strings.TrimSuffix(strings.TrimSpace(input), "/")
			if gitstore.ValidatePath(p, false) != nil {
				return "", nil, contract.Fail("INVALID_PATH_GROUPS", "Listen paths must be relative directories without traversal or wildcards.", 400)
			}
			for _, part := range strings.Split(p, "/") {
				if strings.EqualFold(part, ".git") || strings.ContainsAny(part, "*?[]") {
					return "", nil, contract.Fail("INVALID_PATH_GROUPS", "Listen paths must not contain .git or wildcard segments.", 400)
				}
			}
			paths = append(paths, p)
		}
		if len(paths) == 0 {
			return "", nil, contract.Fail("INVALID_PATH_GROUPS", "Every group needs at least one directory.", 400)
		}
		sort.Strings(paths)
		compact := paths[:0]
		for _, p := range paths {
			covered := false
			for _, parent := range compact {
				if PathContains(parent, p) {
					covered = true
					break
				}
			}
			if !covered {
				compact = append(compact, p)
			}
		}
		out = append(out, PathGroup{Name: name, Paths: compact})
	}
	for i, a := range out {
		for _, b := range out[i+1:] {
			for _, p := range a.Paths {
				for _, q := range b.Paths {
					if PathContains(p, q) || PathContains(q, p) {
						return "", nil, contract.Fail("OVERLAPPING_PATH_GROUPS", "Overlapping directories must belong to the same group.", 400)
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return mode, out, nil
}

func PathContains(directory, path string) bool {
	return directory == path || strings.HasPrefix(path, directory+"/")
}
func (r Repository) EffectiveIndexMode() string {
	if r.IndexMode == "" {
		return IndexModeFull
	}
	return r.IndexMode
}
func (r Repository) WorkGroups() []string {
	if r.EffectiveIndexMode() == IndexModeFull {
		return []string{""}
	}
	out := make([]string, 0, len(r.PathGroups))
	for _, group := range r.PathGroups {
		out = append(out, group.Name)
	}
	return out
}
func (r Repository) GroupPaths(group string) ([]string, bool) {
	if r.EffectiveIndexMode() == IndexModeFull {
		return nil, group == ""
	}
	for _, value := range r.PathGroups {
		if value.Name == group {
			return value.Paths, true
		}
	}
	return nil, false
}
func (r Repository) Scope(group string) ScopeState {
	if group == "" && r.EffectiveIndexMode() == IndexModeFull {
		return ScopeState{Heads: r.Heads, Versions: r.Versions, ObservedAt: r.ObservedAt, LastIndexedAt: r.LastIndexedAt, LastIndexDurationSeconds: r.LastIndexDurationSeconds}
	}
	return r.Scopes[group]
}
func (r *Repository) SetScope(group string, state ScopeState) {
	if group == "" && r.EffectiveIndexMode() == IndexModeFull {
		r.Heads, r.Versions, r.ObservedAt = state.Heads, state.Versions, state.ObservedAt
		r.LastIndexedAt, r.LastIndexDurationSeconds = state.LastIndexedAt, state.LastIndexDurationSeconds
		return
	}
	if r.Scopes == nil {
		r.Scopes = map[string]ScopeState{}
	}
	r.Scopes[group] = state
}
func (r Repository) ScopedVersion(group, sha string) (Version, bool) {
	for _, v := range r.Scope(group).Versions {
		if v.Commit == sha {
			return v, true
		}
	}
	return Version{}, false
}
func (r Repository) MaxActiveJobs() int {
	if r.EffectiveIndexMode() == IndexModeMonorepo && len(r.PathGroups) > 8 {
		return len(r.PathGroups)
	}
	return 8
}

// IndexTimingNote describes the last completed build, never an ETA promise.
func IndexTimingNote(state ScopeState) string {
	if state.LastIndexedAt.IsZero() || state.LastIndexDurationSeconds <= 0 {
		return ""
	}
	return fmt.Sprintf("上次的索引时间是 %.2f s，仅供参考。", state.LastIndexDurationSeconds)
}
