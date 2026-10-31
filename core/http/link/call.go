package link

import (
	"fmt"
	"net/http"
	"slices"
)

// MiddlewareBuilderFunc edits request-verification settings once in order on a
// fresh draft. Nil entries panic; empty expansions omit edits. Request-binding
// functions are retained for requests and are not called during construction.
//
// Example:
//
//	var configure link.MiddlewareBuilderFunc = func(policy *link.MiddlewareBuilder) {
//		policy.BindRequest(loadBinding)
//	}
//	r.With(link.Middleware(signer, configure)).Get("/reset", showResetForm)
type MiddlewareBuilderFunc = func(*MiddlewareBuilder)

// callConfig holds the per-call settings built from [CallOption] values.
type callConfig struct {
	// binding is the value covered by the signature when bound is set.
	binding []byte

	// bound reports that [Bind] was applied. It separates an empty binding
	// from no binding, so a missing value never matches a bound link.
	bound bool
}

// CallOption changes one call to a [Signer] method, rather than the published
// signer. Options apply in order; nil entries panic. Use the same [Bind] policy
// when signing and verifying a bound link.
//
// Example:
//
//	options := []link.CallOption{link.Bind(user.PasswordHash)}
//	if err := signer.Verify(rawURL, options...); err != nil {
//		return err
//	}
type CallOption func(*callConfig)

// newCall applies opts in order.
func newCall(opts []CallOption) callConfig {
	var call callConfig

	for _, opt := range opts {
		opt(&call)
	}

	return call
}

// Bind covers value with the signature, so the link verifies only when the
// verifier supplies the same value again. Use it for server-side state that
// should revoke links when it changes, such as a password hash or an
// updated-at timestamp: every link issued before the change stops verifying
// without any stored list of revoked links.
//
// Pass the same Bind to the signing call and to every [Signer.Verify],
// [Signer.Consume], and [Signer.Inspect] call for the link. A wrong or missing
// binding, and a binding on a link signed without one, returns
// [ErrInvalidSignature], which reveals nothing about which part failed. An
// empty value is a binding like any other, distinct from no binding. Links
// signed without Bind keep verifying without it.
//
// The value is copied and only its SHA-256 digest enters the signed text, so
// the link never discloses it. Use [MiddlewareBuilder.BindRequest] with [Middleware].
//
// Example:
//
//	signed, err := signer.Temporary("/reset?user=42", time.Hour, link.Bind(user.PasswordHash))
//	if err != nil {
//		return err
//	}
//	if err := signer.Verify(signed, link.Bind(user.PasswordHash)); err != nil {
//		return err
//	}
func Bind(value []byte) CallOption {
	value = slices.Clone(value)

	return func(call *callConfig) {
		call.binding = value
		call.bound = true
	}
}

// middlewareConfig holds completed middleware settings independently of drafts.
type middlewareConfig struct {
	// bind derives a request's binding; nil verifies without one.
	bind func(*http.Request) ([]byte, error)
}

// MiddlewareBuilder configures request binding before middleware publication.
// Callbacks run once, synchronously in order on one fresh draft; nil entries
// panic. Retained drafts cannot change the middleware. Binding callbacks are
// stored, not executed during construction.
//
// Example:
//
//	r := router.New()
//	r.Use(link.Middleware(signer, func(policy *link.MiddlewareBuilder) {
//		policy.BindRequest(nil)
//	}))
type MiddlewareBuilder struct {
	// settings is the mutable construction draft; bind captures remain shared.
	settings middlewareConfig
}

// BindRequest makes [Middleware] verify links signed with [Bind], taking the
// binding from derive for each request. Typically derive reads the account
// the link names and returns its current password hash or update time.
//
// derive runs before the signature is checked, so everything it reads from
// the request is untrusted: look the record up by a value from the URL, but do
// not treat that value as proof of anything. An error from derive rejects the
// link with [ErrLinkRejected], as does a missing record. The returned problem
// retains the cause; the [Rejected] event and response body do not contain it.
// Applications must redact the cause before logging it. Every request
// is verified with a binding, so BindRequest cannot accept links signed
// without [Bind].
// A nil derive function disables request binding and verifies unbound links.
// The function and its captures remain shared and run only at request time.
//
// Example:
//
//	r := router.New()
//	r.Use(link.Middleware(signer, func(policy *link.MiddlewareBuilder) {
//		policy.BindRequest(func(r *http.Request) ([]byte, error) {
//			user, err := users.Find(r.Context(), r.URL.Query().Get("user"))
//			if err != nil {
//				return nil, err
//			}
//
//			return user.PasswordHash, nil
//		})
//	}))
func (policy *MiddlewareBuilder) BindRequest(derive func(*http.Request) ([]byte, error)) *MiddlewareBuilder {
	policy.settings.bind = derive

	return policy
}

// bindRequest returns the call settings for request using derive.
func bindRequest(derive func(*http.Request) ([]byte, error), request *http.Request) (callConfig, error) {
	value, err := derive(request)

	if err != nil {
		return callConfig{}, fmt.Errorf("derive link binding: %w", err)
	}

	return newCall([]CallOption{Bind(value)}), nil
}
