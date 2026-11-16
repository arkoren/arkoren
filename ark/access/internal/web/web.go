// Package web holds the request helpers shared by the handlers of the packages
// of arkoren.dev/ark/access. It is internal so the public packages never import
// each other.
package web

import (
	"context"
	"mime"
	"net/http"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ErrEmailUnverified is the 403 problem a handler answers to a user whose email
// is not verified. account.ErrEmailUnverified is this value, so every package
// of the module answers with the same stable type.
var ErrEmailUnverified = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:ark/access:account:email-unverified")
	details.Title("Email not verified")
	details.Detail("Verify your email address to continue.")
	details.Status(http.StatusForbidden)
})

// Verified returns [ErrEmailUnverified] when verified, the Verified setting of
// a package, says subject's email is not verified, and passes its errors
// through. A nil verified admits everyone: the package then has no way to
// know, which the facade only allows for the application's own users.
func Verified(ctx context.Context, verified func(context.Context, string) (bool, error), subject string) error {
	if verified == nil {
		return nil
	}
	ok, err := verified(ctx, subject)
	if err != nil {
		return err
	}
	if !ok {
		return ErrEmailUnverified
	}

	return nil
}

// JSON returns next for requests that declare the media type application/json
// and [request.ErrUnsupportedMediaType] (415) for POST, PUT and PATCH requests
// that declare anything else or nothing. It checks the header, not the body, so
// it also covers routes that read no body (logout, a password reset's
// confirmation): a cross-site HTML form can only send a form media type, and so
// cannot reach any state-changing route of this module. Parameters such as
// charset are allowed; the match is exact on the media type, never a prefix.
// DELETE and safe methods pass: a form cannot send DELETE.
//
// It is only a guard against cross-site forms. It does not authenticate a
// request and is no replacement for CSRF protection.
func JSON(next router.Handler) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				return request.ErrUnsupportedMediaType
			}
		}

		return next(w, r)
	}
}

// MaxBody is the largest JSON body a handler of this module reads, in bytes.
// Every request of the module carries a handful of short strings.
const MaxBody = 64 << 10

// Decode reads the JSON body of r into a T, limited to [MaxBody] bytes. A
// malformed or oversized body returns the request package's problem.
func Decode[T any](r *http.Request) (T, error) {
	return request.JSON[T](r, request.MaxSize(MaxBody))
}

// Session returns the identity of r when it comes from a browser session, the
// only identity allowed to change credentials. An anonymous request, or one
// authenticated by a bearer token, returns [auth.ErrUnauthenticated]: a stolen
// token must never be able to enroll or change a credential.
func Session(r *http.Request) (auth.Identity, error) {
	identity, err := request.Identity(r)
	if err != nil {
		return auth.Identity{}, err
	}
	if !identity.Session() {
		return auth.Identity{}, auth.ErrUnauthenticated
	}

	return identity, nil
}

// Fresh returns the session identity of r after guard confirms the user proved
// themselves recently enough ([auth.Guard.Fresh]). A user without a step-up
// method is fresh right after any login, so a first enrollment needs nothing
// special; otherwise the error is [auth.ErrStepUpRequired] with the methods
// the client can confirm with, or [auth.ErrNoStepUpMethod].
func Fresh(guard *auth.Guard, r *http.Request) (auth.Identity, error) {
	identity, err := Session(r)
	if err != nil {
		return auth.Identity{}, err
	}
	if err := guard.Fresh(r); err != nil {
		return auth.Identity{}, err
	}

	return identity, nil
}
