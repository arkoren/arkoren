package bodylimit

import (
	"net/http"

	"arkoren.dev/core/http/internal/bodysize"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
)

// settings configures the body limit.
type settings struct {
	// Limit is the maximum body size in bytes. Zero or negative uses
	// [request.DefaultMaxBodySize].
	Limit int64
}

// Builder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(bodylimit.Middleware(func(limiter *bodylimit.Builder) {
//		limiter.Limit(1 << 20)
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// Limit is the maximum body size in bytes. Zero or negative uses
// [request.DefaultMaxBodySize].
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(bodylimit.Middleware(func(limiter *bodylimit.Builder) {
//		limiter.Limit(1024)
//	}))
func (limiter *Builder) Limit(limit int64) *Builder {
	limiter.settings.Limit = limit

	return limiter
}

// Middleware returns middleware that caps request bodies for a whole router
// or group. A request whose Content-Length exceeds the limit is rejected with
// [request.ErrBodyTooLarge] (413) before the next handler runs. Otherwise the
// limit is stored in the request context, where the request body helpers read
// it instead of [request.DefaultMaxBodySize], and the body is wrapped in
// [http.MaxBytesReader], so a body that turns out larger while streaming (or
// has no Content-Length) fails the handler's read.
//
// A nil body is treated as empty input. The middleware replaces the context
// and body only on a shallow request copy; it does not read or close the body.
// A non-nil body stream remains shared: downstream reads consume it, and closing
// the wrapped body closes the original stream.
//
// The wrapped body is a hard ceiling: neither request.MaxSize nor
// request.Unlimited, nor a nested bodylimit middleware, can raise it. Mount
// routes that accept larger bodies outside this middleware.
//
// Example:
//
//	r := router.New()
//	r.Use(bodylimit.Middleware(func(limiter *bodylimit.Builder) {
//		limiter.Limit(1 << 20)
//	})) // 1 MiB
func Middleware(configure ...BuilderFunc) router.Middleware {
	config := newSettings(configure...)

	limit := config.Limit

	return func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, r *http.Request) error {
			if r.ContentLength > limit {
				return request.ErrBodyTooLarge
			}

			r = r.WithContext(bodysize.NewContext(r.Context(), limit))

			if r.Body == nil {
				r.Body = http.NoBody
			}

			r.Body = http.MaxBytesReader(writer, r.Body, limit)

			return next(writer, r)
		}
	}
}
