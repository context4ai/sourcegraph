package control

import "time"

// ClearTerminalJobs keeps active work, snapshots and idempotency receipts intact.
// A cutoff prevents this request from deleting jobs that finish during cleanup.
func (r *Repository) ClearTerminalJobs(success, failed bool, cutoff time.Time) int {
	kept := r.Jobs[:0]
	count := 0
	removed := map[string]bool{}
	for _, j := range r.Jobs {
		if !j.Updated.After(cutoff) && ((success && j.State == "succeeded") || (failed && j.State == "failed")) {
			count++
			removed[j.ID] = true
			continue
		}
		kept = append(kept, j)
	}
	r.Jobs = kept
	// Explicit failure-history cleanup also removes archived diagnostics, while
	// preserving failures completed after the caller's cutoff.
	if failed {
		summaries := r.JobFailures[:0]
		for _, f := range r.JobFailures {
			if f.Time.After(cutoff) {
				summaries = append(summaries, f)
			} else if !removed[f.ID] {
				count++
			}
		}
		r.JobFailures = summaries
	}
	return count
}
