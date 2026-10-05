package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/context4ai/sourcegraph/internal/control"
)

var errNoScheduledWork = errors.New("no scheduled preparation")

// A separate scanner revisits every repository once a minute. It only records
// bounded intent; slow fetches and indexing never delay this scan.
func (s *Service) scan(ctx context.Context) {
	if s.StorageSettings().CleanupPending {
		return
	}
	after := ""
	for ctx.Err() == nil {
		rows, err := s.DB.List(ctx, after, 100)
		if err != nil || len(rows) == 0 {
			return
		}
		for _, r := range rows {
			after = r.ID
			s.schedule(ctx, r)
		}
		if len(rows) < 100 {
			return
		}
	}
}

func (s *Service) schedule(ctx context.Context, r control.Repository) bool {
	return s.scheduleAt(ctx, r, time.Now().UTC())
}

func (s *Service) syncDue(r control.Repository, now time.Time) bool {
	for _, group := range r.WorkGroups() {
		if s.scopeSyncDue(r, group, now) {
			return true
		}
	}
	return false
}

func (s *Service) scopeSyncDue(r control.Repository, group string, now time.Time) bool {
	at, eligible := s.scopeSyncTime(r, group)
	return eligible && !now.Before(at)
}

// Shared by the scheduler and catalog: this is eligibility time, not a promise
// that a worker starts at that second (the scanner runs once a minute).
func (s *Service) scopeSyncTime(r control.Repository, group string) (time.Time, bool) {
	if !r.Enabled || r.Deleted {
		return time.Time{}, false
	}
	var next time.Time
	interval := time.Duration(r.SyncInterval()) * time.Minute
	extend := func(at time.Time) {
		if at.After(next) {
			next = at
		}
	}
	hasSync := false
	if check, ok := r.SyncChecks[group]; ok && check.PolicyRevision == r.PolicyRevision && check.StorageID == s.storageID() {
		hasSync = true
		extend(check.StartedAt.Add(interval))
	}

	for _, j := range r.Jobs {
		if j.Group != group || (j.PolicyRevision != 0 && j.PolicyRevision != r.PolicyRevision) {
			continue
		}
		if j.Kind == "sync" && j.State == "cancelled" && j.Error == "CANCELLED_BY_USER" {
			extend(j.Updated.Add(interval))
		}
		if j.State == "cancelled" && j.Error == "SYNC_INTERVAL_CHANGED" {
			continue
		}
		if j.State == "queued" || j.State == "running" {
			return time.Time{}, false
		}
		if j.Kind == "sync" && (r.StorageID == s.storageID() || !j.Created.Before(s.StorageSettings().UpdatedAt)) {
			hasSync = true
			extend(j.Created.Add(interval))
		}
	}
	if !hasSync && r.StorageID == s.storageID() {
		if at := r.Scope(group).ObservedAt; !at.IsZero() {
			extend(at.Add(interval))
		}
	}
	return next, true
}

func (s *Service) nextSyncTime(r control.Repository) *time.Time {
	var earliest *time.Time
	for _, group := range r.WorkGroups() {
		at, eligible := s.scopeSyncTime(r, group)
		if !eligible {
			continue
		}
		if at.IsZero() {
			at = time.Now().UTC()
		}
		if earliest == nil || at.Before(*earliest) {
			value := at
			earliest = &value
		}
	}
	return earliest
}

func (s *Service) scheduleAt(ctx context.Context, r control.Repository, now time.Time) bool {
	if !s.syncDue(r, now) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := control.Mutate(ctx, s.DB, r.Name, func(v *control.Repository) error {
		// Recheck the current interval and active jobs inside the repository CAS.
		// A settings save therefore takes effect on the next scan.
		if !s.syncDue(*v, now) {
			return errNoScheduledWork
		}
		queued := false
		for _, group := range v.WorkGroups() {
			if !s.scopeSyncDue(*v, group, now) {
				continue
			}
			key := "auto-sync-" + group + "-" + strconv.FormatInt(now.Unix()/60, 10) + "-" + s.storageID()
			if _, err := v.QueueScoped("sync", "", group, key, "", "scheduler", control.SourceAutomatic, now); err != nil {
				// A full bounded queue retains its current work. Other scopes are
				// revisited next minute rather than expanding the backlog.
				if queued {
					return nil
				}
				return err
			}
			queued = true
		}
		if !queued {
			return errNoScheduledWork
		}
		return nil
	})
	return err == nil
}

