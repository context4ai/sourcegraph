package identity

import "context"

type prepareKey struct{}

func WithPreparePermission(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, prepareKey{}, allowed)
}
func CanPrepare(ctx context.Context) bool {
	allowed, _ := ctx.Value(prepareKey{}).(bool)
	return allowed
}
