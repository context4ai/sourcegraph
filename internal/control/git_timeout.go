package control

import "github.com/context4ai/sourcegraph/internal/contract"

// Zero is persisted inheritance; old repository records therefore inherit.
func ValidateGitTimeout(minutes int) error {
	switch minutes {
	case 0, 15, 30, 45, 60:
		return nil
	}
	return contract.Fail("INVALID_GIT_TIMEOUT", "Use 0 to inherit, or 15, 30, 45, 60 minutes.", 400)
}
