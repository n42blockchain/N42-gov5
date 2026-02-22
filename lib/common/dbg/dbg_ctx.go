package dbg

import "context"

type debugContextKey struct{}

// ContextWithDebug attaches a debug flag to the given context.
func ContextWithDebug(ctx context.Context, v bool) context.Context {
	return context.WithValue(ctx, debugContextKey{}, v)
}

// Enabled returns whether debug logging is enabled in the given context.
func Enabled(ctx context.Context) bool {
	v, _ := ctx.Value(debugContextKey{}).(bool)
	return v
}

// HTTPHeader is the HTTP header name used to enable debug mode.
// Usage: curl --header "dbg: true" <url>
var HTTPHeader = "dbg"
