package service

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/identity"
	"time"
)

func (s *Service) logQuery(ctx context.Context, repo, op string, start time.Time, input any, err error) {
	s.QueryLogs.Record(ctx, s.boot, identity.Actor(ctx), repo, op, start, input, err)
}
