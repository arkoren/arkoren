package router

import (
	"encoding"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"slices"

	"arkoren.dev/core/event"
)

// RecoverBuilderFunc configures Recover synchronously during construction.
// Callbacks share one draft and run once in order; nil entries panic. Retaining
// the draft cannot change published settings, but function captures stay shared.
//
// Example:
//
//	var configure router.RecoverBuilderFunc = func(recovery *router.RecoverBuilder) {
//		recovery.Handler(nil) // select the default conversion
//	}
//	routes := router.New()
//	routes.Use(router.Recover(configure))
type RecoverBuilderFunc = func(*RecoverBuilder)

// recoverReadLimit bounds bytes read from a panic value that implements [io.Reader].
const recoverReadLimit = 1 << 20 // 1 MB

// ErrRecoverUnexpected identifies a panic converted by the default Recover
// handler, or a nil custom conversion result. The default conversion preserves
// the original cause separately; match this sentinel with errors.Is rather than
// comparing diagnostic text. The HTTP renderer does not expose plain error text.
//
// Example:
//
//	err := fmt.Errorf("request failed: %w", router.ErrRecoverUnexpected)
//	fmt.Println(errors.Is(err, router.ErrRecoverUnexpected)) // true
var ErrRecoverUnexpected = errors.New("an unexpected error occurred")

// ErrFailedRecovering identifies a failure reading or marshaling a panic value
// during default conversion. The conversion failure remains in the error chain
// for server-side diagnosis; custom handlers define their own error identities.
//
// Example:
//
//	err := errors.Join(router.ErrFailedRecovering, io.ErrUnexpectedEOF)
//	fmt.Println(errors.Is(err, router.ErrFailedRecovering)) // true
var ErrFailedRecovering = errors.New(
	"failed recovering from unexpected error",
)

// recoverError preserves a recovered error and its server-side stack trace.
// Each panic publishes a distinct pointer so response finalization can identify
// the failure that was rendered, rather than inherit an older cause's state.
type recoverError struct {
	// err preserves the recovered failure for message rendering and error-chain
	// matching.
	err error
	// stack captures the recovery stack for server-side diagnostics, not the HTTP
	// response.
	stack []byte
}

// Error returns the converted panic's diagnostic message. It can contain the
// original panic text and is intended for restricted server-side diagnostics,
// not client responses. Recover always supplies a non-nil converted error.
//
// Example:
//
//	handler := router.Recover()(func(http.ResponseWriter, *http.Request) error {
//		panic("example")
//	})
//	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
//	message := err.Error() // retain only in a restricted diagnostic sink
//	_ = message
func (recovery recoverError) Error() string {
	return recovery.err.Error()
}

// Unwrap returns the converted error without the stack wrapper. This preserves
// errors.Is and errors.As matching, including custom recovery handler results.
// The returned error and any arbitrary values it captures remain shared.
//
// Example:
//
//	handler := router.Recover()(func(http.ResponseWriter, *http.Request) error {
//		panic("example")
//	})
//	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
//	fmt.Println(errors.Is(errors.Unwrap(err), router.ErrRecoverUnexpected)) // true
func (recovery recoverError) Unwrap() error {
	return recovery.err
}

// Stack returns an owned copy of the stack captured during panic recovery.
// It is a server-side diagnostic and can contain sensitive locations or values;
// do not include it in client responses or forward it without a privacy review.
// Modifying the returned bytes does not change the error or its notification.
//
// Example:
//
//	handler := router.Recover()(func(http.ResponseWriter, *http.Request) error {
//		panic("example")
//	})
//	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
//	var diagnostic interface{ Stack() []byte }
//	if errors.As(err, &diagnostic) {
//		stack := diagnostic.Stack() // safe to retain or redact in place
//		_ = stack
//	}
func (recovery recoverError) Stack() []byte {
	return slices.Clone(recovery.stack)
}

// defaultRecoverHandler converts a recovered panic value to an error joined
// with [ErrRecoverUnexpected].
func defaultRecoverHandler(value any) error {
	var cause error

	switch recovered := value.(type) {
	case error:
		cause = recovered
	case string:
		cause = errors.New(recovered)
	case fmt.Stringer:
		cause = errors.New(recovered.String())
	case io.Reader:
		body, err := io.ReadAll(io.LimitReader(recovered, recoverReadLimit))

		if err != nil {
			return errors.Join(ErrFailedRecovering, err)
		}

		cause = errors.New(string(body))
	case encoding.TextMarshaler:
		text, err := recovered.MarshalText()

		if err != nil {
			return errors.Join(ErrFailedRecovering, err)
		}

		cause = errors.New(string(text))
	default:
		cause = fmt.Errorf("%+v", value)
	}

	return errors.Join(ErrRecoverUnexpected, cause)
}

// RecoverBuilder configures Recover before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(router.Recover(func(recovery *router.RecoverBuilder) {
//		recovery.Handler(nil)
//	}))
type RecoverBuilder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings recoverSettings
}

// Handler converts a recovered panic value into an error. A nil function uses
// the default conversion to [ErrRecoverUnexpected]. If a supplied function
// returns nil, Recover uses ErrRecoverUnexpected without the raw panic value:
// recovery always reports a failure and retains its diagnostic stack.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(router.Recover(func(recovery *router.RecoverBuilder) {
//		recovery.Handler(nil)
//	}))
func (recovery *RecoverBuilder) Handler(handler func(value any) error) *RecoverBuilder {
	recovery.settings.Handler = handler

	return recovery
}

// Events receives [PanicRecovered]. Nil, the default, publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(router.Recover(func(recovery *router.RecoverBuilder) {
//		recovery.Events(event.New())
//	}))
func (recovery *RecoverBuilder) Events(bus *event.Bus) *RecoverBuilder {
	recovery.settings.Events = bus

	return recovery
}

// Recover returns middleware that converts panics into
// [ErrRecoverUnexpected], or into the error from [RecoverBuilder.Handler].
// A nil conversion result falls back to ErrRecoverUnexpected, not success.
// [http.ErrAbortHandler] is re-raised so net/http can abort the connection.
// With [RecoverBuilder.Events] every converted panic publishes [PanicRecovered].
//
// Example:
//
//	routes := router.New()
//	routes.Use(router.Recover()) // before handlers that can panic
func Recover(configure ...RecoverBuilderFunc) Middleware {
	config := newRecoverSettings(configure...)
	handler := config.Handler

	if handler == nil {
		handler = defaultRecoverHandler
	}

	return func(next Handler) Handler {
		return func(
			w http.ResponseWriter,
			r *http.Request,
		) (err error) {
			defer func() {
				e := recover()

				if e == nil {
					return
				}

				if err, ok := e.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(e)
				}

				stack := debug.Stack()
				cause := handler(e)

				if cause == nil {
					cause = ErrRecoverUnexpected
				}

				err = &recoverError{err: cause, stack: stack}

				config.Events.Emit(r.Context(), PanicRecovered, PanicRecoveredEvent{
					Method:  r.Method,
					Pattern: r.Pattern,
					Err:     err,
					Stack:   slices.Clone(stack),
				})
			}()

			return next(w, r)
		}
	}
}
