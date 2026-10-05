package sso

import (
	"net/http"
	"sync/atomic"
)

const SetupPath = Prefix + "setup"

type Bootstrap struct {
	store   Store
	current atomic.Pointer[Handler]
}

func (b *Bootstrap) Current() *Handler { return b.current.Load() }
func (b *Bootstrap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	respond(w, 200, map[string]any{"configured": b.Current() != nil, "configuration": "environment"})
}
