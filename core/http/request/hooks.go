package request

import (
	"net/http"

	"arkoren.dev/core/internal/hook"
)

// ErrNoHooksMiddleware is the problem raised when hooks are accessed on a
// request that was not served through a router.Handler, which installs them.
// It is a construction/wiring mistake rather than optional hook absence.
//
// Example:
//
//	failure := request.ErrNoHooksMiddleware.Derive(func(details *problem.Builder) {
//		details.Detail("The request was not served through the router.")
//	})
//	_ = failure.Status()
var ErrNoHooksMiddleware = hook.ErrNoHooks

// HookRegistry provides lifecycle hook registration for an Arkoren HTTP request.
// Obtain it with [Hooks] only inside a router handler. Callbacks remain attached
// to that request; registering one does not execute it immediately.
//
// Example:
//
//	registry := request.Hooks(r)
//	registry.BeforeWriteHeader(func(writer http.ResponseWriter, _ int) {
//		writer.Header().Set("Cache-Control", "no-store")
//	})
type HookRegistry interface {
	// AfterResponse registers callbacks that run after the response is completed.
	// The argument is the handler's returned error, not proof of client delivery.
	//
	// Example:
	//
	//	request.Hooks(r).AfterResponse(func(err error) {
	//		if err != nil {
	//			slog.ErrorContext(r.Context(), "request failed", "error", err)
	//		}
	//	})
	AfterResponse(...func(error))
	// BeforeWrite registers callbacks that run before response bytes are written.
	// A callback observes an attempted write, which can still fail afterward.
	//
	// Example:
	//
	//	request.Hooks(r).BeforeWrite(func(_ http.ResponseWriter, data []byte) {
	//		slog.DebugContext(r.Context(), "response write", "bytes", len(data))
	//	})
	BeforeWrite(...func(http.ResponseWriter, []byte))
	// BeforeWriteHeader registers callbacks before final response commitment.
	// Use it to change headers before the writer accepts the final status.
	//
	// Example:
	//
	//	request.Hooks(r).BeforeWriteHeader(func(writer http.ResponseWriter, _ int) {
	//		writer.Header().Set("Cache-Control", "no-store")
	//	})
	BeforeWriteHeader(...func(http.ResponseWriter, int))
}

// Hooks returns the hooks instance used to attach callbacks to lifecycle
// events. It panics with [ErrNoHooksMiddleware] when the request was not
// served through a router.Handler. There is no non-panicking public alternative;
// use it only where router wiring guarantees lifecycle-hook installation.
//
// Example:
//
//	registry := request.Hooks(r)
//	registry.BeforeWriteHeader(func(writer http.ResponseWriter, _ int) {
//		writer.Header().Set("Cache-Control", "no-store")
//	})
func Hooks(r *http.Request) HookRegistry {
	if registry, ok := hook.FromContext(r.Context()); ok {
		return registry
	}

	panic(ErrNoHooksMiddleware)
}
