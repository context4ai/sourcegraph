package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPluginDocumentationRoutes(t *testing.T) {
	root := fstest.MapFS{"plugins/llms.txt": {Data: []byte("# Plugin guide")}, "plugins.md": {Data: []byte("# Plugin ABI")}}
	handler := withAssets(root, http.NotFoundHandler())
	for _, path := range []string{"/sourcegraph/plugins/llms.txt", "/sourcegraph/plugins.md"} {
		for _, method := range []string{"GET", "HEAD", "POST"} {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			if method == "POST" {
				if w.Code != 405 {
					t.Fatal(w.Code)
				}
				continue
			}
			if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
				t.Fatalf("%s %s: %d %v", method, path, w.Code, w.Header())
			}
			if method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD returned body")
			}
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/sourcegraph/other.txt", nil))
	if w.Code != 404 {
		t.Fatal("unlisted file exposed")
	}
}
