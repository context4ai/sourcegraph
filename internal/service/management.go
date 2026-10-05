package service

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/identity"
	"time"
)

type Update struct {
	PublicRead          *bool                `json:"public_read,omitempty"`
	PathGroups          *[]control.PathGroup `json:"path_groups,omitempty"`
	SyncIntervalMinutes *int                 `json:"sync_interval_minutes,omitempty"`
	IndexSymlinks       *bool                `json:"index_symlinks,omitempty"`
	IndexGenerated      *bool                `json:"index_generated,omitempty"`
	Policy              control.Policy       `json:"policy"`
	Enabled             bool                 `json:"enabled"`
	Deleted             bool                 `json:"deleted"`
	Preview             string               `json:"preview"`
}

func validateUpdate(u Update) error {
	if err := u.Policy.Validate(); err != nil {
		return err
	}
	if u.SyncIntervalMinutes != nil {
		return control.ValidateSyncInterval(*u.SyncIntervalMinutes)
	}
	return nil
}

func preview(r control.Repository, u Update) string {
	u.Preview = ""
	return digest(struct {
		Revision       int64
		PolicyRevision int64
		Update         Update
	}{r.Revision, r.PolicyRevision, u})
}
func (s *Service) Preview(ctx context.Context, name string, u Update) (any, error) {
	if e := validateUpdate(u); e != nil {
		return nil, e
	}
	r, e := s.Repo(ctx, name)
	if e != nil {
		return nil, e
	}
	groups := r.PathGroups
	if u.PathGroups != nil {
		groups = *u.PathGroups
	}
	_, _, e = control.NormalizeIndexConfig(r.EffectiveIndexMode(), groups)
	if e != nil {
		return nil, e
	}
	interval := r.SyncInterval()
	if u.SyncIntervalMinutes != nil {
		interval = *u.SyncIntervalMinutes
	}
	return map[string]any{"sync_interval_minutes": interval, "revision": r.Revision, "preview": preview(r, u), "retained_versions": len(r.Versions), "note": "Policy changes revoke current version eligibility until the next successful sync; Git bytes are retained until explicit cleanup."}, nil
}
func (s *Service) Update(ctx context.Context, name string, u Update, revision int64) (control.Repository, error) {
	if e := validateUpdate(u); e != nil {
		return control.Repository{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.DB.Get(ctx, name)
	if e != nil {
		return r, e
	}
	if r.Deleted && (!u.Deleted || u.Enabled) {
		return r, contract.Fail("REPOSITORY_DELETED", "Finish permanent deletion before registering this repository again.", 409)
	}
	if r.Revision != revision || u.Preview != preview(r, u) {
		return r, contract.Fail("REVISION_CONFLICT", "Policy preview expired; reload and preview again.", 412)
	}
	oldPolicy := r.Policy
	groups := r.PathGroups
	if u.PathGroups != nil {
		groups = *u.PathGroups
	}
	_, groups, e = control.NormalizeIndexConfig(r.EffectiveIndexMode(), groups)
	if e != nil {
		return r, e
	}
	groupsChanged := digest(groups) != digest(r.PathGroups)
	r.PathGroups = groups
	r.Revision++
	policyChanged := digest(u.Policy) != digest(oldPolicy) || groupsChanged
	revokesWork := policyChanged || r.Enabled != u.Enabled || r.Deleted != u.Deleted
	if revokesWork {
		r.PolicyRevision++
	}
	intervalChanged := u.SyncIntervalMinutes != nil && *u.SyncIntervalMinutes != r.SyncInterval()
	if u.SyncIntervalMinutes != nil {
		r.SyncIntervalMinutes = *u.SyncIntervalMinutes
	}
	if u.PublicRead != nil {
		value := *u.PublicRead
		r.PublicRead = &value
	}
	// Applies from the next index build; published generations stay searchable.
	if u.IndexSymlinks != nil {
		value := *u.IndexSymlinks
		r.IndexSymlinks = &value
	}
	if u.IndexGenerated != nil {
		r.IndexGenerated = *u.IndexGenerated
	}
	r.Policy = u.Policy
	r.Enabled = u.Enabled
	r.Deleted = u.Deleted
	r.Updated = time.Now().UTC()
	for i := range r.Jobs {
		if intervalChanged && r.Jobs[i].Kind == "sync" && r.Jobs[i].State == "queued" && r.Jobs[i].EffectiveSource() == control.SourceAutomatic {
			r.Jobs[i].State = "cancelled"
			r.Jobs[i].Error = "SYNC_INTERVAL_CHANGED"
			r.Jobs[i].Updated = r.Updated
		}
		if revokesWork && (r.Jobs[i].State == "queued" || r.Jobs[i].State == "running") {
			r.Jobs[i].State = "cancelled"
			r.Jobs[i].Updated = r.Updated
			r.Jobs[i].Error = "POLICY_CHANGED"
		}
	}
	// Disable retains evidence; changing the retention policy invalidates observed
	// eligibility until a new sync, rather than authorizing unobserved revisions.
	if policyChanged {
		r.DefaultBranch = ""
		r.ObservedAt = time.Time{}
		r.Heads = map[string]string{}
		r.Versions = nil
		r.Scopes = nil
		r.LastIndexedAt = time.Time{}
		r.LastIndexDurationSeconds = 0
	}
	r.Audit = append(r.Audit, control.Audit{Actor: identity.Actor(ctx), Action: "policy", At: r.Updated})
	if len(r.Audit) > 256 {
		r.Audit = r.Audit[len(r.Audit)-256:]
	}
	e = s.DB.Replace(ctx, r, revision)
	return projectRepository(s.markStorage(r)), e
}
