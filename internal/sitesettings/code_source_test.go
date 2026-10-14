package sitesettings

import (
	"context"
	"testing"
)

func TestCodeSourceSectionSeedsAndUpdates(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	s, err := New(ctx, store, true)
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Snapshot().CodeSource; v.Revision != 1 || v.Values != (CodeSource{}) {
		t.Fatalf("seed = %#v", v)
	}
	if _, err = s.Update(ctx, CodeSourceName, 1, []byte(`{"name":"Company Git","host":"https://Git.Example.com/","username":"bot"}`), "admin"); err != nil {
		t.Fatal(err)
	}
	if o := s.CodeSourceOverride(); o.Host != "https://git.example.com" || o.Name != "Company Git" || o.Username != "bot" {
		t.Fatalf("override = %#v", o)
	}
	if _, err = s.Update(ctx, CodeSourceName, 2, []byte(`{"name":"","host":"http://git.example.com","username":""}`), "admin"); err == nil {
		t.Fatal("non-HTTPS host accepted")
	}
	reloaded, err := New(ctx, store, false)
	if err != nil || reloaded.CodeSourceOverride().Host != "https://git.example.com" {
		t.Fatalf("reload = %#v, %v", reloaded.CodeSourceOverride(), err)
	}
}
