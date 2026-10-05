package wasmplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/fixture.wasm")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestRealPluginABI(t *testing.T) {
	c := DefaultConfig()
	h := New(c)
	defer h.Close()
	b := fixture(t)
	meta, e := Inspect(b, c.MetadataBytes)
	if e != nil || meta.Title != "ABI test plugin" {
		t.Fatalf("metadata: %+v %v", meta, e)
	}
	reads := 0
	reader := func(_ context.Context, p string) ([]byte, error) {
		reads++
		if p != "fixture.json" {
			return nil, errors.New("missing")
		}
		return []byte(`{"flexible":[1,"two",null]}`), nil
	}
	for _, tc := range []struct{ action, want string }{{"scalar", "42"}, {"read", `{"flexible":[1,"two",null]}`}, {"batch", "true"}, {"counter", "1"}, {"counter", "2"}} {
		got := h.Run(context.Background(), b, "same-repo-sha-scope", map[string]any{"action": tc.action}, reader)
		if string(got) != tc.want {
			t.Fatalf("%s: %s", tc.action, got)
		}
	}
	got := h.Run(context.Background(), b, "different-scope", map[string]any{"action": "counter"}, reader)
	if string(got) != "1" {
		t.Fatalf("state crossed scope: %s", got)
	}
	if reads != 3 {
		t.Fatalf("host calls: %d", reads)
	}
}
func TestFailuresPreserveHost(t *testing.T) {
	c := DefaultConfig()
	c.Timeout = 2 * time.Second
	c.OutputBytes = 100
	h := New(c)
	defer h.Close()
	b := fixture(t)
	for _, tc := range []struct{ action, code string }{{"loop", "PLUGIN_TIMEOUT"}, {"trap", "PLUGIN_FAILED"}, {"badptr", "PLUGIN_INVALID_OUTPUT"}, {"invalid", "PLUGIN_INVALID_OUTPUT"}} {
		ctx, cancel := context.WithCancel(context.Background())
		if tc.action == "loop" {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
		}
		out := h.Run(ctx, b, "key", map[string]any{"action": tc.action}, nil)
		cancel()
		if !json.Valid(out) || !strings.Contains(string(out), tc.code) {
			t.Fatalf("%s: %s", tc.action, out)
		}
		out = h.Run(context.Background(), b, "key", map[string]any{"action": "counter"}, nil)
		if string(out) != "1" {
			t.Fatalf("failed instance was retained: %s", out)
		}
	}
	out := h.Run(context.Background(), b, "key", map[string]any{"large": strings.Repeat("x", 200)}, nil)
	if !strings.Contains(string(out), "PLUGIN_OUTPUT_TOO_LARGE") {
		t.Fatal(string(out))
	}
}
func TestDiscoveryNeverExecutes(t *testing.T) {
	b := fixture(t)
	for i := 0; i < 3; i++ {
		if _, e := Inspect(b, 1<<20); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := Inspect([]byte("version https://git-lfs.github.com/spec/v1"), 1000); e == nil {
		t.Fatal("accepted LFS")
	}
	if _, e := Inspect(b, 4); e == nil {
		t.Fatal("metadata budget ignored")
	}
}
func TestCancelledAndClosedHost(t *testing.T) {
	h := New(DefaultConfig())
	b := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := h.Run(ctx, b, "key", map[string]string{"action": "scalar"}, nil)
	if !strings.Contains(string(out), "PLUGIN_TIMEOUT") {
		t.Fatal(string(out))
	}
	h.Close()
	out = h.Run(context.Background(), b, "key", nil, nil)
	if !strings.Contains(string(out), "issues") {
		t.Fatal(string(out))
	}
}

func TestConcurrentIsolationEvictionAndBusy(t *testing.T) {
	c := DefaultConfig()
	c.CacheEntries = 1
	c.IdleInstances = 1
	c.Concurrency = 1
	h := New(c)
	defer h.Close()
	b := fixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan json.RawMessage)
	go func() {
		finished <- h.Run(context.Background(), b, "a", map[string]string{"action": "read"}, func(context.Context, string) ([]byte, error) { close(started); <-release; return []byte(`true`), nil })
	}()
	<-started
	out := h.Run(context.Background(), b, "b", map[string]string{"action": "scalar"}, nil)
	if !strings.Contains(string(out), "PLUGIN_BUSY") {
		t.Fatal(string(out))
	}
	close(release)
	if string(<-finished) != "true" {
		t.Fatal("first request failed")
	}
	// A different valid custom section gives a distinct binary and evicts cache 1.
	other := append(append([]byte(nil), b...), 0, 3, 1, 'x', 0)
	out = h.Run(context.Background(), other, "b", map[string]string{"action": "counter"}, nil)
	if string(out) != "1" {
		t.Fatal(string(out))
	}
	out = h.Run(context.Background(), b, "a", map[string]string{"action": "counter"}, nil)
	if string(out) != "1" {
		t.Fatal("evicted instance survived: " + string(out))
	}
}
func TestReadBudgets(t *testing.T) {
	c := DefaultConfig()
	c.FileBytes = 2
	h := New(c)
	defer h.Close()
	out := h.Run(context.Background(), fixture(t), "key", map[string]string{"action": "read"}, func(context.Context, string) ([]byte, error) { return []byte("123"), nil })
	if !strings.Contains(string(out), "FIXTURE_READ_FAILED") {
		t.Fatal(string(out))
	}
}

func TestPrecompileDoesNotInstantiateAndIsReusable(t *testing.T) {
	h := New(DefaultConfig())
	defer h.Close()
	ctx := context.Background()
	b := fixture(t)
	if err := h.Precompile(ctx, b); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	if len(h.entries) != 1 || h.idleCount != 0 {
		t.Error("prewarm should compile without guest instances")
	}
	h.mu.Unlock()
	entry, hit, err := h.acquire(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("compiled artifact was not reused")
	}
	h.release(entry, nil, "", false)
}

func TestMetadataRequiresVersionTwo(t *testing.T) {
	b, err := os.ReadFile("testdata/fixture.wasm")
	if err != nil {
		t.Fatal(err)
	}
	old := bytes.ReplaceAll(b, []byte(`"abi_version":2`), []byte(`"abi_version":1`))
	if _, err = Inspect(old, 256<<10); err == nil {
		t.Fatal("old JSON ABI accepted")
	}
	if _, err = Inspect([]byte{0, 97, 115, 109, 1, 0, 0, 0}, 256<<10); err == nil {
		t.Fatal("undeclared JSON ABI accepted")
	}
}
