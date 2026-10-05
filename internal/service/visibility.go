package service

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
)

// Fill a visible page, rather than making hidden rows truncate pagination.
func (s *Service) catalogRows(ctx context.Context, after string) ([]control.Repository, error) {
	if !identity.IsAnonymous(ctx) {
		return s.DB.List(ctx, after, 100)
	}
	out := []control.Repository{}
	for {
		rows, err := s.DB.List(ctx, after, 100)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.AllowsAnonymous() {
				out = append(out, r)
				if len(out) == 100 {
					return out, nil
				}
			}
		}
		if len(rows) < 100 {
			return out, nil
		}
		after = rows[len(rows)-1].ID
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

// Anonymous site statistics must not disclose names of private repositories.
func (s *Service) StatisticsFor(ctx context.Context) any {
	result := s.Statistics().(map[string]any)
	if identity.IsAnonymous(ctx) {
		buckets := result["buckets"].([]Bucket)
		for i := range buckets {
			buckets[i].ByRepo = map[string]Metric{}
		}
		result["buckets"] = buckets
	}
	return result
}
