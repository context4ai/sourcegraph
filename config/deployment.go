// Package deployment contains non-secret defaults for this distribution.
package deployment

import (
	_ "embed"
	"encoding/json"
)

//go:embed deployment.json
var raw []byte

// CodeSource is a distribution default; token_env names an environment variable, never a token.
type CodeSource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Username  string `json:"username"`
	TokenEnv  string `json:"token_env"`
	Anonymous bool   `json:"anonymous"`
	Editable  bool   `json:"editable"`
}

type Defaults struct {
	SSO struct {
		Issuer          string `json:"issuer"`
		Endpoints       string `json:"endpoints"`
		ClientID        string `json:"client_id"`
		RegistrationURL string `json:"registration_url"`
	} `json:"sso"`
	CodeSources       []CodeSource      `json:"code_sources"`
	CodeSourceAliases map[string]string `json:"code_source_aliases"`
	ExampleRepo       string            `json:"example_repo"`
	PublicURL         string            `json:"public_url"`
}

func Load() Defaults {
	var value Defaults
	if err := json.Unmarshal(raw, &value); err != nil {
		panic("invalid deployment defaults")
	}
	return value
}
