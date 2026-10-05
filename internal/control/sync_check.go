package control

import (
	"maps"
	"time"
)

// SyncCheck survives removal of uneventful automatic jobs. Scheduling and
// retry ordering must not depend on how much history the UI retains.
type SyncCheck struct {
	StartedAt        time.Time `json:"started_at" bson:"started_at"`
	CheckedAt        time.Time `json:"checked_at" bson:"checked_at"`
	NoChanges        bool      `json:"no_changes" bson:"no_changes"`
	UnchangedCount   int       `json:"unchanged_count" bson:"unchanged_count"`
	Error            string    `json:"error,omitempty" bson:"error,omitempty"`
	PolicyRevision   int64     `json:"-" bson:"policy_revision"`
	StorageID        string    `json:"-" bson:"storage_id"`
	SuccessStartedAt time.Time `json:"-" bson:"success_started_at"`
	SuccessSource    string    `json:"-" bson:"success_source"`
}

func (r Repository) SyncUnchanged(group string, heads map[string]string) bool {
	if len(heads) == 0 || !maps.Equal(r.Scope(group).Heads, heads) {
		return false
	}
	for _, sha := range heads {
		version, ok := r.ScopedVersion(group, sha)
		if !ok || version.Generation == 0 {
			return false
		}
	}
	return true
}

func (r *Repository) RecordSyncCheck(j Job) {
	if j.Kind != "sync" {
		return
	}
	if r.SyncChecks == nil {
		r.SyncChecks = map[string]SyncCheck{}
	}
	check := r.SyncChecks[j.Group]
	if check.PolicyRevision != j.PolicyRevision || check.StorageID != r.StorageID {
		check = SyncCheck{}
	}
	check.StartedAt, check.CheckedAt = j.Created, j.Updated
	check.PolicyRevision, check.StorageID = j.PolicyRevision, r.StorageID
	check.Error = j.Error
	check.NoChanges = j.State == "succeeded" && j.NoChanges
	if j.State == "succeeded" {
		check.SuccessStartedAt, check.SuccessSource = j.Created, j.EffectiveSource()
	}
	if check.NoChanges {
		check.UnchangedCount++
	} else {
		check.UnchangedCount = 0
	}
	r.SyncChecks[j.Group] = check
}
