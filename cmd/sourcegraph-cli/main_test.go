package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func TestSearchRGFlagsReachHTTP(t *testing.T) {
	var received map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sourcegraph/api/search" || r.URL.Query().Get("repo") != "org/repo" {
			t.Errorf("route: %s", r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	transport, args := http.DefaultTransport, os.Args
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = transport; os.Args = args }()
	t.Setenv("SOURCEGRAPH_URL", server.URL+"/sourcegraph")
	t.Setenv("SOURCEGRAPH_API_TOKEN", "")
	for _, tc := range []struct {
		args    []string
		pattern string
		fixed   bool
		paths   []any
	}{
		{[]string{"-F", "-i", "-l", "-g", "!test.go", "-g", "*.go", "-e", "-foo(", "src"}, "-foo(", true, []any{"src"}},
		{[]string{"-i", "-l", "-g", "!test.go", "-g", "*.go", `foo\(`, "src", "lib"}, `foo\(`, false, []any{"src", "lib"}},
	} {
		os.Args = append([]string{"sourcegraph-cli", "search", "-repo", "org/repo"}, tc.args...)
		if code := run(); code != 0 {
			t.Fatalf("exit %d", code)
		}
		if received["Pattern"] != tc.pattern || received["FixedStrings"] != tc.fixed || received["IgnoreCase"] != true || received["Output"] != "files" || !reflect.DeepEqual(received["Paths"], tc.paths) || !reflect.DeepEqual(received["Glob"], []any{"!test.go", "*.go"}) {
			t.Fatalf("request: %+v", received)
		}
	}
}
