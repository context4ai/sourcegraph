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
