package gitstore

import (
	"github.com/context4ai/sourcegraph/internal/contract"
	"strings"
)

// Diagnostics are bounded in memory and never included in errors or logs.
func remoteFailure(diagnostic, code, message string) error {
	text := strings.ToLower(diagnostic)
	if httpStatus(text, "403") || strings.Contains(text, "not authorized") || strings.Contains(text, "permission denied") {
		return contract.Fail("GIT_ACCESS_DENIED", "Code-service credential cannot access this repository.", 403)
	}
	if strings.Contains(text, "authentication failed") || httpStatus(text, "401") || strings.Contains(text, "invalid token") || strings.Contains(text, "expired token") {
		return contract.Fail("GIT_AUTH_FAILED", "Code-service credential was rejected; update the credential.", 401)
	}
	return contract.Fail(code, message, 503)
}

func httpStatus(text, code string) bool {
	for _, prefix := range []string{"error: ", "http ", "http/1.1 ", "http/2 "} {
		if strings.Contains(text, prefix+code) {
			return true
		}
	}
	return false
}
