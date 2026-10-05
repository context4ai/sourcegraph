package httpserver

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/context4ai/sourcegraph/internal/config"
	"github.com/context4ai/sourcegraph/internal/contract"
)

// Public access is method/path specific. A future management route
// cannot become public merely by sharing the same URL prefix.
func publicRead(r *http.Request) bool {
	if r.Method == "GET" {
		switch r.URL.Path {
		case Prefix + "/v1/repo/plugins", Prefix + "/v1/capabilities", Prefix + "/v1/repos", Prefix + "/v1/repo/availability", Prefix + "/v1/repo/commits", Prefix + "/api/admin/v1/repos", Prefix + "/api/admin/v1/repo", Prefix + "/api/admin/v1/statistics":
			return true
		}
	}
	if r.Method == "POST" {
		switch r.URL.Path {
		case Prefix + "/api/search", Prefix + "/v1/repo/resolve", Prefix + "/v1/repo/read", Prefix + "/v1/repo/list", Prefix + "/v1/repo/diff":
			return true
		}
	}
	return false
}

func accessPolicy(c config.Config, backend, login bool) map[string]bool {
	return map[string]bool{"public_read": backend && c.WebsitePublicRead, "public_prepare": false, "sso_enabled": backend && login}
}

func sameOrigin(r *http.Request, configured string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if configured != "" {
		return origin == configured
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Host != r.Host {
		return false
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	return u.Scheme == "https" || (u.Scheme == "http" && loopback)
}

func anonymousActor(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Never trust client-provided forwarding headers for attribution or limits.
	return fmt.Sprintf("anonymous:%x", sha256.Sum256([]byte(host)))
}

type bucket struct {
	until time.Time
	count int
}
type websiteLimits struct {
	mu      sync.Mutex
	buckets map[string]bucket
}

func (l *websiteLimits) allow(actor, kind string, perClient, total int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.buckets == nil {
		l.buckets = map[string]bucket{}
	}
	if len(l.buckets) >= 2048 {
		for k, b := range l.buckets {
			if !now.Before(b.until) {
				delete(l.buckets, k)
			}
		}
	}
	keys := []string{kind + ":all", kind + ":" + actor}
	maxima := []int{total, perClient}
	for i, key := range keys {
		b, ok := l.buckets[key]
		if !ok && len(l.buckets) >= 2048 {
			return false
		}
		if now.Before(b.until) && b.count >= maxima[i] {
			return false
		}
	}
	for _, key := range keys {
		b := l.buckets[key]
		if !now.Before(b.until) {
			b = bucket{until: now.Add(time.Minute)}
		}
		b.count++
		l.buckets[key] = b
	}
	return true
}

func (l *websiteLimits) authorizePrepare(r *http.Request, c config.Config) error {
	return contract.Fail("PREPARATION_NOT_ALLOWED", "Preparation requires an administrator session or service credential.", 403)
}

// Keep logging accurate even when session authorization or an adapter returns
// its own HTTP status. Unwrap supports the standard ResponseController.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
