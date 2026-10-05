package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/identity"
	"github.com/context4ai/sourcegraph/internal/indexer"
)

type KeptIndex struct {
	Group  string `json:"group"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}
type MaintenancePreview struct {
	Preview           string      `json:"preview"`
	Revision          int64       `json:"revision"`
	Keep              []KeptIndex `json:"keep"`
	RemoveGenerations int         `json:"remove_generations"`
	RemoveShards      int         `json:"remove_shards"`
	ReclaimBytes      int64       `json:"reclaim_bytes"`
	GitBytes          int64       `json:"git_bytes"`
	artifacts         []indexer.Artifact
	keepGenerations   map[uint32]bool
}

// The registry changes before bytes are unlinked. Readers already holding mu
// finish first; subsequent readers can never acquire a retired generation.
func (s *Service) PreviewMaintenance(ctx context.Context, name, action string) (MaintenancePreview, error) {
	s.preparation.RLock()
	defer s.preparation.RUnlock()
	r, err := s.DB.Get(ctx, name)
	if err != nil {
		return MaintenancePreview{}, err
	}
	return s.maintenancePlan(ctx, r, action)
}
func (s *Service) maintenancePlan(ctx context.Context, r control.Repository, action string) (MaintenancePreview, error) {
	p := MaintenancePreview{Revision: r.Revision, Keep: []KeptIndex{}, keepGenerations: map[uint32]bool{}}
	if action != "cleanup" && action != "delete" {
		return p, contract.Fail("INVALID_ACTION", "Choose cleanup or delete.", 400)
	}
	if action == "cleanup" {
		if r.Deleted || s.markStorage(r).StorageMissing {
			return p, contract.Fail("REPOSITORY_STORAGE_MISSING", "Synchronize the repository before cleanup.", 409)
		}
		branches := monitoredBranches(r)
		if len(branches) == 0 || (r.Policy.DefaultBranch && r.DefaultBranch == "") {
			return p, contract.Fail("LATEST_INDEX_NOT_READY", "Synchronize and index each monitored branch before cleanup.", 409)
		}
		alignedHeads := map[string]string{}
		for _, group := range r.WorkGroups() {
			scope := r.Scope(group)
			for _, branch := range branches {
				sha := scope.Heads[branch]
				if observed := alignedHeads[branch]; observed != "" && observed != sha {
					return p, contract.Fail("LATEST_INDEX_NOT_READY", "Synchronize every path group to the same branch head before cleanup.", 409)
				}
				alignedHeads[branch] = sha
				v, ok := r.ScopedVersion(group, sha)
				if sha == "" || !ok || v.Generation == 0 {
					return p, contract.Fail("LATEST_INDEX_NOT_READY", "Index the latest commit for every monitored branch and path group before cleanup.", 409)
				}
				p.Keep = append(p.Keep, KeptIndex{group, branch, sha})
				p.keepGenerations[v.Generation] = true
			}
		}
	}
	for _, location := range []struct{ root, subdir string }{{s.Root, "index"}, {s.Root, "manifests"}, {s.gitRoot(), "repos"}} {
		root, err := filepath.EvalSymlinks(location.root)
		if err != nil {
			return p, err
		}
		dir := filepath.Join(root, location.subdir)
		real, err := filepath.EvalSymlinks(dir)
		if err == nil && real != dir {
			return p, errors.New("unsafe repository storage path")
		}
		if err != nil && !os.IsNotExist(err) {
			return p, err
		}
	}
	artifacts, err := s.repositoryArtifacts(ctx, r.Name)
	if err != nil {
		return p, err
	}
	foundKept := map[uint32]bool{}
	for _, a := range artifacts {
		if p.keepGenerations[a.Target.RepositoryID] {
			if len(a.Shards) == 0 {
				return p, contract.Fail("LATEST_INDEX_NOT_READY", "Latest index files are missing; rebuild before cleanup.", 409)
			}
			for _, shard := range a.Shards {
				info, err := os.Lstat(filepath.Join(s.Root, "index", shard))
				if err != nil || !info.Mode().IsRegular() {
					return p, contract.Fail("LATEST_INDEX_NOT_READY", "Latest index files are missing; rebuild before cleanup.", 409)
				}
			}
			foundKept[a.Target.RepositoryID] = true
			continue
		}
		p.artifacts = append(p.artifacts, a)
		p.RemoveGenerations++
		for _, name := range a.Shards {
			info, err := os.Lstat(filepath.Join(s.Root, "index", name))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return p, err
			}
			if !info.Mode().IsRegular() {
				return p, errors.New("invalid shard file")
			}
			p.RemoveShards++
			p.ReclaimBytes += info.Size()
		}
	}
	for id := range p.keepGenerations {
		if !foundKept[id] {
			return p, contract.Fail("LATEST_INDEX_NOT_READY", "Latest index manifest is missing; rebuild before cleanup.", 409)
		}
	}
	if action == "delete" {
		path, err := gitstore.RepositoryDir(s.gitRoot(), r.Name)
		if err != nil {
			return p, err
		}
		err = filepath.WalkDir(filepath.Dir(path), func(_ string, entry os.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.Type().IsRegular() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				p.GitBytes += info.Size()
			}
			return nil
		})
		if err != nil {
			return p, err
		}
	}
	// Include physical inventory so an interrupted operation can be previewed and
	// retried without claiming that already removed bytes are still present.
	p.Preview = digest([]any{action, r.ID, r.Revision, p.Keep, p.artifacts, p.RemoveShards, p.ReclaimBytes, p.GitBytes})
	return p, nil
}
func (s *Service) repositoryArtifacts(ctx context.Context, repo string) ([]indexer.Artifact, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "manifests"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > 10000 {
		return nil, errors.New("manifest scan budget exceeded")
	}
	result := []indexer.Artifact{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !regexp.MustCompile(`^[0-9]+\.json$`).MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(s.Root, "manifests", entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > indexer.MaxManifestBytes {
			return nil, errors.New("invalid generation manifest")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var artifact indexer.Artifact
		if json.Unmarshal(data, &artifact) != nil || artifact.Target.Validate() != nil || entry.Name() != strconv.FormatUint(uint64(artifact.Target.RepositoryID), 10)+".json" {
			return nil, errors.New("invalid generation manifest")
		}
		if artifact.Target.Repo != repo {
			continue
		}
		valid := regexp.MustCompile(`^snapshot-` + strconv.FormatUint(uint64(artifact.Target.RepositoryID), 10) + `_[A-Za-z0-9_.-]+\.zoekt$`)
		for _, shard := range artifact.Shards {
			if !valid.MatchString(shard) {
				return nil, errors.New("invalid shard manifest path")
			}
		}
		result = append(result, artifact)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Target.RepositoryID < result[j].Target.RepositoryID })
	return result, nil
}
func (s *Service) Maintenance(ctx context.Context, name, action, preview string, revision int64) (map[string]any, error) {
	// Plugin reads release the query mutex while retaining their snapshot lease.
	if !s.pluginSnapshots.TryLock() {
		return nil, contract.Fail("REPOSITORY_BUSY", "Reading enhancements are active; retry shortly.", 409)
	}
	defer s.pluginSnapshots.Unlock()
	release, ok := s.acquireRepository(name)
	if !ok {
		return nil, contract.Fail("REPOSITORY_BUSY", "Stop the running task and retry after it finishes stopping.", 409)
	}
	defer release()
	r, err := s.DB.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	for _, job := range r.Jobs {
		if job.State == "running" && time.Now().Before(job.LeaseUntil) {
			return nil, contract.Fail("REPOSITORY_BUSY", "Stop the running task and retry after its lease is released.", 409)
		}
	}
	p, err := s.maintenancePlan(ctx, r, action)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	current, err := s.DB.Get(ctx, name)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if current.Revision != r.Revision {
		s.mu.Unlock()
		return nil, contract.Fail("REVISION_CONFLICT", "Repository changed; preview again.", 412)
	}
	if r.Revision != revision || p.Preview != preview {
		s.mu.Unlock()
		return nil, contract.Fail("REVISION_CONFLICT", "Maintenance preview expired; reload and confirm again.", 412)
	}
	if action == "delete" {
		if _, ok := s.DB.(interface {
			Delete(context.Context, control.Repository) error
		}); !ok {
			s.mu.Unlock()
			return nil, contract.Fail("CLEANUP_UNAVAILABLE", "Permanent repository deletion is unavailable.", 503)
		}
		r.Enabled = false
		r.Deleted = true
		r.PolicyRevision++
	}
	now := time.Now().UTC()
	for _, group := range r.WorkGroups() {
		scope := r.Scope(group)
		for i := range scope.Versions {
			if !p.keepGenerations[scope.Versions[i].Generation] {
				scope.Versions[i].Generation = 0
				scope.Versions[i].Files = 0
				scope.Versions[i].Shards = 0
				scope.Versions[i].Indexed = false
			}
		}
		r.SetScope(group, scope)
	}
	for i := range r.Jobs {
		j := &r.Jobs[i]
		if j.State != "queued" && j.State != "running" {
			continue
		}
		keep := false
		if action == "cleanup" {
			if j.Kind == "sync" {
				keep = true
			}
			for _, v := range p.Keep {
				if v.Group == j.Group && v.Commit == j.Commit {
					keep = true
				}
			}
		}
		if !keep {
			j.State = "cancelled"
			j.Stage = "complete"
			j.Error = "REPOSITORY_" + map[string]string{"delete": "DELETED", "cleanup": "CLEANED"}[action]
			j.Updated = now
			j.LeaseUntil = time.Time{}
		}
	}
	r.DiskUsagePending = action == "cleanup"
	r.Revision++
	r.Updated = now
	r.Audit = append(r.Audit, control.Audit{Actor: identity.Actor(ctx), Action: action, At: now})
	if len(r.Audit) > 256 {
		r.Audit = r.Audit[len(r.Audit)-256:]
	}
	err = s.DB.Replace(ctx, r, revision)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// All prior readers have completed and the CAS revoked every removed index.
	// Slow filesystem work must not hold the global query mutex.
	for _, artifact := range p.artifacts {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		for _, shard := range artifact.Shards {
			if err = os.Remove(filepath.Join(s.Root, "index", shard)); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
		if err = os.Remove(filepath.Join(s.Root, "manifests", strconv.FormatUint(uint64(artifact.Target.RepositoryID), 10)+".json")); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if action == "delete" {
		path, err := gitstore.RepositoryDir(s.gitRoot(), name)
		if err != nil {
			return nil, err
		}
		if err = os.RemoveAll(filepath.Dir(path)); err != nil {
			return nil, err
		}
		db := s.DB.(interface {
			Delete(context.Context, control.Repository) error
		})
		if err = db.Delete(ctx, r); err != nil {
			return nil, err
		}
	}
	if action == "cleanup" {
		s.wakeDiskUsage()
	}
	return map[string]any{"cleaned": action == "cleanup", "deleted": action == "delete", "removed_generations": p.RemoveGenerations, "removed_shards": p.RemoveShards, "reclaimed_bytes": p.ReclaimBytes + p.GitBytes, "keep": p.Keep}, nil
}
