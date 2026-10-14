package codesources

import (
	"slices"
	"testing"
)

func reset(t *testing.T) {
	t.Helper()
	file.Store(nil)
	settings.Store(nil)
	t.Cleanup(func() { file.Store(nil); settings.Store(nil) })
}

func byID(t *testing.T, id string) Source {
	t.Helper()
	s, ok := Lookup(id)
	if !ok {
		t.Fatalf("missing source %s", id)
	}
	return s
}

func TestReleaseDefaultsListGitHubFirst(t *testing.T) {
	reset(t)
	got := List()
	if len(got) != 2 || got[0].ID != "github" || got[1].ID != "git" {
		t.Fatalf("sources = %#v", got)
	}
	if !got[0].Anonymous || got[0].Editable || !got[1].Editable || got[1].Host != "" {
		t.Fatalf("unexpected defaults: %#v", got)
	}
}

func TestWebsiteLayerOnlyChangesEditableSource(t *testing.T) {
	reset(t)
	UseSettings(func() Override {
		return Override{Name: "Company Git", Host: "https://Git.Example.com/", Username: "bot"}
	})
	if s := byID(t, "git"); s.Name != "Company Git" || s.Host != "https://git.example.com" || s.Username != "bot" {
		t.Fatalf("editable source = %#v", s)
	}
	if s := byID(t, "github"); s.Host != "https://github.com" || s.Name != "GitHub" {
		t.Fatalf("github must ignore website values: %#v", s)
	}
}

func TestConfigurationFileWinsAndLocksFields(t *testing.T) {
	reset(t)
	UseSettings(func() Override { return Override{Name: "From website", Host: "https://web.example.com"} })
	if err := Configure(map[string]Override{"git": {Host: "https://file.example.com", TokenEnv: "CUSTOM_GIT_TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	s := byID(t, "git")
	if s.Host != "https://file.example.com" || s.Name != "From website" || s.TokenEnv != "CUSTOM_GIT_TOKEN" {
		t.Fatalf("layered source = %#v", s)
	}
	if !slices.Equal(s.Locked, []string{"host", "token_env"}) {
		t.Fatalf("locked = %#v", s.Locked)
	}
}

func TestKindsAndAnonymousAccess(t *testing.T) {
	reset(t)
	if !slices.Equal(Kinds(), []string{"github", "git"}) || IsAlias("git") {
		t.Fatalf("kinds = %#v", Kinds())
	}
	if !AllowsAnonymous("github") || AllowsAnonymous("git") {
		t.Fatal("only github allows anonymous access")
	}
}

func TestValidationRejectsUnsafeValues(t *testing.T) {
	for _, host := range []string{"http://git.example.com", "https://user@git.example.com", "https://git.example.com/group", "https://git.example.com?a=1", "git.example.com"} {
		if _, err := NormalizeHost(host); err == nil {
			t.Fatalf("host %q accepted", host)
		}
	}
	if Validate(map[string]Override{"gitlab": {Host: "https://git.example.com"}}) == nil {
		t.Fatal("unknown source accepted")
	}
	if Validate(map[string]Override{"git": {TokenEnv: "lower"}}) == nil {
		t.Fatal("invalid token_env accepted")
	}
	if ValidateSettings(Override{TokenEnv: "ANY"}) == nil {
		t.Fatal("website must not set token variables")
	}
}
