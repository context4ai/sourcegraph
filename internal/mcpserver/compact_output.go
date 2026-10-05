package mcpserver

import (
	"sort"
	"time"

	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/service"
)

func putTime(out map[string]any, key string, t time.Time) {
	if !t.IsZero() {
		out[key] = t
	}
}
func putString(out map[string]any, key, value string) {
	if value != "" {
		out[key] = value
	}
}
func compactVersion(v control.Version) map[string]any {
	out := map[string]any{"commit": v.Commit, "indexed": v.Indexed}
	putTime(out, "committer_time", v.Time)
	return out
}
func compactVersions(versions []control.Version) []any {
	out := make([]any, 0, len(versions))
	for _, v := range versions {
		out = append(out, compactVersion(v))
	}
	return out
}
func repositoryState(enabled, deleted, missing bool) string {
	if deleted {
		return "deleted"
	}
	if !enabled {
		return "disabled"
	}
	if missing {
		return "storage_missing"
	}
	return "registered"
}
func compactCatalog(rows []service.Summary) map[string]any {
	items := make([]any, 0, len(rows))
	for _, r := range rows {
		item := map[string]any{"repo": r.Name, "state": repositoryState(r.Enabled, r.Deleted, r.StorageMissing), "index_mode": r.IndexMode, "heads": r.Heads}
		putString(item, "default_branch", r.DefaultBranch)
		if len(r.PathGroups) > 0 {
			item["path_groups"] = r.PathGroups
		}
		items = append(items, item)
	}
	out := map[string]any{"repositories": items}
	// A full page may be the last; the next call can legitimately return empty.
	if len(rows) == 100 {
		out["next_cursor"] = rows[len(rows)-1].ID
	}
	return out
}
func compactJob(j control.Job) map[string]any {
	out := map[string]any{"job_id": j.ID, "state": j.State}
	putString(out, "kind", j.Kind)
	putString(out, "revision", j.RequestedRevision)
	putString(out, "commit", j.Commit)
	putString(out, "group", j.Group)
	if j.State == "running" {
		putString(out, "stage", j.Stage)
	}
	putString(out, "error", j.Error)
	putTime(out, "updated_at", j.Updated)
	if j.LastIndexDurationSeconds > 0 {
		out["previous_index_seconds"] = j.LastIndexDurationSeconds
		out["timing_note"] = "Previous completed index duration; reference only, not an ETA."
	}
	return out
}
func compactAvailability(r control.Repository, jobID string) map[string]any {
	out := map[string]any{"repo": r.Name, "state": repositoryState(r.Enabled, r.Deleted, r.StorageMissing), "index_mode": r.IndexMode, "heads": r.Heads, "versions": compactVersions(r.Versions)}
	putString(out, "default_branch", r.DefaultBranch)
	if len(r.Policy.Branches) > 0 {
		out["monitored_branches"] = r.Policy.Branches
	}
	putTime(out, "observed_at", r.ObservedAt)
	putTime(out, "last_indexed_at", r.LastIndexedAt)
	if r.LastIndexDurationSeconds > 0 {
		out["previous_index_seconds"] = r.LastIndexDurationSeconds
	}
	if len(r.PathGroups) > 0 {
		groups := make([]any, 0, len(r.PathGroups))
		for _, g := range r.PathGroups {
			scope := r.Scopes[g.Name]
			v := map[string]any{"name": g.Name, "paths": g.Paths, "heads": scope.Heads, "versions": compactVersions(scope.Versions)}
			putTime(v, "observed_at", scope.ObservedAt)
			putTime(v, "last_indexed_at", scope.LastIndexedAt)
			if scope.LastIndexDurationSeconds > 0 {
				v["previous_index_seconds"] = scope.LastIndexDurationSeconds
			}
			groups = append(groups, v)
		}
		out["groups"] = groups
	}
	// Preserve exact task polling without sending the entire historical queue.
	jobs := append([]control.Job(nil), r.Jobs...)
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Updated.After(jobs[j].Updated) })
	latest := map[string]bool{}
	selected := []any{}
	for _, j := range jobs {
		if jobID != "" {
			if j.ID == jobID {
				selected = append(selected, compactJob(j))
			}
			continue
		}
		if j.State == "queued" || j.State == "running" {
			selected = append(selected, compactJob(j))
			continue
		}
		if latest[j.Group] {
			continue
		}
		latest[j.Group] = true
		// Successful newer sync checks supersede an older failure in the overview.
		if (j.State == "failed" || j.State == "cancelled") && r.SyncChecks[j.Group].Error == "" && r.SyncChecks[j.Group].CheckedAt.After(j.Updated) {
			continue
		}
		selected = append(selected, compactJob(j))
	}
	out["jobs"] = selected
	if jobID != "" && len(selected) == 0 {
		out["job_lookup"] = map[string]string{"job_id": jobID, "state": "not_found", "message": "Job is not retained; this does not mean it succeeded. Inspect version readiness."}
	}
	if jobID == "" {
		out["jobs_scope"] = "Active jobs and latest retained terminal job per group; pass job_id to inspect a specific retained task."
	}
	return out
}
func compactList(tree gitstore.Tree) map[string]any {
	entries := make([]any, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		item := map[string]any{"path": e.Path, "kind": e.Kind}
		putString(item, "target", e.Target)
		putString(item, "target_kind", e.TargetKind)
		putString(item, "follow_error", e.FollowError)
		if e.Size != nil {
			item["size"] = *e.Size
		}
		entries = append(entries, item)
	}
	out := map[string]any{"commit": tree.Commit, "entries": entries, "has_more": tree.HasMoreResults}
	putString(out, "next_cursor", tree.NextCursor)
	putString(out, "via", tree.Via)
	return out
}
func compactDiff(d gitstore.Diff) map[string]any {
	changes := make([]any, 0, len(d.Changes))
	for _, c := range d.Changes {
		item := map[string]any{"path": c.Path, "change": c.ChangeKind}
		putString(item, "patch", c.Patch)
		if c.Binary {
			item["binary"] = true
		}
		if c.PatchTruncated {
			item["patch_truncated"] = true
		}
		changes = append(changes, item)
	}
	out := map[string]any{"base_commit": d.BaseCommit, "head_commit": d.HeadCommit, "changes": changes, "has_more": d.HasMoreResults}
	if len(d.PathStatus) > 0 {
		out["path_status"] = d.PathStatus
	}
	putString(out, "next_cursor", d.NextCursor)
	return out
}
func compactOutput(name string, v any, in any) any {
	switch name {
	case "repositories":
		if rows, ok := v.([]service.Summary); ok {
			return compactCatalog(rows)
		}
	case "resolve":
		if version, ok := v.(control.Version); ok {
			return compactVersion(version)
		}
	case "availability":
		input := in.(availabilityInput)
		if r, ok := v.(control.Repository); ok {
			return compactAvailability(r, input.JobID)
		}
		if wrapper, ok := v.(map[string]any); ok {
			if r, ok := wrapper["repository"].(control.Repository); ok {
				out := compactAvailability(r, input.JobID)
				out["plugins"] = wrapper["plugins"]
				return out
			}
		}
	case "prepare":
		if j, ok := v.(control.Job); ok {
			return compactJob(j)
		}
	case "list":
		if tree, ok := v.(gitstore.Tree); ok {
			return compactList(tree)
		}
	case "diff":
		if diff, ok := v.(gitstore.Diff); ok {
			return compactDiff(diff)
		}
	}
	return v
}
