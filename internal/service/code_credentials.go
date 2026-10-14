package service

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/codesources"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
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

// defaultCodeProvider is the provider of repositories registered before providers were recorded.
const defaultCodeProvider = "github"

// fetchOptions uses the host pinned at registration; the source still supplies username and anonymous access.
func (s *Service) fetchOptions(r control.Repository, token string) gitstore.FetchOptions {
	provider := r.CodeProvider
	if provider == "" {
		provider = defaultCodeProvider
	}
	source, _ := codesources.Resolve(provider)
	host := r.CodeHost
	if host == "" {
		host = source.Host
	}
	return gitstore.FetchOptions{CredentialHelper: s.Helper, Token: token, Host: host, Username: source.Username, Anonymous: source.Anonymous, Branches: r.Policy.Branches, MinFreeBytes: s.MinFree}
}
