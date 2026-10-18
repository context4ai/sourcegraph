package service

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
	"log/slog"
	"time"
)

var errCompletionRecorded = errors.New("completion already recorded")

// Retry uncertain writes within a bounded budget. The callback must be idempotent.
func retryJobCompletion(ctx context.Context, write func(context.Context) error) error {
	var last error
	for {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		last = write(attempt)
		cancel()
		if last == nil {
			return nil
		}
		var ce *contract.Error
		if errors.As(last, &ce) && ce.Code != "REVISION_CONFLICT" && ce.Code != "CONTROL_STATE_UNAVAILABLE" {
			return last
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(500 * time.Millisecond):
		}
	}
}
func jobFailureCode(err error) string {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "CONTROL_WRITE_TIMEOUT"
	}
	return "CONTROL_STATE_UNAVAILABLE"
}
func logJobWriteFailure(name, id string, err error) {
	slog.Error("task completion could not be persisted; lease recovery required", "repo", name, "job_id", id, "code", jobFailureCode(err))
}

// Log only structured task metadata after persistence. Raw preparation errors
// may contain remote URLs, process output or credentials and must never appear.
func logJobCompletion(repo string, job control.Job) {
	if job.State != "succeeded" && job.State != "failed" {
		return
	}
	if job.State == "succeeded" && job.NoChanges && job.EffectiveSource() == control.SourceAutomatic {
		return
	}
	slog.Info("repository_task_completed", "repo", repo, "job_id", job.ID,
		"kind", job.Kind, "group", job.Group, "commit", job.Commit,
		"state", job.State, "code", job.Error, "attempt", job.Attempt,
		"duration_seconds", job.DurationSeconds)
}
