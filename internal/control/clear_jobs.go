package control

import "time"

// ClearTerminalJobs keeps active work, snapshots and idempotency receipts intact.
// A cutoff prevents this request from deleting jobs that finish during cleanup.
func (r *Repository) ClearTerminalJobs(success, failed bool, cutoff time.Time) int {
	kept := r.Jobs[:0]
	count := 0
	for _, j := range r.Jobs {
		if !j.Updated.After(cutoff) && ((success && j.State == "succeeded") || (failed && j.State == "failed")) {
			count++
			continue
		}
		kept = append(kept, j)
	}
	r.Jobs = kept
	return count
}
