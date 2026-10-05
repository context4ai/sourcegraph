package control

import (
	"github.com/context4ai/sourcegraph/internal/contract"
	"time"
)

const LeaseDuration = 45 * time.Second

func (r *Repository) Claim(owner string, now time.Time) (Job, bool) {
	return r.ClaimSource(owner, "", now)
}

// ClaimSource is committed by repository CAS. A live job excludes every other
// source, including a worker on another process recovering durable intent.
func (r *Repository) ClaimSource(owner, source string, now time.Time) (Job, bool) {
	if !r.Enabled || r.Deleted {
		return Job{}, false
	}
	for _, j := range r.Jobs {
		if j.State == "running" && now.Before(j.LeaseUntil) {
			return Job{}, false
		}
	}
	for i := range r.Jobs {
		j := &r.Jobs[i]
		if source != "" && j.EffectiveSource() != source {
			continue
		}
		if j.State != "queued" && !(j.State == "running" && !now.Before(j.LeaseUntil)) {
			continue
		}
		if j.State == "running" {
			j.RecoveryReason = "LEASE_EXPIRED"
		}
		if j.Attempt >= 3 {
			j.State = "failed"
			j.Stage = "recovery"
			j.Error = "RECOVERY_EXHAUSTED"
			j.Updated = now
			continue
		}
		j.State = "running"
		j.Stage = "starting"
		j.Owner = owner
		j.Fence++
		j.Attempt++
		j.PolicyRevision = r.PolicyRevision
		j.LeaseUntil = now.Add(LeaseDuration)
		j.Updated = now
		j.StartedAt = now
		j.DurationSeconds = 0
		return *j, true
	}
	return Job{}, false
}
func (r *Repository) Owned(job Job, now time.Time) (*Job, error) {
	for i := range r.Jobs {
		j := &r.Jobs[i]
		if j.ID == job.ID {
			if j.State != "running" || j.Owner != job.Owner || j.Fence != job.Fence || !now.Before(j.LeaseUntil) {
				return nil, contract.Fail("STALE_WORKER", "Preparation ownership expired.", 409)
			}
			if !r.Enabled || r.Deleted || r.PolicyRevision != job.PolicyRevision {
				return nil, contract.Fail("POLICY_CHANGED", "Repository policy changed.", 409)
			}
			return j, nil
		}
	}
	return nil, contract.Fail("JOB_NOT_FOUND", "Preparation task is unavailable.", 404)
}
func (r *Repository) Renew(job Job, stage string, now time.Time) error {
	j, e := r.Owned(job, now)
	if e != nil {
		return e
	}
	j.LeaseUntil = now.Add(LeaseDuration)
	j.Stage = stage
	j.Updated = now
	return nil
}
func (r *Repository) Complete(job Job, code string, now time.Time) error {
	j, e := r.Owned(job, now)
	if e != nil {
		return e
	}
	j.State = "succeeded"
	if code != "" {
		j.State = "failed"
	}
	j.Error = code
	if !j.StartedAt.IsZero() && now.After(j.StartedAt) {
		j.DurationSeconds = now.Sub(j.StartedAt).Seconds()
	}
	j.Updated = now
	j.Stage = "complete"
	j.LeaseUntil = time.Time{}
	r.RecordSyncCheck(*j)
	return nil
}

// Prune only terminal jobs whose receipts expired. Active work and tombstones
// do not rely on TTL and cannot disappear while a worker is executing.
func (r *Repository) Prune(now time.Time) {
	receipts := r.Receipts[:0]
	protected := map[string]bool{}
	for _, v := range r.Receipts {
		if now.Before(v.Expires) {
			receipts = append(receipts, v)
			protected[v.JobID] = true
		}
	}
	r.Receipts = receipts
	// Automatic scans have no HTTP receipt and keep a bounded recent history,
	// so a one-minute interval cannot fill a day-long idempotency window.
	automatic := 0
	for _, j := range r.Jobs {
		if j.Kind == "sync" && j.NoChanges && j.State == "succeeded" && j.EffectiveSource() == SourceAutomatic && !protected[j.ID] {
			continue
		}
		if j.EffectiveSource() == SourceAutomatic && j.State != "queued" && j.State != "running" && !protected[j.ID] {
			automatic++
		}
	}
	jobs := r.Jobs[:0]
	for _, j := range r.Jobs {
		if j.Kind == "sync" && j.NoChanges && j.State == "succeeded" && j.EffectiveSource() == SourceAutomatic && !protected[j.ID] {
			continue
		}
		if j.EffectiveSource() == SourceAutomatic && j.State != "queued" && j.State != "running" && !protected[j.ID] {
			if automatic > 32 {
				automatic--
				continue
			}
		}
		if protected[j.ID] || j.State == "queued" || j.State == "running" || now.Sub(j.Updated) < 24*time.Hour {
			jobs = append(jobs, j)
		}
	}
	r.Jobs = jobs
}

// Graceful interruption is not a failed recovery attempt. Fence old work before
// making it eligible again, and never resurrect user-cancelled work.
func (r *Repository) Release(job Job, now time.Time) error {
	j, err := r.Owned(job, now)
	if err != nil {
		return err
	}
	j.State = "queued"
	j.Stage = "interrupted"
	j.Owner = ""
	j.LeaseUntil = time.Time{}
	j.Updated = now
	j.Fence++
	if j.Attempt > 0 {
		j.Attempt--
	}
	return nil
}
