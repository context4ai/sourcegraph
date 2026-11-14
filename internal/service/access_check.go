package service

import (
	"context"
	"errors"
	"time"

	"github.com/context4ai/sourcegraph/internal/codesources"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/identity"
)

// accessCheckLimit bounds one registration access check within the request deadline.
const accessCheckLimit = 20 * time.Second

var errAccessCheckTimeout = contract.FailRetryAfter("ACCESS_CHECK_TIMEOUT", "The code host did not answer the access check in time; retry shortly.", 504, 10)

// CheckAccess lists the remote HEAD with the credentials a registration by the
// caller would use (personal, then global, then anonymous), so a repository
// that cannot be fetched is found before it is registered. Nothing is stored.
func (s *Service) CheckAccess(ctx context.Context, name, provider string) error {
	if provider == "" {
		provider = defaultCodeProvider
	}
	source, known := codesources.Lookup(provider)
	if !known {
		return contract.Fail("INVALID_CODE_PROVIDER", "Choose one of the configured code sources.", 400)
	}
	if source.Host == "" {
		return contract.Fail("CODE_SOURCE_NOT_CONFIGURED", "Configure this code source's host before registering repositories.", 409)
	}
	if err := gitstore.ValidateRepo(name); err != nil {
		return err
	}
	if s.AccessCheck != nil {
		return s.AccessCheck(ctx, name, provider)
	}
	wait := min(requestWait(ctx), accessCheckLimit)
	if wait == 0 {
		return errAccessCheckTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	m := &s.refState
	m.mu.Lock()
	m.init()
	m.mu.Unlock()
	// Shares the remote-lookup slots; no per-name state is kept for unregistered names.
	select {
	case m.lookups8 <- struct{}{}:
		defer func() { <-m.lookups8 }()
	case <-ctx.Done():
		return errAccessCheckTimeout
	}
	dir, cleanup, err := s.refScratch(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	r := control.Repository{Name: name, CodeProvider: provider, CodeHost: source.Host, CreatorID: identity.UserID(ctx)}
	err = s.withGitCredential(ctx, r, func(token string) error {
		o := s.fetchOptions(r, token)
		o.CheckGrowth = nil
		return s.Git.CheckRemoteAccess(ctx, dir, name, o)
	})
	if errors.Is(err, context.DeadlineExceeded) || (err != nil && ctx.Err() != nil) {
		return errAccessCheckTimeout
	}
	return err
}

// CheckRegistrationAccess runs CheckAccess before a new registration, outside
// the registration lock. An existing name is left to Register, which replays
// or reports the conflict without contacting the code host.
func (s *Service) CheckRegistrationAccess(ctx context.Context, name, provider string) error {
	if err := gitstore.ValidateRepo(name); err != nil {
		return err
	}
	_, err := s.DB.Get(ctx, name)
	if err == nil {
		return nil
	}
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != "REPOSITORY_NOT_FOUND" {
		return err
	}
	return s.CheckAccess(ctx, name, provider)
}
