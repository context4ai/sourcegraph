package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/indexer"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// expiredAge keeps a just-written manifest out of collection while its build
// is still publishing it.
const expiredAge = 5 * time.Minute

var manifestName = regexp.MustCompile(`^[0-9]+\.json$`)

// CollectionStatus describes the latest automatic index collection round.
// Reasons are fixed codes; paths and raw filesystem errors are never exposed.
type CollectionStatus struct {
	LastRunAt          time.Time `json:"last_run_at,omitempty"`
	LastReclaimAt      time.Time `json:"last_reclaim_at,omitempty"`
	RemovedGenerations int       `json:"removed_generations"`
	ReclaimedBytes     int64     `json:"reclaimed_bytes"`
	PendingGenerations int       `json:"pending_generations"`
	PendingBytes       int64     `json:"pending_bytes"`
	BusyRepositories   int       `json:"busy_repositories"`
	InvalidManifests   int       `json:"invalid_manifests"`
	LastError          string    `json:"last_error,omitempty"`
	LastErrorAt        time.Time `json:"last_error_at,omitempty"`
}

type collectionState struct {
	mu     sync.Mutex
	cursor int
	status CollectionStatus
}

// IndexCollection returns a copy of the latest automatic collection round.
func (s *Service) IndexCollection() CollectionStatus {
	s.collection.mu.Lock()
	defer s.collection.mu.Unlock()
	return s.collection.status
}

type repositoryArtifact struct {
	artifact indexer.Artifact
	bytes    int64
}

// Collect removes index generations that no retained version references.
// Each repository is collected under its own preparation slot, so a busy
// repository defers only its own cleanup and the round continues with the
// others until ctx expires. Readers cannot hold these generations: retention
// revoked them under the query lock before they became unreferenced.
func (s *Service) Collect(ctx context.Context) (CollectionStatus, error) {
	round := CollectionStatus{LastRunAt: time.Now().UTC()}
	byRepo, invalid, err := s.expiredCandidates(ctx)
	round.InvalidManifests = invalid
	if err != nil {
		return s.finishCollection(round, "manifest_scan_failed"), err
	}
	names := make([]string, 0, len(byRepo))
	for name := range byRepo {
		names = append(names, name)
	}
	sort.Strings(names)
	// Rotate the starting repository so a short budget still reaches everyone.
	s.collection.mu.Lock()
	start := 0
	if len(names) > 0 {
		start = s.collection.cursor % len(names)
		s.collection.cursor = start + 1
	}
	s.collection.mu.Unlock()
	reason := ""
	for i := range names {
		name := names[(start+i)%len(names)]
		if ctx.Err() != nil {
			reason = "round_budget_exhausted"
			break
		}
		release, ok := s.acquireRepository(name)
		if !ok {
			round.BusyRepositories++
			count, bytes := s.unreferenced(ctx, name, byRepo[name])
			round.PendingGenerations += count
			round.PendingBytes += bytes
			continue
		}
		res := s.retireUnreferenced(ctx, name, byRepo[name])
		release()
		round.RemovedGenerations += res.removed
		round.ReclaimedBytes += res.bytes
		round.PendingGenerations += res.pending
		round.PendingBytes += res.pendingBytes
		if res.err != nil && reason == "" {
			reason = collectionReason(res.err)
		}
	}
	if round.RemovedGenerations > 0 {
		round.LastReclaimAt = time.Now().UTC()
	}
	return s.finishCollection(round, reason), nil
}

