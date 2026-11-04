package httpserver

import (
	"context"
	"sync"
	"time"

	"github.com/context4ai/sourcegraph/internal/identity"
	"github.com/context4ai/sourcegraph/internal/service"
)

// preparerCache decides whether a personal token's MCP session lists prepare.
// Stateless MCP asks on every request, so the registry scan is cached for a
// minute per user; errors fail closed to the read-only tool set.
type preparerCache struct {
	mu      sync.Mutex
	entries map[string]preparerEntry
}
type preparerEntry struct {
	allowed bool
	until   time.Time
}

func (c *preparerCache) allowed(ctx context.Context, s *service.Service) bool {
	if identity.TokenOwnerAdmin(ctx) {
		return true
	}
	user := identity.UserID(ctx)
	if user == "" || s == nil {
		return false
	}
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[user]; ok && now.Before(e.until) {
		c.mu.Unlock()
		return e.allowed
	}
	c.mu.Unlock()
	lookup, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	allowed, err := s.ManagesAnyRepository(lookup, user)
	if err != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= 4096 {
		c.entries = map[string]preparerEntry{}
	}
	c.entries[user] = preparerEntry{allowed: allowed, until: now.Add(time.Minute)}
	return allowed
}
