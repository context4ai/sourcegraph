package service

import (
	"context"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/identity"
)

// AuthorizePrepare admits site administrators, the deployment credential, a
// personal token whose owner is currently an administrator, and the
// repository's managers through their session or personal token.
func (s *Service) AuthorizePrepare(ctx context.Context, name string) error {
	if identity.IsAdmin(ctx) || identity.TokenOwnerAdmin(ctx) {
		return nil
	}
	user := identity.UserID(ctx)
	if user == "" || identity.IsAnonymous(ctx) {
		return contract.Fail("ACCESS_DENIED", "Index preparation requires a repository manager or an administrator.", 403)
	}
	r, err := s.Repo(ctx, name)
	if err != nil {
		return err
	}
	if !r.IsManager(user) {
		return contract.Fail("REPOSITORY_MANAGEMENT_DENIED", "Only this repository's managers or a site administrator can prepare it.", 403)
	}
	return nil
}

// ManagesAnyRepository reports whether the user manages at least one
// repository that is not deleted. It reads the registry page by page.
func (s *Service) ManagesAnyRepository(ctx context.Context, user string) (bool, error) {
	if user == "" {
		return false, nil
	}
	after := ""
	for {
		rows, err := s.DB.List(ctx, after, 100)
		if err != nil {
			return false, err
		}
		for _, r := range rows {
			if !r.Deleted && r.IsManager(user) {
				return true, nil
			}
		}
		if len(rows) < 100 {
			return false, nil
		}
		after = rows[len(rows)-1].ID
	}
}
