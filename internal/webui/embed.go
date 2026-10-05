// Package webui serves the compiled website without exposing the repository filesystem.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/gitstore"
)

//go:embed dist
var assets embed.FS

func Wrap(next http.Handler) http.Handler {
	root, _ := fs.Sub(assets, "dist")
	return withAssets(root, next)
}

func pageFile(p string) string {
	switch p {
	case "/sourcegraph/plugins/llms.txt":
		return "plugins/llms.txt"
	case "/sourcegraph/plugins.md":
		return "plugins.md"
	case "/sourcegraph", "/sourcegraph/", "/sourcegraph/index.html":
		return "index.html"
	case "/sourcegraph/code", "/sourcegraph/code.html":
		return "code.html"
	case "/sourcegraph/repos", "/sourcegraph/repos.html":
		return "repos.html"
	case "/sourcegraph/repo", "/sourcegraph/repo.html":
		return "repo.html"
	case "/sourcegraph/connect", "/sourcegraph/connect.html":
		return "connect.html"
	case "/sourcegraph/query-errors", "/sourcegraph/query-errors.html":
		return "query-errors.html"
	case "/sourcegraph/operations", "/sourcegraph/operations.html":
		return "operations.html"
	case "/sourcegraph/login", "/sourcegraph/login.html":
		return "login.html"
	case "/sourcegraph/users", "/sourcegraph/users.html":
		return "users.html"
	case "/sourcegraph/api-tokens", "/sourcegraph/api-tokens.html", "/sourcegraph/user-settings", "/sourcegraph/user-settings.html":
		return "user-settings.html"
	case "/sourcegraph/settings", "/sourcegraph/settings.html":
		return "settings.html"
	default:
		if strings.HasPrefix(p, "/sourcegraph/repos/") && gitstore.ValidateRepo(strings.TrimPrefix(p, "/sourcegraph/repos/")) == nil {
			return "repo.html"
		}
		return ""
	}
}

func withAssets(root fs.FS, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/sourcegraph/", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/sourcegraph/api-tokens" || r.URL.Path == "/sourcegraph/api-tokens.html" {
			target := "/sourcegraph/user-settings"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}
		name := pageFile(r.URL.Path)
		page := name != ""
		asset := strings.HasPrefix(r.URL.Path, "/sourcegraph/assets/")
		if !page && !asset {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Use GET or HEAD.", http.StatusMethodNotAllowed)
			return
		}
		if page && strings.HasSuffix(r.URL.Path, ".html") {
			target := strings.TrimSuffix(r.URL.Path, ".html")
			if target == "/sourcegraph/index" {
				target = "/sourcegraph/"
			}
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}
		if asset {
			name = strings.TrimPrefix(r.URL.Path, "/sourcegraph/")
			if path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") || !fs.ValidPath(name) || !allowedAsset(path.Ext(name)) {
				http.NotFound(w, r)
				return
			}
		}
		data, err := fs.ReadFile(root, name)
		if err != nil {
			if page {
				http.Error(w, "Website assets are not built. Run bun run build:web before building the service.", http.StatusServiceUnavailable)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' https: data:; font-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		contentType := mime.TypeByExtension(path.Ext(name))
		if name == "plugins/llms.txt" || name == "plugins.md" {
			contentType = "text/plain; charset=utf-8"
		}
		if path.Ext(name) == ".mjs" || path.Ext(name) == ".js" {
			contentType = "text/javascript; charset=utf-8"
		}
		w.Header().Set("Content-Type", contentType)
		// Revalidate files on deploy; filenames may be stable between builds.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}

func allowedAsset(ext string) bool {
	switch ext {
	case ".js", ".mjs", ".css", ".svg", ".png", ".jpg", ".jpeg", ".webp", ".ico", ".woff", ".woff2":
		return true
	}
	return false
}
