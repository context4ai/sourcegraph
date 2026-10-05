package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/indexer"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Run owns one automatic worker, one manual worker and an independent scanner.
// Durable repository queues remain bounded; no work goroutine waits on a busy
// repository, and shutdown waits for both executors to release their leases.
func (s *Service) Run(ctx context.Context) {
	var workers sync.WaitGroup
	s.diskSignal()
	workers.Add(1)
	go func() { defer workers.Done(); s.runPlugins(ctx) }()
	workers.Add(1)
	go func() { defer workers.Done(); s.runDiskUsage(ctx) }()
	for _, source := range []string{control.SourceAutomatic, control.SourceManual} {
		workers.Add(1)
		go func(source string) { defer workers.Done(); s.runWorker(ctx, source) }(source)
	}
	defer workers.Wait()
	s.scan(ctx)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.scan(ctx)
			budget, stop := context.WithTimeout(ctx, 3*time.Second)
			_ = s.Collect(budget)
			stop()
		}
	}
}

func (s *Service) runWorker(ctx context.Context, source string) {
	after := ""
	for ctx.Err() == nil {
		worked := false
		if !s.StorageSettings().CleanupPending {
			rows, err := s.DB.List(ctx, after, 100)
			if err == nil {
				for _, r := range rows {
					after = r.ID
					// Finish the bounded head pipeline before moving to the next repository.
					for count := 0; count < 9 && ctx.Err() == nil; count++ {
						if !s.runNext(ctx, r.Name, source, s.prepare) {
							break
						}
						worked = true
					}
				}
				if len(rows) == 100 {
					continue
				}
			}
		}
		after = ""
		if worked {
			continue
		}
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// The local slot protects shared Git snapshot refs even when a cancelled job
// loses its durable lease. The database CAS additionally rejects live owners.
func (s *Service) acquireRepository(name string) (func(), bool) {
	if !s.preparation.TryRLock() {
		return nil, false
	}
	s.repositoryWorkMu.Lock()
	if s.repositoryWork[name] {
		s.repositoryWorkMu.Unlock()
		s.preparation.RUnlock()
		return nil, false
	}
	if s.repositoryWork == nil {
		s.repositoryWork = map[string]bool{}
	}
	s.repositoryWork[name] = true
	s.repositoryWorkMu.Unlock()
	return func() {
		s.repositoryWorkMu.Lock()
		delete(s.repositoryWork, name)
		s.repositoryWorkMu.Unlock()
		s.preparation.RUnlock()
	}, true
}

type preparationFunc func(context.Context, control.Repository, control.Job) error

func (s *Service) runNext(ctx context.Context, name, source string, prepare preparationFunc) bool {
	release, ok := s.acquireRepository(name)
	if !ok {
		return false
	}
	defer release()
	if s.StorageSettings().CleanupPending || ctx.Err() != nil {
		return false
	}
	var job control.Job
	claimed := false
	_, err := control.Mutate(ctx, s.DB, name, func(r *control.Repository) error {
		exhausted := false
		for _, pending := range r.Jobs {
			if pending.EffectiveSource() == source && pending.Attempt >= 3 && (pending.State == "queued" || (pending.State == "running" && !time.Now().Before(pending.LeaseUntil))) {
				exhausted = true
			}
		}
		job, claimed = r.ClaimSource(s.boot, source, time.Now().UTC())
		if !claimed && !exhausted {
			return errNoScheduledWork
		}
		return nil
	})
	if err != nil || !claimed {
		return false
	}
	s.executeClaimed(ctx, name, job, prepare)
	return true
}

func (s *Service) execute(parent context.Context, name string, j control.Job) {
	release, ok := s.acquireRepository(name)
	if !ok {
		return
	}
	defer release()
	s.executeClaimed(parent, name, j, s.prepare)
}

func (s *Service) prepare(ctx context.Context, r control.Repository, j control.Job) error {
	if j.Kind == "prepare" {
		return s.prepareRevision(ctx, r, j)
	}
	if j.Kind == "sync" {
		return s.sync(ctx, r, j)
	}
	if j.Kind == "index" {
		r = s.markStorage(r)
		if v, ok := r.ScopedVersion(j.Group, j.Commit); ok && v.Generation != 0 {
			return nil
		}
		return s.build(ctx, r, j)
	}
	return errors.New("unsupported preparation")
}

func (s *Service) executeClaimed(parent context.Context, name string, j control.Job, prepare preparationFunc) {
	if s.StorageSettings().CleanupPending {
		return
	}
	budget := 45 * time.Minute
	if j.Kind == "prepare" {
		budget = 16 * 45 * time.Minute
	} // up to 16 sequential path groups
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	s.repositoryWorkMu.Lock()
	if s.jobCancels == nil {
		s.jobCancels = map[string]context.CancelFunc{}
	}
	s.jobCancels[j.ID] = cancel
	s.repositoryWorkMu.Unlock()
	defer func() { s.repositoryWorkMu.Lock(); delete(s.jobCancels, j.ID); s.repositoryWorkMu.Unlock() }()
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		s.keepLease(ctx, cancel, name, j)
	}()

	r, e := s.DB.Get(ctx, name)
	if e == nil {
		if _, e = r.Owned(j, time.Now().UTC()); e == nil {
			e = prepare(ctx, r, j)
		}
	}
	if ctx.Err() != nil {
		e = ctx.Err()
	}
	defer func() { cancel(); <-renewDone }()
	if parent.Err() != nil {
		release, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = control.Mutate(release, s.DB, name, func(r *control.Repository) error { return r.Release(j, time.Now().UTC()) })
		return
	}
	finish, stop := context.WithTimeout(context.Background(), 25*time.Second)
	defer stop()
	code := ""
	if e != nil {
		code = "PREPARATION_FAILED"
		var problem *contract.Error
		if errors.As(e, &problem) && problem.Code != "" {
			// Persist only the structured public code, never transport errors,
			// repository output or credential-bearing diagnostic text.
			code = problem.Code
		} else if errors.Is(e, context.DeadlineExceeded) {
			code = "PREPARATION_TIMEOUT"
		}
	}
	finishErr := retryJobCompletion(finish, func(attempt context.Context) error {
		_, err := control.Mutate(attempt, s.DB, name, func(r *control.Repository) error {
			for _, existing := range r.Jobs {
				if existing.ID == j.ID && existing.Owner == j.Owner && existing.Fence == j.Fence && (existing.State == "succeeded" || existing.State == "failed") {
					return errCompletionRecorded
				}
			}
			now := time.Now().UTC()
			if err := r.Complete(j, code, now); err != nil {
				return err
			}
			if code == "" {
				r.DiskUsagePending = true
			}
			s.queueHeads(r, j.EffectiveSource(), now)
			r.Prune(now)
			return nil
		})
		if errors.Is(err, errCompletionRecorded) {
			return nil
		}
		return err
	})
	if finishErr != nil {
		logJobWriteFailure(name, j.ID, finishErr)
	}
	if code == "" && finishErr == nil {
		s.wakeDiskUsage()
		s.wakePlugins(name, j.Commit)
	}
}
func (s *Service) sync(ctx context.Context, r control.Repository, j control.Job) error {
	git, _, err := s.groupGit(r, j.Group)
	if err != nil {
		return err
	}
	var defaultBranch string
	e := s.withGitCredential(ctx, r, func(token string) error {
		var err error
		defaultBranch, err = git.FetchSelection(ctx, r.Name, gitstore.FetchOptions{DefaultBranch: r.Policy.DefaultBranch, CredentialHelper: s.Helper, Token: token, Branches: r.Policy.Branches, MinFreeBytes: s.MinFree})
		return err
	})
	if e != nil {
		return e
	}
	branches := append([]string(nil), r.Policy.Branches...)
	if defaultBranch != "" {
		found := false
		for _, b := range branches {
			if b == defaultBranch {
				found = true
			}
		}
		if !found {
			branches = append(branches, defaultBranch)
		}
	}
	heads := map[string]string{}
	versions := []control.Version{}
	seen := map[string]bool{}
	for _, b := range branches {
		history, e := git.History(ctx, r.Name, b, r.Policy.Days, r.Policy.Count, time.Now())
		if e != nil {
			return e
		}
		heads[b] = history[0].SHA
		for _, v := range history {
			if seen[v.SHA] {
				continue
			}
			seen[v.SHA] = true
			n := control.Version{Commit: v.SHA, Time: v.CommitterTime}
			if old, ok := r.ScopedVersion(j.Group, v.SHA); ok && r.StorageID == s.storageID() {
				n = old
			}
			versions = append(versions, n)
		}
	}
	if len(versions) > 4096 {
		return errors.New("combined retention window exceeds capacity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e = control.Mutate(ctx, s.DB, r.Name, func(v *control.Repository) error {
		owned, e := v.Owned(j, time.Now().UTC())
		if e != nil {
			return e
		}
		owned.NoChanges = v.StorageID == s.storageID() && v.SyncUnchanged(j.Group, heads)
		if v.StorageID != s.storageID() {
			// The first synchronized scope in a new storage directory invalidates
			// every old scope so remaining groups cannot advertise stale shards.
			v.Scopes = nil
		}
		v.StorageID = s.storageID()
		v.DefaultBranch = defaultBranch
		state := v.Scope(j.Group)
		state.ObservedAt = time.Now().UTC()
		state.Heads = heads
		state.Versions = versions
		v.SetScope(j.Group, state)
		return nil
	})
	return e
}
func (s *Service) build(ctx context.Context, r control.Repository, j control.Job) error {
	current := s.markStorage(r)
	if current.StorageMissing {
		return contract.Fail("REPOSITORY_STORAGE_MISSING", "Synchronize the repository after changing storage.", 409)
	}
	if _, ok := current.ScopedVersion(j.Group, j.Commit); !ok {
		return contract.Fail("REVISION_NOT_ELIGIBLE", "Commit is outside the current path group policy.", 404)
	}
	git, paths, e := s.groupGit(r, j.Group)
	if e != nil {
		return e
	}
	started := time.Now()
	if len(paths) > 0 {
		if e = s.withGitCredential(ctx, r, func(token string) error {
			return git.HydratePaths(ctx, r.Name, j.Commit, paths, gitstore.FetchOptions{CredentialHelper: s.Helper, Token: token, Branches: r.Policy.Branches, MinFreeBytes: s.MinFree})
		}); e != nil {
			return e
		}
	}
	id, e := s.DB.NextGeneration(ctx)
	if e != nil {
		return e
	}
	target := zoektclient.Target{Repo: r.Name, Commit: j.Commit, RepositoryID: id, Group: j.Group}
	var a indexer.Artifact
	opts := indexer.Options{SkipGenerated: !r.IndexGenerated, NoFollowLinks: r.IndexSymlinks != nil && !*r.IndexSymlinks}
	if len(paths) > 0 && !opts.NoFollowLinks {
		guard, err := git.ObjectGrowthGuard(r.Name, 256<<20)
		if err != nil {
			return err
		}
		opts.Hydrate = git.BudgetLinkHydrator(ctx, r.Name, j.Commit, func(entries []gitstore.Entry) error {
			return s.withGitCredential(ctx, r, func(token string) error {
				return git.HydrateEntries(ctx, r.Name, j.Commit, entries, gitstore.FetchOptions{CredentialHelper: s.Helper, Token: token, Branches: r.Policy.Branches, MinFreeBytes: s.MinFree, CheckGrowth: guard})
			})
		}, 256<<20)
	}
	if len(paths) > 0 {
		a, e = s.Builder.BuildPaths(ctx, target, paths, opts)
	} else {
		a, e = s.Builder.Build(ctx, target, opts)
	}
	if e != nil {
		return e
	}
	published := false
	defer func() {
		if !published {
			for _, name := range a.Shards {
				_ = os.Remove(filepath.Join(s.Root, "index", name))
			}
			_ = os.Remove(filepath.Join(s.Root, "manifests", fmt.Sprintf("%d.json", id)))
		}
	}()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		loaded, e := s.Engine.Loaded(ctx, target, len(a.Shards), a.Documents)
		if e == nil && loaded {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("engine load deadline")
		case <-tick.C:
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e = control.Mutate(ctx, s.DB, r.Name, func(v *control.Repository) error {
		if _, e := v.Owned(j, time.Now().UTC()); e != nil {
			return e
		}
		state := v.Scope(j.Group)
		for i := range state.Versions {
			if state.Versions[i].Commit == j.Commit {
				state.Versions[i].Generation = id
				state.Versions[i].LinkFormat = 1
				state.Versions[i].Files = a.Documents
				state.Versions[i].Shards = len(a.Shards)
				state.LastIndexedAt = time.Now().UTC()
				state.LastIndexDurationSeconds = time.Since(started).Seconds()
				v.SetScope(j.Group, state)
				return nil
			}
		}
		return errors.New("commit no longer eligible")
	})
	published = e == nil
	return e
}

func (s *Service) groupGit(r control.Repository, group string) (*gitstore.Store, []string, error) {
	paths, ok := r.GroupPaths(group)
	if !ok {
		return nil, nil, contract.Fail("INVALID_PATH_GROUP", "Choose a registered path group.", 400)
	}
	if group == "" {
		return s.Git, nil, nil
	}
	store, err := s.Git.ForGroup(r.Name, group)
	return store, paths, err
}
