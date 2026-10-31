package oauthauth

import (
	"errors"
	"net/http"

	"arkoren.dev/foundation/problem"
)

// ErrInvalidLoginRequest is returned when a login request contains an unsafe
// or malformed local redirect target.
//
// Example:
//
//	if errors.Is(err, oauth.ErrInvalidLoginRequest) {
//		// The return_to value was rejected.
//	}
var ErrInvalidLoginRequest = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/oauth:oauthauth:invalid-login-request")
	details.Title("Invalid login request")
	details.Detail("The login request could not be processed.")
	details.Status(http.StatusBadRequest)
})

// ErrInvalidCallback is returned when a callback cannot be verified: the
// login cookie is missing, expired or tampered with, an ID-token check fails,
// the provider reports an error, or the code exchange returns invalid_grant
// (including a consumed code). Cookie deletion is not a server-side replay cache.
// Returned errors preserve causes; LoginFailed payloads contain only this
// template when classified as an invalid callback, not those causes.
//
// Example:
//
//	if errors.Is(err, oauth.ErrInvalidCallback) {
//		// Ask the user to start the login again.
//	}
var ErrInvalidCallback = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/oauth:oauthauth:invalid-callback")
	details.Title("Invalid authentication callback")
	details.Detail("The authentication callback could not be verified.")
	details.Status(http.StatusBadRequest)
})

// ErrProviderUnavailable is returned when the provider cannot complete an
// authentication operation.
//
// Example:
//
//	if errors.Is(err, oauth.ErrProviderUnavailable) {
//		// Retry later.
//	}
var ErrProviderUnavailable = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/oauth:oauthauth:provider-unavailable")
	details.Title("Authentication provider unavailable")
	details.Detail("The authentication provider could not complete the request.")
	details.Status(http.StatusBadGateway)
})

// ErrNotStarted is returned when the client is used before [Client.Start]
// has discovered the OIDC provider.
//
// Example:
//
//	if errors.Is(err, oauth.ErrNotStarted) {
//		// Start has not succeeded yet.
//	}
var ErrNotStarted = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/oauth:oauthauth:not-started")
	details.Title("Authentication not ready")
	details.Detail("The authentication client has not started.")
	details.Status(http.StatusServiceUnavailable)
})

// ErrUnsupported is returned when an operation needs OIDC but the provider is
// plain OAuth2, such as [Client.Verify].
//
// Example:
//
//	if errors.Is(err, oauth.ErrUnsupported) {
//		// Use an OIDC provider for bearer tokens.
//	}
var ErrUnsupported = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:driver/oauth:oauthauth:unsupported")
	details.Title("Operation not supported")
	details.Detail("The authentication provider does not support this operation.")
	details.Status(http.StatusNotImplemented)
})

// ErrNoAudiences is returned by [Client.Verify] when the client has no
// accepted audiences. Configure them with [ClientBuilder.Audiences]: the client ID is
// deliberately not a default, because it is the audience of ID tokens.
//
// Example:
//
//	if errors.Is(err, oauth.ErrNoAudiences) {
//		// Set client.Audiences("https://api.example.com") in its construction callback.
//	}
var ErrNoAudiences = errors.New("oauth: Verify requires ClientBuilder.Audiences")

// invalidCallback adds callback failure details to the standard problem.
func invalidCallback(err error) problem.Details {
	details := ErrInvalidCallback.Derive(func(details *problem.Builder) {
		details.Extension("reason", "invalid_callback")
	})

	if err == nil {
		return details
	}

	return details.Derive(func(details *problem.Builder) {
		details.Error(err)
	})
}

// invalidLoginRequest adds login-request failure details to the standard problem.
func invalidLoginRequest(err error) problem.Details {
	return ErrInvalidLoginRequest.Derive(func(details *problem.Builder) {
		details.Extension("reason", "invalid_login_request")
		details.Error(err)
	})
}

// providerUnavailable adds the provider error to the standard problem.
func providerUnavailable(err error) problem.Details {
	return ErrProviderUnavailable.Derive(func(details *problem.Builder) {
		details.Error(err)
	})
}