// collectPendingGit finishes Git collection that a committed eviction or
// retention change left behind, e.g. after a restart or a failed job, so space
// is released without waiting for a successful synchronization. Only
// repositories whose retention marker is missing or names removed commits are
// touched; busy ones are retried next round. Each repository gets its Git
// timeout; ctx is the service lifetime.
func (s *Service) collectPendingGit(ctx context.Context) {
	if s.DB == nil || s.Git == nil {
		return
	}
	after := ""
	for ctx.Err() == nil {
		rows, err := s.DB.List(ctx, after, 100)
		if err != nil {
			return
		}
		for _, r := range rows {
			after = r.ID
			if ctx.Err() != nil || !r.Enabled || r.Deleted || !s.gitReclaimPending(r) {
				continue
			}
			release, ok := s.acquireRepository(r.Name)
			if !ok {
				continue
			}
			repoCtx, cancel := context.WithTimeout(ctx, time.Duration(s.gitTimeoutMinutes(r))*time.Minute)
			s.finishGitReclaim(repoCtx, r)
			cancel()
			release()
		}
		if len(rows) < 100 {
			return
		}
	}
}

func (s *Service) finishCollection(round CollectionStatus, reason string) CollectionStatus {
	s.collection.mu.Lock()
	previous := s.collection.status
	if round.LastReclaimAt.IsZero() {
		round.LastReclaimAt = previous.LastReclaimAt
	}
	round.LastError, round.LastErrorAt = previous.LastError, previous.LastErrorAt
	if reason != "" {
		round.LastError, round.LastErrorAt = reason, round.LastRunAt
	}
	s.collection.status = round
	s.collection.mu.Unlock()
	if round.RemovedGenerations > 0 || reason != "" {
		slog.Info("index_collection", "removed_generations", round.RemovedGenerations, "reclaimed_bytes", round.ReclaimedBytes, "pending_generations", round.PendingGenerations, "pending_bytes", round.PendingBytes, "busy_repositories", round.BusyRepositories, "invalid_manifests", round.InvalidManifests, "reason", reason)
	}
	return round
}

func collectionReason(err error) string {
	var ce *contract.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "round_budget_exhausted"
	case errors.As(err, &ce):
		return "registry_unavailable"
	default:
		return "remove_failed"
	}
}

// expiredCandidates groups readable manifests older than expiredAge by
// repository. Malformed manifests are counted and left in place.
func (s *Service) expiredCandidates(ctx context.Context) (map[string][]repositoryArtifact, int, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "manifests"))
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if len(entries) > 10000 {
		return nil, 0, errors.New("manifest scan budget exceeded")
	}
	result := map[string][]repositoryArtifact{}
	invalid := 0
	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		if entry.IsDir() || !manifestName.MatchString(entry.Name()) {
			continue
		}
		a, young, ok := s.readManifest(entry.Name())
		if !ok {
			invalid++
			continue
		}
		if young {
			continue
		}
		result[a.Target.Repo] = append(result[a.Target.Repo], repositoryArtifact{a, s.shardBytes(a)})
	}
	return result, invalid, nil
}

// readManifest validates one manifest and its shard names; young reports a
// manifest written within expiredAge, which is not read further.
func (s *Service) readManifest(name string) (a indexer.Artifact, young, ok bool) {
	p := filepath.Join(s.Root, "manifests", name)
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > indexer.MaxManifestBytes {
		return a, false, false
	}
	if time.Since(info.ModTime()) < expiredAge {
		return a, true, true
	}
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &a) != nil || a.Target.Validate() != nil || name != strconv.FormatUint(uint64(a.Target.RepositoryID), 10)+".json" {
		return indexer.Artifact{}, false, false
	}
	allowed := regexp.MustCompile(`^snapshot-` + strconv.FormatUint(uint64(a.Target.RepositoryID), 10) + `_[A-Za-z0-9_.-]+\.zoekt$`)
	for _, shard := range a.Shards {
		if !allowed.MatchString(shard) {
			return indexer.Artifact{}, false, false
		}
	}
	return a, false, true
}

func (s *Service) shardBytes(a indexer.Artifact) int64 {
	var bytes int64
	for _, shard := range a.Shards {
		if info, err := os.Lstat(filepath.Join(s.Root, "index", shard)); err == nil && info.Mode().IsRegular() {
			bytes += info.Size()
		}
	}
	return bytes
}

