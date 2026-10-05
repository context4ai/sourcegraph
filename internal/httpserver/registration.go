package httpserver

import (
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
)

type registrationRequest struct {
	CodeProvider string              `json:"code_provider"`
	PublicRead   *bool               `json:"public_read"`
	IndexMode    string              `json:"index_mode"`
	PathGroups   []control.PathGroup `json:"path_groups"`
	Name         string              `json:"name"`
	Policy       struct {
		Branches      []string `json:"branches"`
		DefaultBranch bool     `json:"default_branch"`
		Days          *int     `json:"days"`
		Count         *int     `json:"count"`
	} `json:"policy"`
	SyncIntervalMinutes *int `json:"sync_interval_minutes"`
}

// Omitted retention adopts the server's current cached site defaults. Explicit
// policies retain their original API semantics, including validation failures.
// The resulting policy is saved with the repository, not retroactively changed
// by later edits to the defaults.
func registrationPolicy(q registrationRequest, settings *sitesettings.Service) (control.Policy, error) {
	p := control.Policy{Branches: q.Policy.Branches, DefaultBranch: q.Policy.DefaultBranch}
	if q.Policy.Days == nil && q.Policy.Count == nil {
		if settings == nil {
			return p, contract.Fail("SETTINGS_UNAVAILABLE", "Repository defaults are unavailable; supply an explicit retention policy.", 503)
		}
		snapshot := settings.Snapshot().RepositoryDefaults
		if snapshot.Revision < 1 {
			return p, contract.Fail("SETTINGS_UNAVAILABLE", "Repository defaults are not initialized.", 503)
		}
		switch snapshot.Values.RetentionMode {
		case "days":
			p.Days = snapshot.Values.RetentionDays
		case "count":
			p.Count = snapshot.Values.RetentionCount
		default:
			return p, contract.Fail("SETTINGS_UNAVAILABLE", "Repository defaults are invalid.", 503)
		}
	} else {
		if q.Policy.Days != nil {
			p.Days = *q.Policy.Days
		}
		if q.Policy.Count != nil {
			p.Count = *q.Policy.Count
		}
	}
	return p, p.Validate()
}
