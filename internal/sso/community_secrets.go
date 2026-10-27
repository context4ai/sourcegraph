package sso

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/context4ai/sourcegraph/internal/secretstore"
	"os"
)

type CommunitySecrets interface {
	Get(context.Context, string) ([]byte, error)
	Insert(context.Context, string, []byte) error
}
type oauthCredentials struct{ GoogleID, GoogleSecret, GitHubID, GitHubSecret string }

func communityCredentials(ctx context.Context, vaults []CommunitySecrets) (oauthCredentials, error) {
	seed := oauthCredentials{os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET"), os.Getenv("GITHUB_CLIENT_ID"), os.Getenv("GITHUB_CLIENT_SECRET")}
	if len(vaults) == 0 || vaults[0] == nil {
		return seed, nil
	}
	var result oauthCredentials
	v := vaults[0]
	data, e := v.Get(ctx, "community-oauth")
	if errors.Is(e, secretstore.ErrMissing) {
		if seed == (oauthCredentials{}) {
			return seed, nil
		}
		if (seed.GoogleID == "") != (seed.GoogleSecret == "") || (seed.GitHubID == "") != (seed.GitHubSecret == "") {
			return result, errors.New("OAuth client ID and secret must be configured together")
		}
		data, _ = json.Marshal(seed)
		if e = v.Insert(ctx, "community-oauth", data); e != nil && !errors.Is(e, secretstore.ErrExists) {
			return result, e
		}
		data, e = v.Get(ctx, "community-oauth")
	}
	if e != nil {
		return result, e
	}
	if json.Unmarshal(data, &result) != nil {
		return result, errors.New("persisted OAuth configuration is invalid")
	}
	return result, nil
}
