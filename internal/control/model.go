// Package control owns durable repository policy and preparation intent.
package control

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/gitstore"
)

type Policy struct {
	DefaultBranch bool     `json:"default_branch,omitempty" bson:"default_branch,omitempty"`
	Branches      []string `json:"branches" bson:"branches"`
	Days          int      `json:"days" bson:"days"`
	Count         int      `json:"count" bson:"count"`
}

func (p Policy) Validate() error {
	slots := len(p.Branches)
	if p.DefaultBranch {
		slots++
	}
	if slots < 1 || slots > 8 {
		return contract.Fail("INVALID_POLICY", "Choose 1..8 exact branches.", 400)
	}
	seen := map[string]bool{}
	for _, b := range p.Branches {
		if gitstore.ValidateRevision(b) != nil || strings.HasPrefix(b, "refs/") || b == "snapshot" || seen[b] {
			return contract.Fail("INVALID_POLICY", "Branches must be unique short names.", 400)
		}
		seen[b] = true
	}
	if p.Days < 0 || p.Days > 365 || p.Count < 0 || p.Count > 4096 || (p.Days == 0) == (p.Count == 0) {
		return contract.Fail("INVALID_POLICY", "Use days 1..365 or count 1..4096.", 400)
	}
	return nil
}

type Version struct {
	LinkUnavailable bool                   `json:"-" bson:"-"`
	LinkFormat      int                    `json:"-" bson:"link_format,omitempty"`
	Links           *gitstore.LinkSnapshot `json:"-" bson:"-"`
	Indexed         bool                   `json:"indexed" bson:"-"`
	Commit          string                 `json:"commit" bson:"commit"`
	Time            time.Time              `json:"committer_time" bson:"committer_time"`
	Generation      uint32                 `json:"-" bson:"generation"`
	Files           int                    `json:"files" bson:"files"`
	Shards          int                    `json:"shards" bson:"shards"`
}

const (
	SourceManual               = "manual"
	SourceAutomatic            = "automatic"
	DefaultSyncIntervalMinutes = 5
)

func ValidateSyncInterval(minutes int) error {
	switch minutes {
	case 1, 3, 5, 10, 30, 60, 180, 360, 720, 1440:
		return nil
	default:
		return contract.Fail("INVALID_SYNC_INTERVAL", "Choose a sync interval of 1, 3, 5, 10, 30, 60, 180, 360, 720 or 1440 minutes.", 400)
	}
}

type Job struct {
	RecoveryReason           string    `json:"recovery_reason,omitempty" bson:"recovery_reason,omitempty"`
	NoChanges                bool      `json:"no_changes,omitempty" bson:"no_changes,omitempty"`
	RequestedRevision        string    `json:"requested_revision,omitempty" bson:"requested_revision,omitempty"`
	Group                    string    `json:"group,omitempty" bson:"group,omitempty"`
	StartedAt                time.Time `json:"started_at,omitempty,omitzero" bson:"started_at,omitempty"`
	DurationSeconds          float64   `json:"duration_seconds,omitempty" bson:"duration_seconds,omitempty"`
	LastIndexDurationSeconds float64   `json:"last_index_duration_seconds,omitempty" bson:"last_index_duration_seconds,omitempty"`
	LastIndexedAt            time.Time `json:"last_indexed_at,omitempty,omitzero" bson:"last_indexed_at,omitempty"`
	IndexTimingNote          string    `json:"index_timing_note,omitempty" bson:"index_timing_note,omitempty"`
	Source                   string    `json:"source" bson:"source"`
	PolicyRevision           int64     `json:"policy_revision" bson:"policy_revision"`
	ID                       string    `json:"job_id" bson:"id"`
	Kind                     string    `json:"kind" bson:"kind"`
	Commit                   string    `json:"commit,omitempty" bson:"commit"`
	State                    string    `json:"state" bson:"state"`
	Stage                    string    `json:"stage" bson:"stage"`
	Error                    string    `json:"error,omitempty" bson:"error"`
	Owner                    string    `json:"-" bson:"owner"`
	Fence                    int64     `json:"-" bson:"fence"`
	Attempt                  int       `json:"attempt" bson:"attempt"`
	LeaseUntil               time.Time `json:"-" bson:"lease_until"`
	Created                  time.Time `json:"created_at" bson:"created_at"`
	Updated                  time.Time `json:"updated_at" bson:"updated_at"`
}
type Receipt struct {
	Key     string    `bson:"key"`
	Digest  string    `bson:"digest"`
	JobID   string    `bson:"job_id"`
	Expires time.Time `bson:"expires"`
}
type Audit struct {
	Actor  string    `bson:"actor"`
	Action string    `bson:"action"`
	At     time.Time `bson:"at"`
}
type DiskUsage struct {
	Bytes     int64     `json:"bytes" bson:"bytes"`
	SampledAt time.Time `json:"sampled_at" bson:"sampled_at"`
	StorageID string    `json:"-" bson:"storage_id"`
}

