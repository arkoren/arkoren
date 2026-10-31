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
//		return User{}, auth.ErrNoCredentials
//	}
var ErrNoCredentials = errors.New("auth: no credentials")

// ErrUnauthenticated is the problem returned when a request has no
// authenticated user or presents invalid credentials.
//
// Example:
//
//	if _, err := request.User[User](r); err != nil {
//		return err // renders as 401
//	}
var ErrUnauthenticated = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/auth:unauthenticated")
	details.Title("Unauthenticated")
	details.Detail("Authentication is required to access this resource.")
	details.Status(http.StatusUnauthorized)
})

// Authenticator resolves the user of a request. It returns
// [ErrNoCredentials] when the request has no credentials it handles, which
// lets the next authenticator try. Any other error stops the chain.
//
// Only return a user after verifying credentials. A client-supplied identity
// header alone is not proof of identity.
//
// The example uses an existing router r and your application's verifyToken
// function, with signature func(context.Context, string) (User, error). The
// verifier must validate the token before returning a user and return an error
// other than [ErrNoCredentials] for an invalid, expired, or revoked token.
// [Bearer] parses the header and delegates verification to that function.
//
// Example:
//
//	authenticate := auth.Bearer(verifyToken)
//	r.Use(auth.Middleware(authenticate), auth.Require[User]())
type Authenticator[U any] func(r *http.Request) (U, error)

// contextKey is the context key under which a user of type U is stored.
type contextKey[U any] struct{}

// NewContext returns a context carrying user. [Middleware] does this for
// requests; call it directly in tests.
//
// Example:
//
//	ctx := auth.NewContext(context.Background(), User{ID: 1})
//	user, ok := auth.FromContext[User](ctx)
func NewContext[U any](ctx context.Context, user U) context.Context {
	return context.WithValue(ctx, contextKey[U]{}, user)
}

// FromContext returns the user of type U stored in ctx. The boolean reports
// whether one was found.
//
// Example:
//
//	if user, ok := auth.FromContext[User](r.Context()); ok {
//		_ = user.ID
//	}
func FromContext[U any](ctx context.Context) (U, bool) {
	user, ok := ctx.Value(contextKey[U]{}).(U)

	return user, ok
}

// Middleware tries each authenticator in order and stores the first user
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
//		auth.NewSessionIdentity(loadUser).Authenticator(),
//		auth.Bearer(verifyKey),
//	))
func Middleware[U any](authenticators ...Authenticator[U]) router.Middleware {
	authenticators = slices.Clone(authenticators)

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			for _, authenticate := range authenticators {
				user, err := authenticate(r)

				if errors.Is(err, ErrNoCredentials) {
					continue
				}

				if err != nil {
					return err
				}

				return next(w, r.WithContext(NewContext(r.Context(), user)))
			}

			return next(w, r)
		}
	}
}

// Builder configures Require[User] before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(auth.Require[User](func(authenticator *auth.Builder) {
//		authenticator.ErrorResponse(problem.Unauthorized("Bearer"))
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
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
//	r.Use(auth.Require[User](func(authenticator *auth.Builder) {
//		authenticator.ErrorResponse(problem.Forbidden())
//	}))
func (authenticator *Builder) ErrorResponse(response problem.Details) *Builder {
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
//	r.Use(auth.Require[User](func(authenticator *auth.Builder) {
//		authenticator.Events(event.New())
//	}))
func (authenticator *Builder) Events(bus *event.Bus) *Builder {
	authenticator.settings.Events = bus

	return authenticator
}

// Require returns a middleware that rejects requests without a user of type
// U in the context and, with [Builder.Events], publishes [Rejected]. The default
// is 401 with a Bearer challenge; [Builder.ErrorResponse] can replace both the
// problem and its headers. A zero ErrorResponse restores the default.
// A user stored under a different type counts as absent, so the request fails
// closed. Rejection events omit paths and causes; the HTTP problem's normal
// instance rendering and request context values are unchanged.
//
// Example:
//
//	api := r.With(auth.Require[User]())
func Require[U any](configure ...BuilderFunc) router.Middleware {
	config := newSettings(configure...)

	response := config.ErrorResponse

	if reflect.ValueOf(response).IsZero() {
		response = ErrUnauthenticated.Derive(func(details *problem.Builder) {
			details.Header("WWW-Authenticate", "Bearer")
		})
	}

	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			if _, ok := FromContext[U](r.Context()); !ok {
				config.Events.Emit(r.Context(), Rejected, RejectedEvent{
					Method: r.Method,
				})

				return response
			}

			return next(w, r)
		}
	}
}
