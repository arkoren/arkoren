// Package requestlog logs returned handler errors and observed 5xx responses to
// a [slog.Logger]. Records contain the concrete path and raw error, which can
// contain personal identifiers or secrets; select appropriate access controls,
// retention, and redaction before exporting these logs.
//
// Install logging before recovery so recovered panics are observable errors:
//
//	routes := router.New()
//	routes.Use(requestlog.Middleware(), router.Recover())
//
// Successful hijacks are not failures. Status zero means no final status was
// observed, for example when a handler returns an error after taking ownership
// of the connection. Direct writes to that connection are outside HTTP hooks.
package requestlog

import (
	"log/slog"
	"net/http"

	"arkoren.dev/core/http/router"
)

// settings configures [Middleware].
type settings struct {
	// Logger receives the failed-request records. Nil uses [slog.Default],
	// resolved for every request so later slog.SetDefault calls take effect.
	Logger *slog.Logger
}

// Builder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(requestlog.Middleware(func(log *requestlog.Builder) {
//		log.Logger(slog.Default())
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// Logger receives the failed-request records. Nil uses [slog.Default],
// resolved for every request so later slog.SetDefault calls take effect.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(requestlog.Middleware(func(log *requestlog.Builder) {
//		log.Logger(slog.Default())
//	}))
func (log *Builder) Logger(logger *slog.Logger) *Builder {
	log.settings.Logger = logger

	return log
}

// Middleware logs requests that return an error or respond with a 5xx
// status. Each record carries the method, path, status, and error. It logs
// with the request context, so the telemetry log handler adds the trace and
// span IDs when the telemetry middleware ran earlier. Without a logger it
// uses [slog.Default].
// A successful hijack is not a failure. A returned error after hijacking is
// logged with status zero when no final HTTP status passed through the writer;
// bytes written directly to the owned connection cannot be observed here.
// Paths and raw errors can contain sensitive values. Use an appropriate logger
// and export policy, and place router.Recover inside this middleware to observe
// handler panics as returned errors.
//
// Example:
//
//	r.Use(requestlog.Middleware(func(log *requestlog.Builder) {
//		log.Logger(logger)
//	}))
func Middleware(configure ...BuilderFunc) router.Middleware {
	config := newSettings(configure...)

	return func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, r *http.Request) error {
			observe(r, config.Logger)

			return next(writer, r)
		}
	}
}

// orDefault returns logger, or the default logger when it is nil. It is
// resolved per request so later slog.SetDefault calls take effect.
func orDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}

	return logger
}
