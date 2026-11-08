package service

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

const (
	// snapshotLimit is the per-group version cap; evictionBatch bounds one eviction round.
	snapshotLimit = 4096
	evictionBatch = 64
)

// protectedCommits are never evicted: branch heads, pins and commits of queued or running jobs.
func protectedCommits(r control.Repository) map[string]bool {
	protected := map[string]bool{}
	for _, sha := range r.PinnedCommits {
		protected[sha] = true
	}
	for _, group := range r.WorkGroups() {
		for _, sha := range r.Scope(group).Heads {
			protected[sha] = true
		}
	}
	for _, j := range r.Jobs {
		if j.State == "queued" || j.State == "running" {
			protected[j.Commit] = true
			protected[j.RequestedRevision] = true
		}
	}
	return protected
}

func versionStamp(v control.Version) time.Time {
	if !v.ObservedAt.IsZero() {
		return v.ObservedAt
	}
	return v.Time
}

// trimSnapshots drops the oldest unprotected versions until at most max remain.
// ok is false when protected versions alone exceed max.
func trimSnapshots(versions []control.Version, protected map[string]bool, max int) ([]control.Version, bool) {
	if len(versions) <= max {
		return versions, true
	}
	order := make([]int, len(versions))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return versionStamp(versions[order[a]]).Before(versionStamp(versions[order[b]]))
	})
	excess := len(versions) - max
	drop := map[int]bool{}
	for _, i := range order {
		if excess == 0 {
			break
		}
		if !protected[versions[i].Commit] {
			drop[i] = true
			excess--
		}
	}
	out := make([]control.Version, 0, len(versions)-len(drop))
	for i, v := range versions {
		if !drop[i] {
			out = append(out, v)
		}
	}
	return out, excess == 0
}

// invalidateRetention runs inside a control-data update that is about to revoke
// versions of group, before it commits: Git collection then cannot be skipped as
// already done, even if the process stops before collecting. Its error must
// abort the update. Retried updates repeat it harmlessly.
func (s *Service) invalidateRetention(r control.Repository, group string) error {
	if s.Git == nil {
		return nil
	}
	git, _, err := s.groupGit(r, group)
	if err == nil {
		err = git.InvalidateRetention(r.Name)
	}
	if err != nil {
		slog.Warn("retention_marker", "repo", r.Name, "error", errorCode(err))
		return contract.Fail("RECLAIM_FAILED", "Snapshots could not be released safely; nothing was changed. Retry shortly.", 503)
	}
	return nil
}

// removesVersions reports whether next drops a commit that current holds.
func removesVersions(current, next []control.Version) bool {
	kept := map[string]bool{}
	for _, v := range next {
		kept[v.Commit] = true
	}
	for _, v := range current {
		if !kept[v.Commit] {
			return true
		}
	}
	return false
}

// evictSnapshots revokes up to n of the oldest unprotected versions across every
// path group in one control-data commit. It holds the query lock, so readers of
// those versions drain before any file is touched; physical collection follows.
func (s *Service) evictSnapshots(ctx context.Context, name string, n int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	_, err := control.Mutate(ctx, s.DB, name, func(v *control.Repository) error {
		removed = 0
		protected := protectedCommits(*v)
		oldest := map[string]time.Time{}
		for _, group := range v.WorkGroups() {
			for _, version := range v.Scope(group).Versions {
				if protected[version.Commit] {
					continue
				}
				at := versionStamp(version)
				if seen, ok := oldest[version.Commit]; !ok || at.Before(seen) {
					oldest[version.Commit] = at
				}
			}
		}
		commits := make([]string, 0, len(oldest))
		for sha := range oldest {
			commits = append(commits, sha)
		}
		sort.Slice(commits, func(i, j int) bool {
			if !oldest[commits[i]].Equal(oldest[commits[j]]) {
				return oldest[commits[i]].Before(oldest[commits[j]])
			}
			return commits[i] < commits[j]
		})
		if len(commits) > n {
			commits = commits[:n]
		}
		drop := map[string]bool{}
		for _, sha := range commits {
			drop[sha] = true
		}
		for _, group := range v.WorkGroups() {
			state := v.Scope(group)
			kept := make([]control.Version, 0, len(state.Versions))
			for _, version := range state.Versions {
				if !drop[version.Commit] {
					kept = append(kept, version)
				}
			}
			if len(kept) < len(state.Versions) {
				if err := s.invalidateRetention(*v, group); err != nil {
					return err
				}
			}
			state.Versions = kept
			v.SetScope(group, state)
		}
		removed = len(commits)
		return nil
	})
	return removed, err
}

func repositoryBudget(r control.Repository) int64 {
	if r.MaxStorageBytes > 0 {
		return r.MaxStorageBytes
	}
	return defaultRepositoryBudget
}