// Every completion refills missing head work as queue capacity becomes free.
// The successful sync remains the source of the pipeline even when a manual
// historical index is the task that releases its next slot.
func (s *Service) queueHeads(r *control.Repository, source string, now time.Time) {
	if r.StorageID != s.storageID() {
		return
	}
	for _, group := range r.WorkGroups() {
		s.queueScopeHeads(r, group, source, now)
	}
}

func (s *Service) queueScopeHeads(r *control.Repository, group, source string, now time.Time) {
	state := r.Scope(group)
	var lastSync time.Time
	if check, ok := r.SyncChecks[group]; ok && check.PolicyRevision == r.PolicyRevision && check.StorageID == s.storageID() && !check.SuccessStartedAt.Before(s.StorageSettings().UpdatedAt) {
		lastSync, source = check.SuccessStartedAt, check.SuccessSource
	}

	for i := len(r.Jobs) - 1; i >= 0; i-- {
		j := r.Jobs[i]
		if j.Group == group && j.Kind == "sync" && j.State == "succeeded" && j.PolicyRevision == r.PolicyRevision && !j.Created.Before(s.StorageSettings().UpdatedAt) && !j.Created.Before(lastSync) {
			source = j.EffectiveSource()
			lastSync = j.Created
			break
		}
	}
	seen := map[string]bool{}
	branches := append([]string(nil), r.Policy.Branches...)
	if r.Policy.DefaultBranch && r.DefaultBranch != "" {
		branches = append(branches, r.DefaultBranch)
	}
	for _, branch := range branches {
		head := state.Heads[branch]
		v, ok := r.ScopedVersion(group, head)
		if !ok || v.Generation != 0 || seen[head] {
			continue
		}
		seen[head] = true
		attempted := false
		cancelledID := ""
		failedID := ""
		failures := 0
		for _, j := range r.Jobs {
			if j.Group != group || j.Kind != "index" || j.Commit != head || j.Created.Before(s.StorageSettings().UpdatedAt) {
				continue
			}
			if j.State == "failed" && j.PolicyRevision == r.PolicyRevision {
				failures++
				if failedID == "" || j.Updated.Before(lastSync) {
					failedID = j.ID
				}
				if j.Updated.Before(lastSync) {
					continue
				}
			}
			if currentHeadAttempt(j, r.PolicyRevision) {
				attempted = true
				break
			}
			if j.State == "cancelled" && j.PolicyRevision == r.PolicyRevision {
				cancelledID = j.ID
			}
		}
		if attempted || failures >= 3 {
			continue
		}
		key := "head-" + group + "-" + head + "-" + s.storageID() + "-policy-" + strconv.FormatInt(r.PolicyRevision, 10)
		if failedID != "" {
			key += "-after-failure-" + failedID
		}
		if cancelledID != "" {
			key += "-after-" + cancelledID
		}
		if _, err := r.QueueScoped("index", head, group, key, digest([]string{"index", head, group}), "worker", source, now); err != nil {
			return
		}
	}
}

// Queued legacy jobs have no captured policy revision until their first claim.
// Policy updates cancel queued work, so a surviving queued legacy job is current.
func currentHeadAttempt(j control.Job, revision int64) bool {
	switch j.State {
	case "queued":
		return j.PolicyRevision == 0 || j.PolicyRevision == revision
	case "running", "succeeded", "failed":
		return j.PolicyRevision == revision
	default:
		return false
	}
}
