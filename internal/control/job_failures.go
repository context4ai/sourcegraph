package control

import (
	"sort"
	"time"
)

const JobFailureLimit = 128
const JobFailureRetention = 7 * 24 * time.Hour

// JobFailure is a diagnostic receipt, not runnable work or an idempotency receipt.
// No raw process output, credentials, actor or lease metadata is retained.
type JobFailure struct {
	ID              string    `json:"job_id" bson:"job_id"`
	Kind            string    `json:"kind" bson:"kind"`
	Group           string    `json:"group,omitempty" bson:"group,omitempty"`
	Commit          string    `json:"commit,omitempty" bson:"commit,omitempty"`
	Code            string    `json:"code" bson:"code"`
	Attempt         int       `json:"attempt" bson:"attempt"`
	Time            time.Time `json:"time" bson:"time"`
	DurationSeconds float64   `json:"duration_seconds,omitempty" bson:"duration_seconds,omitempty"`
}

func (r *Repository) retainJobFailures(now time.Time) {
	byID := make(map[string]JobFailure)
	for _, f := range r.JobFailures {
		if now.Sub(f.Time) < JobFailureRetention {
			byID[f.ID] = f
		}
	}
	for _, j := range r.Jobs {
		if j.State == "failed" && now.Sub(j.Updated) < JobFailureRetention {
			byID[j.ID] = JobFailure{ID: j.ID, Kind: j.Kind, Group: j.Group, Commit: j.Commit, Code: j.Error, Attempt: j.Attempt, Time: j.Updated, DurationSeconds: j.DurationSeconds}
		}
	}
	out := make([]JobFailure, 0, len(byID))
	for _, f := range byID {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].ID < out[j].ID
		}
		return out[i].Time.After(out[j].Time)
	})
	if len(out) > JobFailureLimit {
		out = out[:JobFailureLimit]
	}
	r.JobFailures = out
}
