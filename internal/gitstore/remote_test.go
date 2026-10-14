package gitstore

import (
	"slices"
	"testing"
)

func TestRemoteUsesRepositoryHost(t *testing.T) {
	if got := remoteURL("group/name", "https://github.com/"); got != "https://github.com/group/name.git" {
		t.Fatalf("remoteURL = %q", got)
	}
}

func TestCredentialEnvScopesTokenToRemoteHost(t *testing.T) {
	got := credentialEnv(remoteURL("group/name", "https://github.com"), FetchOptions{Token: "secret", Username: "bot"})
	if !slices.Equal(got, []string{"SOURCEGRAPH_GIT_TOKEN=secret", "SOURCEGRAPH_GIT_HOST=github.com", "SOURCEGRAPH_GIT_USERNAME=bot"}) {
		t.Fatalf("credentialEnv = %#v", got)
	}
}
