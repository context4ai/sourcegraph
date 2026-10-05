// Package deployment contains non-secret defaults for this distribution.
package deployment

import (
	_ "embed"
	"encoding/json"
)

//go:embed deployment.json
var raw []byte

type Defaults struct {
	SSO struct {
		Issuer          string `json:"issuer"`
		Endpoints       string `json:"endpoints"`
		ClientID        string `json:"client_id"`
		RegistrationURL string `json:"registration_url"`
	} `json:"sso"`
	CodeHost    string `json:"code_host"`
	ExampleRepo string `json:"example_repo"`
	PublicURL   string `json:"public_url"`
}

func Load() Defaults {
	var value Defaults
	if err := json.Unmarshal(raw, &value); err != nil {
		panic("invalid deployment defaults")
	}
	return value
}
