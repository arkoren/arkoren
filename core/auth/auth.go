package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ErrNoCredentials is returned by an [Authenticator] when the request carries
// nothing it understands, so the next authenticator may try.
// Invalid credentials must return a different error to stop fallback.
//
// Example:
//
//	if r.Header.Get("Authorization") == "" {
//		return auth.Identity{}, auth.ErrNoCredentials
//	}
var ErrNoCredentials = errors.New("auth: no credentials")

// ErrUnauthenticated is the problem returned when a request has no
// authenticated identity or presents invalid credentials.
//
// Example:
//
//	if _, err := request.Identity(r); err != nil {
//		return err // renders as 401
//	}
var ErrUnauthenticated = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/auth:unauthenticated")
	details.Title("Unauthenticated")
	details.Detail("Authentication is required to access this resource.")
	details.Status(http.StatusUnauthorized)
})

// Authenticator resolves who is acting on a request. It returns
// [ErrNoCredentials] when the request has no credentials it handles, which
// lets the next authenticator try. Any other error stops the chain.
//
// Only return an [Identity] after verifying credentials. A client-supplied
// identity header alone is not proof of identity.
//
// The example uses an existing router r and your application's verifyToken
// function, with signature func(context.Context, string) (auth.Identity, error).
// The verifier must validate the token before returning an identity and return
// an error other than [ErrNoCredentials] for an invalid, expired, or revoked
// token. [Bearer] parses the header and delegates verification to that
// function.
//
// Example:
//
//	authenticate := auth.Bearer(verifyToken)
//	r.Use(auth.Middleware(authenticate), auth.Require())
type Authenticator func(r *http.Request) (Identity, error)

// contextKey is the context key under which the [Identity] is stored.
type contextKey struct{}

// NewContext returns a context carrying identity. [Middleware] does this for
// requests; call it directly in tests.
//
// Example:
//
//	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42"))
//	identity, ok := auth.FromContext(ctx)
func NewContext(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, identity)
}

// FromContext returns the [Identity] stored in ctx. The boolean reports
// whether one was found. It needs no knowledge of the application's user type.
//
// Example:
//
//	if identity, ok := auth.FromContext(r.Context()); ok {
//		_ = identity.Subject()
//	}
func FromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(contextKey{}).(Identity)

	return identity, ok
}

// UserFromContext returns the application user of type U that the
// authenticator attached to the identity in ctx, by type-asserting
// [UserRecord.Value] of [Identity.Record]. The boolean is false when there is
// no identity, no record value, or the value has another type, so a mismatch
// fails closed.
//
// Example:
//
//	if user, ok := auth.UserFromContext[User](r.Context()); ok {
//		_ = user.ID
//	}
func UserFromContext[U any](ctx context.Context) (U, bool) {
	identity, ok := FromContext(ctx)

	if !ok {
		var zero U

		return zero, false
	}

	user, ok := identity.record.Value.(U)

	return user, ok
}

// Middleware tries each authenticator in order and stores the first identity
// found in the request context. When all return [ErrNoCredentials] the
// request continues anonymously; use [Require] to reject it. Any other
// error is returned and the handler is not called.
//
// Middleware copies the authenticator slice before returning, so replacing
// caller-owned entries cannot change the chain. Functions and their captured
// state remain shared.
//
// Example:
//
//	r.Use(auth.Middleware(
//		login.Authenticator(),
//		auth.Bearer(verifyKey),
//	))
func Middleware(authenticators ...Authenticator) router.Middleware {
	authenticators = slices.Clone(authenticators)

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			for _, authenticate := range authenticators {
				identity, err := authenticate(r)

				if errors.Is(err, ErrNoCredentials) {
					continue
				}

				if err != nil {
					return err
				}

				return next(w, r.WithContext(NewContext(r.Context(), identity)))
			}

			return next(w, r)
		}
	}
}

// MiddlewareBuilder configures [Require] before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(auth.Require(func(authenticator *auth.MiddlewareBuilder) {
//		authenticator.ErrorResponse(problem.Unauthorized("Bearer"))
//	}))
type MiddlewareBuilder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings middlewareSettings
}

// ErrorResponse is the problem returned for anonymous requests. Zero
// uses [ErrUnauthenticated] with a "WWW-Authenticate: Bearer" challenge.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(auth.Require(func(authenticator *auth.MiddlewareBuilder) {
//		authenticator.ErrorResponse(problem.Forbidden())
//	}))
func (authenticator *MiddlewareBuilder) ErrorResponse(response problem.Details) *MiddlewareBuilder {
	authenticator.settings.ErrorResponse = response

	return authenticator
}

// Events receives [Rejected] when [Require] rejects a request. The
// default, nil, publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(auth.Require(func(authenticator *auth.MiddlewareBuilder) {
//		authenticator.Events(event.New())
//	}))
func (authenticator *MiddlewareBuilder) Events(bus *event.Bus) *MiddlewareBuilder {
	authenticator.settings.Events = bus

	return authenticator
}

// Require returns a middleware that rejects requests without an [Identity] in
// the context and, with [MiddlewareBuilder.Events], publishes [Rejected]. The default
// is 401 with a Bearer challenge; [MiddlewareBuilder.ErrorResponse] can replace both the
// problem and its headers. A zero ErrorResponse restores the default.
// Rejection events omit paths and causes; the HTTP problem's normal
// instance rendering and request context values are unchanged.
//
// Example:
//
//	api := r.With(auth.Require())
func Require(configure ...MiddlewareBuilderFunc) router.Middleware {
	return require(newMiddlewareSettings(configure...))
}

// require builds the [Require] middleware from completed settings, so wrappers
// such as [RequireLevel] apply the caller's callbacks once and reuse its
// rejection of anonymous requests.
func require(config middlewareSettings) router.Middleware {
	response := config.ErrorResponse

	if reflect.ValueOf(response).IsZero() {
		response = ErrUnauthenticated.Derive(func(details *problem.Builder) {
			details.Header("WWW-Authenticate", "Bearer")
		})
	}

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			if _, ok := FromContext(r.Context()); !ok {
				config.Events.Emit(r.Context(), Rejected, RejectedEvent{
					Method: r.Method,
				})

				return response
			}

			return next(w, r)
		}
	}
}
