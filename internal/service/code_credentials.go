package service

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"time"
)

func (s *Service) withGitCredential(ctx context.Context, r control.Repository, operation func(string) error) error {
	if s.Credentials == nil {
		return operation(s.Token)
	} // isolated fixtures
	candidates, err := s.Credentials.Candidates(ctx, r.CreatorID, r.CodeProvider)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = operation(candidate.Token)
		var ce *contract.Error
		authFailure := errors.As(err, &ce) && ce.Code == "GIT_AUTH_FAILED"
		if err == nil || authFailure {
			budget, stop := context.WithTimeout(ctx, 2*time.Second)
			s.Credentials.Report(budget, candidate, authFailure)
			stop()
		}
		if err == nil {
			return nil
		}
		if !errors.As(err, &ce) || (ce.Code != "GIT_AUTH_FAILED" && ce.Code != "GIT_ACCESS_DENIED" && ce.Code != "GIT_FETCH_FAILED" && ce.Code != "GIT_HYDRATION_FAILED") {
			return err
		}
	}
	return err
}
