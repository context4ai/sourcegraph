package service

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"log/slog"
	"time"
)

func (s *Service) keepLease(ctx context.Context, cancel context.CancelFunc, name string, job control.Job) {
	maintainLease(ctx, cancel, job.LeaseUntil, 10*time.Second, func(ctx context.Context) (time.Time, error) {
		next, err := control.RenewLease(ctx, s.DB, name, job, time.Now().UTC())
		if err != nil {
			slog.Warn("task lease renewal failed", "repo", name, "job_id", job.ID, "code", jobFailureCode(err))
		}
		return next, err
	})
}

func maintainLease(ctx context.Context, cancel context.CancelFunc, until time.Time, interval time.Duration, renew func(context.Context) (time.Time, error)) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	expiry := time.NewTimer(time.Until(until))
	defer expiry.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			cancel()
			return
		case <-tick.C:
			deadline := time.Now().Add(5 * time.Second)
			if until.Before(deadline) {
				deadline = until
			}
			budget, stop := context.WithDeadline(ctx, deadline)
			next, err := renew(budget)
			stop()
			if err == nil {
				until = next
				if !expiry.Stop() {
					select {
					case <-expiry.C:
					default:
					}
				}
				expiry.Reset(time.Until(until))
				continue
			}
			var ce *contract.Error
			lost := errors.As(err, &ce) && (ce.Code == "STALE_WORKER" || ce.Code == "POLICY_CHANGED" || ce.Code == "JOB_NOT_FOUND" || ce.Code == "REPOSITORY_NOT_FOUND")
			if lost || !time.Now().Before(until) {
				cancel()
				return
			}
		}
	}
}