type Repository struct {
	SyncChecks               map[string]SyncCheck  `json:"sync_checks,omitempty" bson:"sync_checks,omitempty"`
	CodeProvider             string                `json:"code_provider,omitempty" bson:"code_provider,omitempty"`
	CreatorID                string                `json:"created_by,omitempty" bson:"created_by,omitempty"`
	LeaseRevision            int64                 `json:"-" bson:"lease_revision,omitempty"`
	DiskUsage                *DiskUsage            `json:"disk_usage,omitempty" bson:"disk_usage,omitempty"`
	DiskUsagePending         bool                  `json:"-" bson:"disk_usage_pending,omitempty"`
	PublicRead               *bool                 `json:"public_read,omitempty" bson:"public_read,omitempty"`
	DefaultBranch            string                `json:"default_branch,omitempty" bson:"default_branch,omitempty"`
	IndexMode                string                `json:"index_mode" bson:"index_mode,omitempty"`
	PathGroups               []PathGroup           `json:"path_groups,omitempty" bson:"path_groups,omitempty"`
	Scopes                   map[string]ScopeState `json:"scopes,omitempty" bson:"scopes,omitempty"`
	LastIndexedAt            time.Time             `json:"last_indexed_at,omitempty,omitzero" bson:"last_indexed_at,omitempty"`
	LastIndexDurationSeconds float64               `json:"last_index_duration_seconds,omitempty" bson:"last_index_duration_seconds,omitempty"`
	SyncIntervalMinutes      int                   `json:"sync_interval_minutes" bson:"sync_interval_minutes"`
	// IndexGenerated is false by default: minified JS/CSS and source maps are
	// left out of the text index from the next build on.
	IndexSymlinks  *bool             `json:"index_symlinks,omitempty" bson:"index_symlinks,omitempty"`
	IndexGenerated bool              `json:"index_generated" bson:"index_generated,omitempty"`
	StorageID      string            `json:"-" bson:"storage_id,omitempty"`
	StorageMissing bool              `json:"storage_missing,omitempty" bson:"-"`
	ID             string            `json:"repo_id" bson:"_id"`
	Name           string            `json:"name" bson:"name"`
	Revision       int64             `json:"revision" bson:"revision"`
	PolicyRevision int64             `json:"policy_revision" bson:"policy_revision"`
	Enabled        bool              `json:"enabled" bson:"enabled"`
	Deleted        bool              `json:"deleted" bson:"deleted"`
	Policy         Policy            `json:"policy" bson:"policy"`
	Heads          map[string]string `json:"heads" bson:"heads"`
	Versions       []Version         `json:"versions" bson:"versions"`
	Jobs           []Job             `json:"jobs" bson:"jobs"`
	Receipts       []Receipt         `json:"-" bson:"receipts"`
	Audit          []Audit           `json:"-" bson:"audit"`
	Updated        time.Time         `json:"updated_at" bson:"updated_at"`
	ObservedAt     time.Time         `json:"observed_at" bson:"observed_at"`
}

func ID(name string) string { h := sha256.Sum256([]byte(name)); return hex.EncodeToString(h[:]) }
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (r *Repository) Version(sha string) (Version, bool) {
	for _, v := range r.Versions {
		if v.Commit == sha {
			return v, true
		}
	}
	return Version{}, false
}
func (r *Repository) SyncInterval() int {
	if r.SyncIntervalMinutes == 0 {
		return DefaultSyncIntervalMinutes
	}
	return r.SyncIntervalMinutes
}

// Missing source predates separate workers and remains recoverable as manual.
func (j Job) EffectiveSource() string {
	if j.Source == "" {
		return SourceManual
	}
	return j.Source
}

func (r *Repository) Queue(kind, commit, key, digest, actor string, now time.Time) (Job, error) {
	return r.QueueSource(kind, commit, key, digest, actor, SourceManual, now)
}