// referencedGenerations reads which generations retained versions reference.
// A repository missing from the registry references none.
func (s *Service) referencedGenerations(ctx context.Context, name string) (map[uint32]bool, bool, error) {
	r, err := s.DB.Get(ctx, name)
	if err != nil {
		var ce *contract.Error
		if errors.As(err, &ce) && ce.Code == "REPOSITORY_NOT_FOUND" {
			return map[uint32]bool{}, false, nil
		}
		return nil, false, err
	}
	referenced := map[uint32]bool{}
	for _, group := range append([]string{""}, r.WorkGroups()...) {
		for _, v := range r.Scope(group).Versions {
			if v.Generation != 0 {
				referenced[v.Generation] = true
			}
		}
	}
	return referenced, true, nil
}

// unreferenced counts expired generations of a repository that could not be
// collected this round.
func (s *Service) unreferenced(ctx context.Context, name string, candidates []repositoryArtifact) (int, int64) {
	referenced, _, err := s.referencedGenerations(ctx, name)
	if err != nil {
		return 0, 0
	}
	count, bytes := 0, int64(0)
	for _, c := range candidates {
		if !referenced[c.artifact.Target.RepositoryID] {
			count++
			bytes += c.bytes
		}
	}
	return count, bytes
}

type retireResult struct {
	removed, pending    int
	bytes, pendingBytes int64
	err                 error
}

// retireUnreferenced removes the given generations of one repository that no
// retained version references. The caller holds the repository's preparation
// slot, so no build of this repository can publish a generation meanwhile.
func (s *Service) retireUnreferenced(ctx context.Context, name string, candidates []repositoryArtifact) retireResult {
	var res retireResult
	if len(candidates) == 0 {
		return res
	}
	referenced, registered, err := s.referencedGenerations(ctx, name)
	if err != nil {
		res.err = err
		return res
	}
	for _, c := range candidates {
		if referenced[c.artifact.Target.RepositoryID] {
			continue
		}
		if res.err == nil && ctx.Err() != nil {
			res.err = ctx.Err()
		}
		if res.err == nil {
			res.err = s.removeGeneration(c.artifact)
			if res.err == nil {
				res.removed++
				res.bytes += c.bytes
				continue
			}
		}
		res.pending++
		res.pendingBytes += c.bytes
	}
	if res.removed > 0 && registered {
		// Refresh the repository's disk sample; a failure only delays the page value.
		update, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, _ = control.Mutate(update, s.DB, name, func(v *control.Repository) error {
			v.DiskUsagePending = true
			return nil
		})
		cancel()
		s.wakeDiskUsage()
	}
	return res
}

func (s *Service) removeGeneration(a indexer.Artifact) error {
	for _, shard := range a.Shards {
		if err := os.Remove(filepath.Join(s.Root, "index", shard)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	err := os.Remove(filepath.Join(s.Root, "manifests", strconv.FormatUint(uint64(a.Target.RepositoryID), 10)+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// reclaimRepository collects one repository's expired generations from inside
// its own preparation job, which already holds the repository slot.
func (s *Service) reclaimRepository(ctx context.Context, name string) (int, error) {
	artifacts, err := s.repositoryArtifacts(ctx, name)
	if err != nil {
		return 0, err
	}
	candidates := make([]repositoryArtifact, 0, len(artifacts))
	for _, a := range artifacts {
		info, err := os.Lstat(filepath.Join(s.Root, "manifests", strconv.FormatUint(uint64(a.Target.RepositoryID), 10)+".json"))
		if err != nil || time.Since(info.ModTime()) < expiredAge {
			continue
		}
		candidates = append(candidates, repositoryArtifact{a, s.shardBytes(a)})
	}
	res := s.retireUnreferenced(ctx, name, candidates)
	return res.removed, res.err
}
