// Package identity carries an authenticated audit actor, never caller headers.
package identity

import (
	"context"
	"strings"
)

type actorKey struct{}

func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}
func Actor(ctx context.Context) string {
	if actor, ok := ctx.Value(actorKey{}).(string); ok && actor != "" {
		return actor
	}
	return "pilot" // Existing machine-token receipts retain their namespace.
}

type anonymousKey struct{}

func WithAnonymous(ctx context.Context, actor string) context.Context {
	return context.WithValue(WithActor(ctx, actor), anonymousKey{}, true)
}
func IsAnonymous(ctx context.Context) bool {
	value, _ := ctx.Value(anonymousKey{}).(bool)
	return value
}

// UserID is derived exclusively from an authenticated server-side actor.
func UserID(ctx context.Context) string {
	actor := Actor(ctx)
	if strings.HasPrefix(actor, "sso:") {
		return strings.TrimPrefix(actor, "sso:")
	}
	if parts := strings.Split(actor, ":"); len(parts) == 3 && parts[0] == "api-key" {
		return parts[1]
	}
	return ""
}

type adminKey struct{}

// WithAdmin records that the authenticated caller holds site-wide management
// authority: an administrator session or the deployment service credential.
func WithAdmin(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminKey{}, true)
}
func IsAdmin(ctx context.Context) bool {
	value, _ := ctx.Value(adminKey{}).(bool)
	return value
}

// ReadsAllRepositories reports whether repository visibility does not narrow
// the caller: an administrator session, the deployment credential, or
// background work that carries no request identity. Every HTTP request is
// anonymous, a session or a personal token by the time it reaches the service,
// so an empty actor never comes from a caller.
func ReadsAllRepositories(ctx context.Context) bool {
	if IsAdmin(ctx) {
		return true
	}
	actor, _ := ctx.Value(actorKey{}).(string)
	return actor == "" && !IsAnonymous(ctx)
}

type ownerAdminKey struct{}

// WithTokenOwnerAdmin records that a personal token's owner is currently an
// administrator. It only widens index preparation of repositories the token can
// see, never repository visibility or site management.
func WithTokenOwnerAdmin(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownerAdminKey{}, true)
}
func TokenOwnerAdmin(ctx context.Context) bool {
	value, _ := ctx.Value(ownerAdminKey{}).(bool)
	return value
}
