package service

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"time"
)

// CancelJob revokes durable ownership before interrupting the local process tree.
// Other instances observe the revocation during their lease renewal.
func (s *Service) CancelJob(ctx context.Context, name, id string) (control.Job, error) {
	var result control.Job
	_, err := control.Mutate(ctx, s.DB, name, func(r *control.Repository) error {
		for i := range r.Jobs {
			j := &r.Jobs[i]
			if j.ID != id {
				continue
			}
			if j.State != "queued" && j.State != "running" {
				result = *j
				return nil
			}
			now := time.Now().UTC()
			j.State = "cancelled"
			j.Error = "CANCELLED_BY_USER"
			j.Updated = now
			j.Fence++
			j.LeaseUntil = time.Time{}
			if !j.StartedAt.IsZero() {
				j.DurationSeconds = now.Sub(j.StartedAt).Seconds()
			}
			result = *j
			return nil
		}
		return contract.Fail("JOB_NOT_FOUND", "Preparation task is unavailable.", 404)
	})
	if err == nil {
		s.repositoryWorkMu.Lock()
		if cancel := s.jobCancels[id]; cancel != nil {
			cancel()
		}
		s.repositoryWorkMu.Unlock()
	}
	return result, err
}
