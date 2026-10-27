package httpserver

import (
	"context"
	"net/http"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"github.com/context4ai/sourcegraph/internal/sso"
)

// repositoryManagementRoute lists the ?repo= management routes a repository
// manager may use on that repository. The site-wide routes stay admin-only.
func repositoryManagementRoute(path string) bool {
	switch path {
	case Prefix + "/api/admin/v1/repo", Prefix + "/api/admin/v1/repo/preview", Prefix + "/api/admin/v1/repo/prepare",
		Prefix + "/api/admin/v1/repo/commit-options", Prefix + "/api/admin/v1/repo/index/preview", Prefix + "/api/admin/v1/repo/index",
		Prefix + "/api/admin/v1/repo/cleanup/preview", Prefix + "/api/admin/v1/repo/cleanup",
		Prefix + "/api/admin/v1/repo/delete/preview", Prefix + "/api/admin/v1/repo/delete",
		Prefix + "/api/admin/v1/repo/jobs/cancel", Prefix + "/api/admin/v1/repo/purge", Prefix + "/api/admin/v1/repo/managers":
		return true
	}
	return false
}

func registrationRoute(r *http.Request) bool {
	return r.Method == "POST" && r.URL.Path == Prefix+"/api/admin/v1/repos"
}

// delegatedRoute is opened to signed-in users at the gate; the handler then
// requires site management, repository management or the registration setting.
func delegatedRoute(r *http.Request) bool {
	return registrationRoute(r) || (repositoryManagementRoute(r.URL.Path) && !publicRead(r))
}

// authorizeDelegated runs after authentication. Site administrators and the
// deployment credential pass; others need the setting (registration) or to be
// a manager of the target repository.
func authorizeDelegated(ctx context.Context, r *http.Request, s *service.Service, settings *sitesettings.Service, repo string) error {
	if identity.IsAdmin(ctx) {
		return nil
	}
	user := identity.UserID(ctx)
	if registrationRoute(r) {
		if user != "" && settings.AllowUserRepositories() {
			return nil
		}
		return contract.Fail("REGISTRATION_DISABLED", "Only administrators can register repositories on this site.", 403)
	}
	row, err := s.Repo(ctx, repo)
	if err != nil {
		return err
	}
	if !row.IsManager(user) {
		return contract.Fail("REPOSITORY_MANAGEMENT_DENIED", "Only this repository's managers or a site administrator can manage it.", 403)
	}
	return nil
}

// repositoryView adds who created and manages a repository, and whether the
// caller may manage it. People are shown to signed-in callers only.
type repositoryView struct {
	control.Repository
	CanManage bool         `json:"can_manage"`
	Creator   *sso.Person  `json:"creator,omitempty"`
	Managers  []sso.Person `json:"managers,omitempty"`
}

func viewRepository(ctx context.Context, row control.Repository, auth *sso.Handler) repositoryView {
	user := identity.UserID(ctx)
	view := repositoryView{Repository: row, CanManage: identity.IsAdmin(ctx) || row.IsManager(user)}
	if auth == nil || (user == "" && !identity.IsAdmin(ctx)) {
		return view
	}
	if row.CreatorID != "" {
		creator := auth.People(ctx, []string{row.CreatorID})[0]
		view.Creator = &creator
	}
	view.Managers = auth.People(ctx, row.EffectiveManagers())
	return view
}
