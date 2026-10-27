package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigureListener(t *testing.T) {
	defer func() { networkListener = false }()
	for host, want := range map[string]bool{"::": true, "0.0.0.0": true, "10.1.2.3": true, "127.0.0.1": false, "::1": false, "localhost": false} {
		ConfigureListener(host)
		if networkListener != want {
			t.Errorf("ConfigureListener(%q) = %v, want %v", host, networkListener, want)
		}
	}
}

func TestLoopbackRequestWithPublicHost(t *testing.T) {
	defer func() { networkListener = false }()
	for _, exposed := range []bool{false, true} {
		if exposed {
			ConfigureListener("::")
		} else {
			ConfigureListener("127.0.0.1")
		}
		srv := httptest.NewServer(New(nil))
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Host = "context.example"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if exposed && resp.StatusCode == http.StatusForbidden {
			t.Fatalf("network listener must accept proxied requests with a public Host")
		}
		if !exposed && resp.StatusCode != http.StatusForbidden {
			t.Fatalf("loopback-only listener must keep DNS rebinding protection, got %d", resp.StatusCode)
		}
	}
}
