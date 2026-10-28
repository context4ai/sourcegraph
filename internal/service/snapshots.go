package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

const defaultRepositoryBudget int64 = 100 << 30

func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
func retainedSnapshots(r control.Repository, versions []control.Version, heads map[string]string, now time.Time) []control.Version {
	mandatory := map[string]bool{}
	for _, sha := range r.PinnedCommits {
		mandatory[sha] = true
	}
	for _, sha := range heads {
		mandatory[sha] = true
	}
	for _, j := range r.Jobs {
		if j.State == "queued" || j.State == "running" {
			mandatory[j.Commit] = true
			mandatory[j.RequestedRevision] = true
		}
	}
	values := append([]control.Version(nil), versions...)
	stamp := func(v control.Version) time.Time {
		if !v.ObservedAt.IsZero() {
			return v.ObservedAt
		}
		return v.Time
	}
	sort.SliceStable(values, func(i, j int) bool { return stamp(values[i]).After(stamp(values[j])) })
	result := []control.Version{}
	seen := map[string]bool{}
	ordinary := 0
	for _, v := range values {
		if seen[v.Commit] {
			continue
		}
		seen[v.Commit] = true
		retain := mandatory[v.Commit]
		if !retain {
			if r.Policy.Count > 0 {
				retain = ordinary < r.Policy.Count
			} else {
				retain = !stamp(v).Before(now.Add(-time.Duration(r.Policy.Days) * 24 * time.Hour))
			}
			if retain {
				ordinary++
			}
		}
		if retain {
			result = append(result, v)
		}
	}
	return result
}

// Workers own the repository preparation slot. The short query barrier drains
// readers of revoked versions. Git keeps every currently readable snapshot rooted
// while atomically replacing packs, so repacking does not block other repositories.
func (s *Service) collectRepositorySnapshots(ctx context.Context, name string) error {
	s.mu.Lock()
	r, err := s.DB.Get(ctx, name)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	for _, group := range r.WorkGroups() {
		state := r.Scope(group)
		if len(state.Versions) == 0 {
			continue
		}
		commits := make([]string, 0, len(state.Versions))
		for _, v := range state.Versions {
			commits = append(commits, v.Commit)
		}
		git, _, err := s.groupGit(r, group)
		if err != nil {
			return err
		}
		if err = git.RetainSnapshots(ctx, name, commits, func() error { return git.CheckFree(s.MinFree) }); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) checkRepositoryBudget(ctx context.Context, r control.Repository, extra ...string) error {
	limit := r.MaxStorageBytes
	if limit == 0 {
		limit = defaultRepositoryBudget
	}
	path, err := gitstore.RepositoryDir(s.gitRoot(), r.Name)
	if err != nil {
		return err
	}
	meter := diskMeter{seen: map[[2]uint64]bool{}}
	if err = meter.walk(ctx, filepath.Dir(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	artifacts, err := s.repositoryArtifacts(ctx, r.Name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, a := range artifacts {
		for _, shard := range a.Shards {
			if err = meter.walk(ctx, filepath.Join(s.Root, "index", shard)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for _, path := range extra {
		if err = meter.walk(ctx, path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if meter.bytes >= limit {
		return contract.Fail("REPOSITORY_STORAGE_LIMIT", "Repository storage budget reached; release unused pinned snapshots or increase its budget.", 409)
	}
	return nil
}
