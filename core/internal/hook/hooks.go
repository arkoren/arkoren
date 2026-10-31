// Package hook implements the request lifecycle behind router.Handler and the
// public request.Hooks registration interface. Each request owns a Registry;
// callbacks run in reverse registration order from snapshots taken under a
// mutex, so callbacks can register further hooks without deadlocking.
//
// ResponseWriter dispatches final-status and body callbacks while delegating
// native capabilities through Unwrap. Informational statuses other than 101
// leave the final response pending; successful hijacking commits ownership to
// the caller. Its commitment state supports concurrent observation, not
// arbitrary concurrent writes to a native HTTP response.
//
// Example:
//
//	registry := hook.New()
//	registry.BeforeWriteHeader(func(writer http.ResponseWriter, status int) {
//		writer.Header().Set("X-Service", "example")
//	})
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), registry)
//	writer.WriteHeader(http.StatusNoContent)
package hook

import (
	"context"
	"net/http"
	"slices"
	"sync"

	"arkoren.dev/foundation/problem"
)

// ErrNoHooks is the problem raised when hooks are required on a request
// that was not served through a router.Handler, which installs them.
//
// Example:
//
//	failure := hook.ErrNoHooks.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrNoHooks = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:internal/hook:no-hooks")
	details.Title("No hooks context")
	details.Detail("Unable to resolve hooks as there's no context value")
	details.Status(http.StatusInternalServerError)
})

// contextKey identifies the lifecycle registry in a request context.
type contextKey struct{}

// Registry stores callbacks for one HTTP request lifecycle. Registration and
// snapshot creation are synchronized; callbacks run without holding its mutex
// and in reverse registration order. Function captures remain shared, and nil
// callbacks panic when invoked. Do not copy a Registry after first use.
//
// Example:
//
//	registry := hook.New()
//	registry.AfterResponse(func(err error) {
//		fmt.Println(err == nil)
//	})
//	registry.RunAfterResponse(nil)
type Registry struct {
	// mutex protects the registered lifecycle callbacks.
	mutex sync.Mutex

	// afterResponseHooks run after response handling completes.
	afterResponseHooks []func(error)

	// beforeWriteHeaderHooks run before a response status is written.
	beforeWriteHeaderHooks []func(http.ResponseWriter, int)

	// beforeWriteHooks run before response content is written.
	beforeWriteHooks []func(http.ResponseWriter, []byte)
}

// New creates an empty lifecycle registry for one request. It performs no I/O
// and retains no request until the caller registers callbacks or adds it to a
// context. The request owner controls when each phase runs.
//
// Example:
//
//	registry := hook.New()
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), registry)
//	writer.WriteHeader(http.StatusNoContent)
func New() *Registry {
	return &Registry{}
}

// NewContext derives a context carrying registry without copying it or losing
// parent cancellation and values. The caller supplies a non-nil parent and a
// live registry; the request owner remains responsible for running its phases.
//
// Example:
//
//	registry := hook.New()
//	ctx := hook.NewContext(context.Background(), registry)
//	resolved, ok := hook.FromContext(ctx)
//	fmt.Println(ok, resolved == registry) // true true
func NewContext(ctx context.Context, registry *Registry) context.Context {
	return context.WithValue(ctx, contextKey{}, registry)
}

// FromContext returns the shared lifecycle registry attached by NewContext.
// A context without that value returns nil and false; it does not create a new
// registry. Callers requiring hooks report ErrNoHooks when no value exists.
//
// Example:
//
//	ctx := hook.NewContext(context.Background(), hook.New())
//	if registry, ok := hook.FromContext(ctx); ok {
//		registry.AfterResponse(func(err error) {
//			fmt.Println(err == nil)
//		})
//	}
func FromContext(ctx context.Context) (*Registry, bool) {
	registry, ok := ctx.Value(contextKey{}).(*Registry)

	return registry, ok
}

// AfterResponse appends callbacks for the response-finalization phase. They
// receive the handler error in reverse registration order, including when the
// router is unwinding a panic. Registration retains the functions and captures;
// it does not execute callbacks or transfer their resources to the registry.
//
// Example:
//
//	registry := hook.New()
//	registry.AfterResponse(func(err error) {
//		fmt.Println(err == nil)
//	})
//	registry.RunAfterResponse(nil)
func (registry *Registry) AfterResponse(callbacks ...func(error)) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	registry.afterResponseHooks = append(registry.afterResponseHooks, callbacks...)
}

