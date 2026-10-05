package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/context4ai/sourcegraph/internal/wasmplugin"
)

type pluginConnectionKey struct{}
type pluginConnection struct {
	off      bool
	defaults []wasmplugin.Request
}

func parsePluginConnection(q url.Values) (pluginConnection, bool) {
	values, exists := q["plugins"]
	if !exists {
		return pluginConnection{}, true
	}
	if len(values) != 1 {
		return pluginConnection{}, false
	}
	switch values[0] {
	case "auto":
		return pluginConnection{}, true
	case "off":
		return pluginConnection{off: true}, true
	case "":
		return pluginConnection{}, false
	}
	names := strings.Split(values[0], ",")
	if len(names) > 16 {
		return pluginConnection{}, false
	}
	config := pluginConnection{}
	seen := map[string]bool{}
	for _, name := range names {
		if !wasmplugin.ValidName(name) || seen[name] {
			return pluginConnection{}, false
		}
		seen[name] = true
		config.defaults = append(config.defaults, wasmplugin.Request{Name: name})
	}
	return config, true
}

func pluginConnectionHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		config, valid := parsePluginConnection(q)
		if err != nil || !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "INVALID_PLUGINS", "message": "Use plugins=auto, plugins=off, or up to 16 distinct comma-separated plugin names."})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), pluginConnectionKey{}, config)))
	})
}

func connectionPlugins(ctx context.Context, explicit []wasmplugin.Request) []wasmplugin.Request {
	config, _ := ctx.Value(pluginConnectionKey{}).(pluginConnection)
	if config.off {
		return []wasmplugin.Request{}
	}
	if explicit != nil {
		return explicit
	}
	return config.defaults
}
