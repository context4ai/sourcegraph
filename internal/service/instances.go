package service

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"time"
)

func (s *Service) Instances(ctx context.Context) (any, error) {
	db, ok := s.DB.(interface {
		RecentObservations(context.Context, time.Time, int) ([]control.Observation, error)
	})
	if !ok {
		return nil, contract.Fail("OBSERVATIONS_UNAVAILABLE", "Instance observations are not configured.", 503)
	}
	now := time.Now().UTC()
	ctx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	rows, e := db.RecentObservations(ctx, now.Add(-5*time.Minute), 101)
	if e != nil {
		return nil, contract.Fail("OBSERVATIONS_UNAVAILABLE", "Could not read instance observations within the budget.", 503)
	}
	truncated := len(rows) > 100
	if truncated {
		rows = rows[:100]
	}
	return map[string]any{"instances": rows, "truncated": truncated, "sampled_at": now, "stale_after_seconds": 180, "scope": "heartbeats within five minutes; per-process search/read/read_many traffic, not repository ownership or routing guarantees"}, nil
}
