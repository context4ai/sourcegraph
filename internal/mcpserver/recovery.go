package mcpserver

import (
	"context"
	"errors"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/service"
)

// Recovery advice follows this request's authenticated handler, not an inferred
// actor or a second call to the mutating tool's rate-limited authorization guard.
func publicError(err error, canPrepare bool) (string, string) {
	code, message := "DEPENDENCY_UNAVAILABLE", "Operation could not complete."
	var ce *contract.Error
	if errors.As(err, &ce) {
		code, message = ce.Code, ce.Message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code, message = "REQUEST_TIMEOUT", "The operation exceeded its time budget; retry a smaller request."
	}
	if errors.Is(err, context.Canceled) {
		code, message = "REQUEST_CANCELED", "The operation was canceled."
	}
	return code, recoveryMessage(code, message, canPrepare)
}

func recoveryMessage(code, message string, canPrepare bool) string {
	var description string
	switch code {
	case "INDEX_NOT_READY":
		description = "The requested searchable index is not ready or fully loaded. A known file may be readable at the same SHA in full mode; monorepo content still requires its group prepared."
	case "CONTENT_NOT_READY":
		description = "The requested group's file content is not prepared at this SHA. This is missing material, not an empty file."
	case "SCOPE_NOT_READY":
		description = "A selected path group has no observation for this SHA. The response cannot combine different commits."
	case "REPOSITORY_STORAGE_MISSING":
		description = "Repository files or indexes are missing after a storage change."
	case "DEFAULT_BRANCH_NOT_OBSERVED", "BRANCH_NOT_OBSERVED":
		description = "The requested/default monitored branch has no synchronized head. No alternate version was selected."
	case "REVISION_NOT_ELIGIBLE":
		description = "The requested revision is outside the currently observed retention scope. Keep the requested baseline; do not substitute another revision."
	default:
		return message
	}
	if canPrepare {
		return description + " Inspect availability when needed. If preparation is intended, use prepare with the requested revision; acceptance is asynchronous and does not guarantee readiness or eligibility."
	}
	return description + " This connection is read-only. Report the unavailable scope or ask a maintainer to synchronize/prepare it; no prepare tool is available here."
}

func batchRecoveryHints(batch service.ReadManyResult, canPrepare bool) service.ReadManyResult {
	// Copy before annotating so shared Service results retain their protocol.
	batch.Items = append([]service.ReadManyItem(nil), batch.Items...)
	for i := range batch.Items {
		if e := batch.Items[i].Error; e != nil {
			copy := *e
			copy.Message = recoveryMessage(copy.Code, copy.Message, canPrepare)
			batch.Items[i].Error = &copy
		}
	}
	return batch
}