func (r *Repository) QueueSource(kind, commit, key, digest, actor, source string, now time.Time) (Job, error) {
	return r.QueueScoped(kind, commit, "", key, digest, actor, source, now)
}

func (r *Repository) QueueScoped(kind, commit, group, key, digest, actor, source string, now time.Time) (Job, error) {
	if _, ok := r.GroupPaths(group); !ok && !(kind == "prepare" && group == "") {
		return Job{}, contract.Fail("INVALID_PATH_GROUP", "Choose a registered path group.", 400)
	}
	if group != "" {
		h := sha256.Sum256([]byte(group + "\x00" + digest))
		digest = hex.EncodeToString(h[:])
	}
	if !r.Enabled || r.Deleted {
		return Job{}, contract.Fail("REPOSITORY_DISABLED", "Repository is not enabled.", 403)
	}
	r.Prune(now)
	live := r.Receipts[:0]
	for _, v := range r.Receipts {
		if now.Before(v.Expires) {
			live = append(live, v)
		}
	}
	r.Receipts = live
	for _, v := range live {
		if v.Key == key {
			if v.Digest != digest {
				return Job{}, contract.Fail("IDEMPOTENCY_CONFLICT", "Key was used for a different request.", 409)
			}
			for i := range r.Jobs {
				j := &r.Jobs[i]
				if j.ID == v.JobID {
					j.promoteQueuedManual(source, now)
					return *j, nil
				}
			}
			return Job{}, contract.Fail("JOB_HISTORY_CLEARED", "Task history was cleared; use a new idempotency key for a new operation.", 409)
		}
	}
	for i := range r.Jobs {
		j := &r.Jobs[i]
		if j.Group == group && j.Kind == kind && ((kind == "prepare" && j.RequestedRevision == commit) || (kind != "prepare" && j.Commit == commit)) && (j.State == "queued" || j.State == "running") {
			if source != SourceAutomatic && len(r.Receipts) >= 256 {
				return Job{}, contract.Fail("QUEUE_FULL", "Repository operation receipts are at their bounded capacity.", 503)
			}
			j.promoteQueuedManual(source, now)
			if source != SourceAutomatic {
				r.Receipts = append(r.Receipts, Receipt{key, digest, j.ID, now.Add(24 * time.Hour)})
			}
			return *j, nil
		}
	}
	if (source != SourceAutomatic && len(r.Receipts) >= 256) || len(r.Jobs) >= 256 {
		return Job{}, contract.Fail("QUEUE_FULL", "Repository operation history is at its bounded capacity.", 503)
	}
	active := 0
	for _, j := range r.Jobs {
		if j.State == "queued" || j.State == "running" {
			active++
		}
	}
	if active >= r.MaxActiveJobs() {
		return Job{}, contract.Fail("QUEUE_FULL", "Repository preparation queue is full.", 503)
	}
	j := Job{Group: group, LastIndexDurationSeconds: r.Scope(group).LastIndexDurationSeconds, LastIndexedAt: r.Scope(group).LastIndexedAt, IndexTimingNote: IndexTimingNote(r.Scope(group)), PolicyRevision: r.PolicyRevision, Source: source, ID: NewID(), Kind: kind, Commit: commit, State: "queued", Stage: "queued", Created: now, Updated: now}
	if kind == "prepare" {
		j.RequestedRevision = commit
		j.Commit = ""
	}
	r.Jobs = append(r.Jobs, j)
	if source != SourceAutomatic {
		r.Receipts = append(r.Receipts, Receipt{key, digest, j.ID, now.Add(24 * time.Hour)})
	}
	r.Audit = append(r.Audit, Audit{actor, kind, now})
	if len(r.Audit) > 256 {
		r.Audit = r.Audit[len(r.Audit)-256:]
	}
	return j, nil
}

// A manual request may take over queued automatic intent without creating a
// second task. Running work retains its owner, fence and executor source.
func (j *Job) promoteQueuedManual(source string, now time.Time) {
	if source == SourceManual && j.State == "queued" && j.EffectiveSource() == SourceAutomatic {
		j.Source = SourceManual
		j.Updated = now
	}
}

// Existing records were public before per-repository visibility was introduced.
func (r Repository) AllowsAnonymous() bool { return r.PublicRead == nil || *r.PublicRead }