// BeforeWrite appends callbacks for each body write. They receive the native
// writer and the borrowed bytes about to be sent, in reverse registration order.
// Copy bytes before retaining them beyond the callback; registration retains
// the callback functions and does not execute them.
//
// Example:
//
//	registry := hook.New()
//	registry.BeforeWrite(func(writer http.ResponseWriter, body []byte) {
//		fmt.Println(len(body))
//	})
//	registry.RunBeforeWrite(httptest.NewRecorder(), []byte("hello"))
func (registry *Registry) BeforeWrite(callbacks ...func(http.ResponseWriter, []byte)) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	registry.beforeWriteHooks = append(registry.beforeWriteHooks, callbacks...)
}

// BeforeWriteHeader appends callbacks that may edit headers before the final
// response status is written. ResponseWriter skips this phase for informational
// statuses other than 101. Registration retains the functions; invocation uses
// reverse registration order and does not hold the registry mutex.
//
// Example:
//
//	registry := hook.New()
//	registry.BeforeWriteHeader(func(writer http.ResponseWriter, status int) {
//		writer.Header().Set("X-Service", "example")
//	})
//	writer := hook.NewResponseWriter(httptest.NewRecorder(), registry)
//	writer.WriteHeader(http.StatusNoContent)
func (registry *Registry) BeforeWriteHeader(callbacks ...func(http.ResponseWriter, int)) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	registry.beforeWriteHeaderHooks = append(registry.beforeWriteHeaderHooks, callbacks...)
}

// RunAfterResponse invokes a snapshot in last-in, first-out order with err.
// It does not clear registrations or recover callback panics. The request owner
// calls it once during finalization; registrations made by callbacks wait for a
// subsequent invocation rather than changing the current snapshot.
//
// Example:
//
//	registry := hook.New()
//	registry.AfterResponse(func(err error) {
//		fmt.Println(err == nil)
//	})
//	registry.RunAfterResponse(nil)
func (registry *Registry) RunAfterResponse(err error) {
	for _, callback := range registry.afterResponseCallbacks() {
		callback(err)
	}
}

// RunBeforeWrite invokes a last-in, first-out callback snapshot for one body
// write. It borrows content and does not write it, clear registrations, or
// recover panics; ResponseWriter performs the actual write after this phase.
//
// Example:
//
//	registry := hook.New()
//	registry.BeforeWrite(func(writer http.ResponseWriter, body []byte) {
//		fmt.Println(len(body))
//	})
//	registry.RunBeforeWrite(httptest.NewRecorder(), []byte("hello"))
func (registry *Registry) RunBeforeWrite(writer http.ResponseWriter, content []byte) {
	for _, callback := range registry.beforeWriteCallbacks() {
		callback(writer, content)
	}
}

// RunBeforeWriteHeader invokes a last-in, first-out callback snapshot with the
// native writer and final status. It neither validates nor writes the status;
// ResponseWriter owns those operations. Callback panics propagate to the caller.
//
// Example:
//
//	registry := hook.New()
//	registry.BeforeWriteHeader(func(writer http.ResponseWriter, status int) {
//		writer.Header().Set("X-Service", "example")
//	})
//	registry.RunBeforeWriteHeader(httptest.NewRecorder(), http.StatusNoContent)
func (registry *Registry) RunBeforeWriteHeader(writer http.ResponseWriter, status int) {
	for _, callback := range registry.beforeWriteHeaderCallbacks() {
		callback(writer, status)
	}
}

// afterResponseCallbacks returns a LIFO snapshot of callbacks.
func (registry *Registry) afterResponseCallbacks() []func(error) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	callbacks := slices.Clone(registry.afterResponseHooks)
	slices.Reverse(callbacks)

	return callbacks
}

// beforeWriteCallbacks returns a LIFO snapshot of callbacks.
func (registry *Registry) beforeWriteCallbacks() []func(http.ResponseWriter, []byte) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	callbacks := slices.Clone(registry.beforeWriteHooks)
	slices.Reverse(callbacks)

	return callbacks
}

// beforeWriteHeaderCallbacks returns a LIFO snapshot of callbacks.
func (registry *Registry) beforeWriteHeaderCallbacks() []func(http.ResponseWriter, int) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	callbacks := slices.Clone(registry.beforeWriteHeaderHooks)
	slices.Reverse(callbacks)

	return callbacks
}
