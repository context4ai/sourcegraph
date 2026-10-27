package gitstore

import (
	"errors"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
)

func TestRemoteFailureNamesMissingOrInvisibleRepositories(t *testing.T) {
	for diagnostic, want := range map[string]string{
		"remote: Repository not found.\nfatal: repository 'https://code.example.com/a/b.git/' not found": "GIT_REPOSITORY_UNAVAILABLE",
		"fatal: unable to access 'https://x/a/b.git/': The requested URL returned error: 404":            "GIT_REPOSITORY_UNAVAILABLE",
		"error: 404 Not Found":                         "GIT_REPOSITORY_UNAVAILABLE",
		"fatal: Authentication failed for 'https://x'": "GIT_AUTH_FAILED",
		"error: 403 Forbidden":                         "GIT_ACCESS_DENIED",
		"fatal: unable to connect":                     "GIT_FETCH_FAILED",
	} {
		var ce *contract.Error
		if err := remoteFailure(diagnostic, "GIT_FETCH_FAILED", "fetch failed"); !errors.As(err, &ce) || ce.Code != want {
			t.Errorf("%q => %v, want %s", diagnostic, err, want)
		}
	}
}