// repositoryUsage measures Git objects, published index shards and extra paths.
func (s *Service) repositoryUsage(ctx context.Context, r control.Repository, extra ...string) (int64, error) {
	path, err := gitstore.RepositoryDir(s.gitRoot(), r.Name)
	if err != nil {
		return 0, err
	}
	meter := diskMeter{seen: map[[2]uint64]bool{}}
	if err = meter.walk(ctx, filepath.Dir(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	artifacts, err := s.repositoryArtifacts(ctx, r.Name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	for _, a := range artifacts {
		for _, shard := range a.Shards {
			if err = meter.walk(ctx, filepath.Join(s.Root, "index", shard)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
		}
	}
	for _, path := range extra {
		if err = meter.walk(ctx, path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
	}
	return meter.bytes, nil
}

// indexReserve estimates the space a new index of group needs: 1.2 times its
// latest published generation. It is only an estimate; preparation itself is
// still bounded by the in-flight ceiling and the instance free-space reserve.
func (s *Service) indexReserve(ctx context.Context, r control.Repository, group string) int64 {
	artifacts, err := s.repositoryArtifacts(ctx, r.Name)
	if err != nil {
		return 0
	}
	var latest uint32
	var bytes int64
	for _, a := range artifacts {
		if a.Target.Group == group && a.Target.RepositoryID > latest {
			latest, bytes = a.Target.RepositoryID, s.shardBytes(a)
		}
	}
	return bytes + bytes/5
}

// ensureRepositoryBudget runs only at safe points inside the repository's own
// job (before Git or index I/O starts, or after publication). It frees room for
// reserve bytes by collecting expired indexes, then evicts the oldest unprotected
// snapshots in batches, measuring again after each batch.
func (s *Service) ensureRepositoryBudget(ctx context.Context, name string, reserve int64) error {
	reclaimed := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := s.DB.Get(ctx, name)
		if err != nil {
			return err
		}
		limit := repositoryBudget(r)
		used, err := s.repositoryUsage(ctx, r)
		if err != nil {
			return err
		}
		if used+reserve < limit {
			return nil
		}
		if !reclaimed {
			reclaimed = true
			if removed, err := s.reclaimRepository(ctx, name); err == nil && removed > 0 {
				continue
			}
		}
		versions := map[string]bool{}
		for _, group := range r.WorkGroups() {
			for _, v := range r.Scope(group).Versions {
				versions[v.Commit] = true
			}
		}
		per := used / int64(len(versions)+1)
		n := evictionBatch
		if per > 0 {
			if need := int((used+reserve-limit)/per) + 1; need < n {
				n = need
			}
		}
		evicted, err := s.evictSnapshots(ctx, name, n)
		if err != nil {
			return contract.Fail("RECLAIM_FAILED", "Older snapshots could not be released; collection continues in the background. Retry shortly.", 503)
		}
		if evicted == 0 {
			if used < limit {
				// Only the estimate does not fit; the in-flight ceiling still bounds preparation.
				return nil
			}
			return contract.Fail("REPOSITORY_STORAGE_LIMIT", "Protected snapshots (branch heads, pinned snapshots and active jobs) fill the repository storage budget; remove pins you no longer need or raise the budget.", 409)
		}
		slog.Info("snapshot_eviction", "repo", name, "evicted", evicted, "used_bytes", used, "limit_bytes", limit, "reserve_bytes", reserve)
		if err := s.collectRepositorySnapshots(ctx, name); err != nil {
			var ce *contract.Error
			if errors.As(err, &ce) && ce.Code == "INSUFFICIENT_STORAGE" {
				return err
			}
			return contract.Fail("RECLAIM_FAILED", "Released snapshots are not collected yet; collection continues in the background. Retry shortly.", 503)
		}
	}
}

// applyRetention re-applies the retention policy after a preparation or index
// task published its index instead of waiting for the next synchronization,
// then brings usage back under the storage budget and collects what left retention. Failures are left to the
// background collection round; the published index stays valid.
func (s *Service) applyRetention(ctx context.Context, name string) {
	s.mu.Lock()
	_, err := control.Mutate(ctx, s.DB, name, func(v *control.Repository) error {
		now := time.Now().UTC()
		for _, group := range v.WorkGroups() {
			state := v.Scope(group)
			if len(state.Versions) == 0 {
				continue
			}
			next := retainedSnapshots(*v, state.Versions, state.Heads, now)
			if removesVersions(state.Versions, next) {
				if err := s.invalidateRetention(*v, group); err != nil {
					return err
				}
			}
			state.Versions = next
			v.SetScope(group, state)
		}
		return nil
	})
	s.mu.Unlock()
	if err == nil {
		err = s.ensureRepositoryBudget(ctx, name, 0)
	}
	if err == nil {
		var r control.Repository
		if r, err = s.DB.Get(ctx, name); err == nil {
			s.finishGitReclaim(ctx, r)
		}
	}
	if err != nil {
		slog.Warn("retention_after_prepare", "repo", name, "error", errorCode(err))
	}
}

// gitReclaimPending reports whether a committed eviction or retention change
// still has Git objects to collect. It only reads retention markers.
// A group that cannot be inspected is logged and skipped, not reported as
// having nothing to collect; the next round checks it again.
func (s *Service) gitReclaimPending(r control.Repository) bool {
	for _, group := range r.WorkGroups() {
		git, _, err := s.groupGit(r, group)
		pending := false
		if err == nil {
			state := r.Scope(group)
			commits := make([]string, 0, len(state.Versions))
			for _, v := range state.Versions {
				commits = append(commits, v.Commit)
			}
			pending, err = git.RetentionPending(r.Name, commits)
		}
		if err != nil {
			slog.Warn("git_reclaim_check", "repo", r.Name, "group", group, "error", inspectionCode(err))
			continue
		}
		if pending {
			return true
		}
	}
	return false
}

// inspectionCode names why local repository files could not be inspected,
// without paths or raw system messages.
func inspectionCode(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return "permission_denied"
	}
	if code := errorCode(err); code != "internal" {
		return code
	}
	return "unreadable"
}

// finishGitReclaim completes Git collection that an interrupted run committed.
// Callers hold the repository's preparation slot.
func (s *Service) finishGitReclaim(ctx context.Context, r control.Repository) {
	if !s.gitReclaimPending(r) {
		return
	}
	if err := s.collectRepositorySnapshots(ctx, r.Name); err != nil {
		slog.Warn("git_reclaim", "repo", r.Name, "error", errorCode(err))
	}
}

func errorCode(err error) string {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "internal"
}
