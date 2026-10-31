// Package resend carries the number of times an outgoing HTTP request has
// been resent through a [context.Context], from a retrying round tripper
// (arkoren.dev/core/http/client) to the instrumented one it wraps
// (arkoren.dev/core/service/telemetry), which records it as http.request.resend_count.
// The original request is attempt zero; callers attach a new count per retry.
//
//	ctx := resend.NewContext(ctx, 1)
//	fmt.Println(resend.FromContext(ctx)) // 1
package resend

import "context"

// key is the context key for the resend count.
type key struct{}

// NewContext returns a copy of ctx that records count resends: 1 for the
// first resend of a request, 2 for the second, and so on.
// It performs no I/O or range validation; the retrying client owns the count.
// A nil parent follows context.WithValue's panic rule.
//
// Example: ctx := resend.NewContext(ctx, 1)
func NewContext(ctx context.Context, count int) context.Context {
	return context.WithValue(ctx, key{}, count)
}

// FromContext returns the resend count stored by [NewContext], zero for the
// first attempt of a request.
// Contexts without an attached count also return zero and are not mutated.
//
// Example: count := resend.FromContext(resend.NewContext(ctx, 2)); fmt.Println(count)
func FromContext(ctx context.Context) int {
	count, _ := ctx.Value(key{}).(int)

	return count
}
