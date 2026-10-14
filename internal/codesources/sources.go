// Package codesources resolves the fixed code-host slots of this distribution.
// Values come from the release defaults, then the website settings for the
// editable slot, then the service configuration file, which always wins.
package codesources

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"unicode"

	deployment "github.com/context4ai/sourcegraph/config"
	"github.com/context4ai/sourcegraph/internal/contract"
)

// Override sets individual fields; empty fields inherit the lower layer.
type Override struct {
	Name     string `json:"name,omitempty"`
	Host     string `json:"host,omitempty"`
	Username string `json:"username,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
}

type Source struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Host      string   `json:"host"`
	Username  string   `json:"username"`
	Anonymous bool     `json:"anonymous"`
	Editable  bool     `json:"editable"`
	Locked    []string `json:"locked,omitempty"`
	TokenEnv  string   `json:"-"`
}

var (
	file     atomic.Pointer[map[string]Override]
	settings atomic.Pointer[func() Override]

	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
	envPattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// Configure installs the service configuration file layer once at startup.
func Configure(overrides map[string]Override) error {
	if err := Validate(overrides); err != nil {
		return err
	}
	copied := make(map[string]Override, len(overrides))
	for id, o := range overrides {
		o.Host, _ = NormalizeHost(o.Host)
		copied[id] = o
	}
	file.Store(&copied)
	return nil
}

// Validate rejects unknown slots and malformed values in the configuration file.
func Validate(overrides map[string]Override) error {
	known := map[string]bool{}
	for _, s := range deployment.Load().CodeSources {
		known[s.ID] = true
	}
	for id, o := range overrides {
		if !known[id] {
			return fmt.Errorf("code_sources.%s is not a code source of this distribution", id)
		}
		if err := validate(o); err != nil {
			return fmt.Errorf("code_sources.%s: %s", id, err.Error())
		}
		if o.TokenEnv != "" && !envPattern.MatchString(o.TokenEnv) {
			return fmt.Errorf("code_sources.%s: token_env must be an upper-case environment variable name", id)
		}
	}
	return nil
}

// UseSettings connects the website settings layer of the editable slot.
func UseSettings(read func() Override) { settings.Store(&read) }

// ValidateSettings checks website input; the configuration file still decides locked fields.
func ValidateSettings(o Override) error {
	if o.TokenEnv != "" {
		return contract.Fail("INVALID_CODE_SOURCE", "Token variables can only be set in the service configuration file.", 400)
	}
	if err := validate(o); err != nil {
		return contract.Fail("INVALID_CODE_SOURCE", err.Error(), 400)
	}
	return nil
}

func validate(o Override) error {
	if o.Name != "" && (len([]rune(o.Name)) > 40 || strings.IndexFunc(o.Name, unicode.IsControl) >= 0) {
		return fmt.Errorf("name must be at most 40 printable characters")
	}
	if o.Username != "" && !usernamePattern.MatchString(o.Username) {
		return fmt.Errorf("username may contain only letters, digits and . _ @ -")
	}
	if o.Host != "" {
		if _, err := NormalizeHost(o.Host); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeHost accepts only an HTTPS origin; paths, credentials and queries are rejected.
func NormalizeHost(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("host must be an HTTPS origin such as https://git.example.com")
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// List returns the visible slots in release order with all layers applied.
func List() []Source {
	var ui Override
	if read := settings.Load(); read != nil {
		ui = (*read)()
	}
	return Effective(ui)
}

// Effective resolves the slots as if the website layer held ui.
func Effective(ui Override) []Source {
	overrides := map[string]Override{}
	if f := file.Load(); f != nil {
		overrides = *f
	}
	defaults := deployment.Load().CodeSources
	out := make([]Source, 0, len(defaults))
	for _, d := range defaults {
		s := Source{ID: d.ID, Name: d.Name, Host: d.Host, Username: d.Username, TokenEnv: d.TokenEnv, Anonymous: d.Anonymous, Editable: d.Editable}
		f := overrides[d.ID]
		if d.Editable {
			if ui.Name != "" && f.Name == "" {
				s.Name = ui.Name
			}
			if host, _ := NormalizeHost(ui.Host); host != "" && f.Host == "" {
				s.Host = host
			}
			if ui.Username != "" && f.Username == "" {
				s.Username = ui.Username
			}
		}
		for field, value := range map[string]string{"name": f.Name, "host": f.Host, "username": f.Username, "token_env": f.TokenEnv} {
			if value != "" {
				s.Locked = append(s.Locked, field)
			}
		}
		sort.Strings(s.Locked)
		if f.Name != "" {
			s.Name = f.Name
		}
		if f.Host != "" {
			s.Host = f.Host
		}
		if f.Username != "" {
			s.Username = f.Username
		}
		if f.TokenEnv != "" {
			s.TokenEnv = f.TokenEnv
		}
		if s.Username == "" {
			s.Username = "oauth2"
		}
		out = append(out, s)
	}
	return out
}

// Lookup finds a visible slot by its exact ID.
func Lookup(id string) (Source, bool) {
	for _, s := range List() {
		if s.ID == id {
			return s, true
		}
	}
	return Source{}, false
}

// Resolve maps a repository's code_provider, including retired aliases, to its slot.
func Resolve(provider string) (Source, bool) {
	if target, ok := deployment.Load().CodeSourceAliases[provider]; ok {
		provider = target
	}
	return Lookup(provider)
}

// IsAlias reports a retired provider kept only for already registered repositories.
func IsAlias(provider string) bool {
	_, ok := deployment.Load().CodeSourceAliases[provider]
	return ok
}

// Kinds lists every credential kind: visible slots first, then retired aliases.
func Kinds() []string {
	d := deployment.Load()
	out := []string{}
	for _, s := range d.CodeSources {
		out = append(out, s.ID)
	}
	aliases := []string{}
	for alias := range d.CodeSourceAliases {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return append(out, aliases...)
}

// EnvToken returns the deployment environment token of a visible slot.
func EnvToken(kind string) string {
	if s, ok := Lookup(kind); ok && s.TokenEnv != "" {
		return os.Getenv(s.TokenEnv)
	}
	return ""
}

// AllowsAnonymous reports whether a provider may fetch public repositories without a token.
func AllowsAnonymous(provider string) bool {
	s, ok := Resolve(provider)
	return ok && s.Anonymous
}

// EditableSource returns the slot the website settings may change.
func EditableSource() (Source, bool) {
	for _, s := range List() {
		if s.Editable {
			return s, true
		}
	}
	return Source{}, false
}
