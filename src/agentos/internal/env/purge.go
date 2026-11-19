package env

import "context"

// Phase-2 extension (additive): Env.Delete has no options, so the "purge"
// flag of DELETE /envs/{id}?purge=1 travels in the context. Providers check
// IsPurge(ctx) in Delete to also remove the underlying container / instance
// data; without it they should keep data where that makes sense.

type purgeKey struct{}

func WithPurge(ctx context.Context) context.Context { return context.WithValue(ctx, purgeKey{}, true) }

func IsPurge(ctx context.Context) bool { v, _ := ctx.Value(purgeKey{}).(bool); return v }
