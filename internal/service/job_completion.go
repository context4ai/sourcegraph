package service

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/contract"
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
