// Package bodysize carries the request body size limit through a
// context.Context, from the body-limit middleware
// (arkoren.dev/core/http/middleware/bodylimit) to the request body helpers
// (arkoren.dev/core/http/request). It is internal so that only the middleware
// can set the limit; handlers cannot widen it by writing to the context.
// The value is a byte count; request helpers interpret absence using their own
// defaults. This context metadata does not itself wrap or limit an io.Reader.
//
//	ctx := bodysize.NewContext(ctx, 1<<20)
//	limit, present := bodysize.FromContext(ctx)
//	fmt.Println(limit, present)
package bodysize

import "context"

// contextKey is the context key under which the limit is stored.
type contextKey struct{}

// NewContext returns a copy of ctx that carries n as the body size limit.
// It retains no reader and performs no validation; the middleware owns policy
// and actual enforcement. A nil parent follows context.WithValue's panic rule.
//
// Example: ctx := bodysize.NewContext(ctx, 1<<20)
func NewContext(ctx context.Context, n int64) context.Context {
	return context.WithValue(ctx, contextKey{}, n)
}

// FromContext returns the limit stored in ctx and whether there is one.
// Absence returns zero and false, distinct from an explicitly stored zero.
//
// Example: limit, present := bodysize.FromContext(bodysize.NewContext(ctx, 1024)); fmt.Println(limit, present)
func FromContext(ctx context.Context) (int64, bool) {
	n, ok := ctx.Value(contextKey{}).(int64)

	return n, ok
}
