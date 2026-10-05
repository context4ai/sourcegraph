package service

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
	"time"
)

type ClearJobsResult struct {
	Removed  int  `json:"removed"`
	Complete bool `json:"complete"`
}

func (s *Service) ClearFinishedJobs(ctx context.Context, success, failed bool) (ClearJobsResult, error) {
	result := ClearJobsResult{}
	cutoff := time.Now().UTC()
	after := ""
	for {
		rows, err := s.DB.List(ctx, after, 100)
		if err != nil {
			return result, err
		}
		for _, row := range rows {
			after = row.ID
			removed := 0
			_, err = control.Mutate(ctx, s.DB, row.Name, func(r *control.Repository) error {
				removed = r.ClearTerminalJobs(success, failed, cutoff)
				if removed == 0 {
					return errNoScheduledWork
				}
				r.Audit = append(r.Audit, control.Audit{Actor: identity.Actor(ctx), Action: "clear_finished_jobs", At: cutoff})
				if len(r.Audit) > 256 {
					r.Audit = r.Audit[len(r.Audit)-256:]
				}
				return nil
			})
			if err == errNoScheduledWork {
				continue
			}
			if err != nil {
				return result, err
			}
			result.Removed += removed
		}
		if len(rows) < 100 {
			result.Complete = true
			return result, nil
		}
	}
}
