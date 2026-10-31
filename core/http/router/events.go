package router

import "arkoren.dev/core/event"

// PanicRecoveredEvent is the diagnostic payload of [PanicRecovered]. It is
// emitted synchronously only when Recover has an event bus. Err and Stack can
// contain sensitive panic diagnostics; applications must restrict and sanitize
// their listener output rather than forward the entire payload to clients.
// The event's stack bytes are detached from the error's diagnostic stack.
//
// Example:
//
//	bus := event.New()
//	_, err := bus.Listen(router.PanicRecovered, func(ctx context.Context, notice router.PanicRecoveredEvent) error {
//		slog.ErrorContext(ctx, "request panicked", "method", notice.Method, "pattern", notice.Pattern)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
type PanicRecoveredEvent struct {
	// Method is the request method, not an observed response status or a
	// guarantee that the client received a replacement problem response.
	//
	// Example:
	//
	//	notice := router.PanicRecoveredEvent{Method: http.MethodPost}
	//	fmt.Println(notice.Method) // POST
	Method string

	// Pattern is the matched route pattern, such as "GET /users/{id}";
	// empty when no route matched. It describes registration, not the actual
	// request path, query, or path parameter values.
	//
	// Example:
	//
	//	notice := router.PanicRecoveredEvent{Pattern: "GET /users/{id}"}
	//	fmt.Println(notice.Pattern) // GET /users/{id}
	Pattern string

	// Err is the error the panic was converted into, as [Recover] returns it.
	// Its cause and text can contain sensitive panic values. Use errors.Is for
	// classification and restrict any diagnostic output to approved sinks.
	//
	// Example:
	//
	//	notice := router.PanicRecoveredEvent{Err: router.ErrRecoverUnexpected}
	//	fmt.Println(errors.Is(notice.Err, router.ErrRecoverUnexpected)) // true
	Err error

	// Stack is the goroutine stack captured when the panic was recovered. It
	// does not alias the returned error's stack; listeners sharing this event
	// must copy it before mutation to avoid changing what later listeners see.
	// It is a sensitive diagnostic, not a client-safe notification body.
	//
	// Example:
	//
	//	notice := router.PanicRecoveredEvent{Stack: []byte("redacted diagnostic")}
	//	retained := bytes.Clone(notice.Stack)
	//	_ = retained
	Stack []byte
}

// PanicRecovered is published when [Recover] converts a panic into an error.
// [net/http.ErrAbortHandler] is re-raised and publishes nothing.
// It requires an explicitly supplied event bus; listener failures do not change
// the recovered error. Treat Err and Stack as sensitive server-side diagnostics.
//
// Example:
//
//	bus := event.New()
//	_, err := bus.Listen(router.PanicRecovered, func(ctx context.Context, notice router.PanicRecoveredEvent) error {
//		slog.ErrorContext(ctx, "request panicked", "method", notice.Method, "pattern", notice.Pattern)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	routes := router.New()
//	routes.Use(router.Recover(func(recovery *router.RecoverBuilder) {
//		recovery.Events(bus)
//	}))
var PanicRecovered = event.NewTopic[PanicRecoveredEvent]("http.panic.recovered")
