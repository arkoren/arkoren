package csrf

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ErrBlocked is the default error returned when a CSRF attack is detected.
// It contains a structured problem response with appropriate HTTP status and details
// that can be safely returned to clients without exposing security implementation details.
//
// Example:
//
//	failure := csrf.ErrBlocked.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrBlocked = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/middleware/csrf:blocked")
	details.Title("Cross-Origin Request Blocked")
	details.Detail("The request was rejected because its origin or fetch context did not meet security requirements.")
	details.Status(http.StatusForbidden)
})

// settings configures CSRF protection for trusted cross-origin requests.
type settings struct {
	// TrustedOrigins is the list of trusted origins allowed to make
	// cross-origin requests. They are added to Protection when both
	// are set.
	TrustedOrigins []string

	// Protection is the engine that checks requests. Zero builds a new
	// [http.CrossOriginProtection]. A provided value is mutated to add
	// TrustedOrigins.
	Protection *http.CrossOriginProtection

	// ErrorResponse is the problem returned for a blocked request. Zero
	// uses [ErrBlocked].
	ErrorResponse problem.Details

	// Events receives [Blocked]. Nil, the default, publishes nothing.
	Events *event.Bus
}

// Builder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(csrf.Middleware(func(protection *csrf.Builder) {
//		protection.TrustedOrigins("https://app.example.com")
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// TrustedOrigins is the list of trusted origins allowed to make
// cross-origin requests. They are added to Protection when both
// are set.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(csrf.Middleware(func(protection *csrf.Builder) {
//		protection.TrustedOrigins("https://app.example.com")
//	}))
func (protection *Builder) TrustedOrigins(origins ...string) *Builder {
	protection.settings.TrustedOrigins = slices.Clone(origins)

	return protection
}

// Protection is the engine that checks requests. Zero builds a new
// [http.CrossOriginProtection]. A provided value is mutated to add
// TrustedOrigins.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(csrf.Middleware(func(protection *csrf.Builder) {
//		protection.Protection(http.NewCrossOriginProtection())
//	}))
func (protection *Builder) Protection(engine *http.CrossOriginProtection) *Builder {
	protection.settings.Protection = engine

	return protection
}

// ErrorResponse is the problem returned for a blocked request. Zero
// uses [ErrBlocked].
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(csrf.Middleware(func(protection *csrf.Builder) {
//		protection.ErrorResponse(problem.Forbidden())
//	}))
func (protection *Builder) ErrorResponse(response problem.Details) *Builder {
	protection.settings.ErrorResponse = response

	return protection
}

// Events receives [Blocked]. Nil, the default, publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(csrf.Middleware(func(protection *csrf.Builder) {
//		protection.Events(event.New())
//	}))
func (protection *Builder) Events(bus *event.Bus) *Builder {
	protection.settings.Events = bus

	return protection
}

// Middleware returns a middleware that protects against Cross-Site Request
// Forgery attacks using Go's built-in http.CrossOriginProtection. It panics
// when a trusted origin is malformed. Safe methods (GET, HEAD, OPTIONS) are
// never checked, so they must not change state. With [Builder.Events] every
// blocked request publishes [Blocked].
// Requests without Sec-Fetch-Site or Origin headers are allowed; this policy is
// not authentication. No trusted cross-origin exceptions are added by default.
//
// Example:
//
//	routes := router.New()
//	routes.Use(csrf.Middleware())
func Middleware(configure ...BuilderFunc) router.Middleware {
	config := newSettings(configure...)

	protection := config.Protection

	if protection == nil {
		protection = http.NewCrossOriginProtection()
	}

	for _, origin := range config.TrustedOrigins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			panic(fmt.Sprintf("csrf: %v", err))
		}
	}

	response := config.ErrorResponse

	if reflect.ValueOf(response).IsZero() {
		response = ErrBlocked
	}

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			if err := protection.Check(r); err != nil {
				config.Events.Emit(r.Context(), Blocked, BlockedEvent{
					Method: r.Method,
					Path:   r.URL.Path,
					Origin: r.Header.Get("Origin"),
					Err:    err,
				})

				return response.Derive(func(details *problem.Builder) {
					details.Error(err)
				})
			}

			return next(w, r)
		}
	}
}
