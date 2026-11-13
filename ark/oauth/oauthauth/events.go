package oauthauth

import (
	"errors"
	"net/http"

	"arkoren.dev/core/event"
)

// LoginSucceededEvent reports the external identity after local session login.
// It contains no email or token. Subject and issuer can still identify a person;
// apply the application's privacy policy before retaining them in observations.
//
// Example:
//
//	_, err := bus.Listen(oauth.LoginSucceeded, func(_ context.Context, outcome oauth.LoginSucceededEvent) error {
//		_, _ = outcome.Issuer, outcome.Subject // Identify the external account.
//		return nil
//	})
//	if err != nil {
//		return err
//	}
type LoginSucceededEvent struct {
	// Subject is the provider's identifier of the user.
	// Together with Issuer it names the external account, not a local user ID.
	//
	// Example:
	//
	//	outcome := oauth.LoginSucceededEvent{Issuer: "https://id.example.com", Subject: "user-42"}
	//	_, _ = outcome.Issuer, outcome.Subject
	Subject string

	// Issuer identifies the provider.
	// Use it with Subject to distinguish accounts from different issuers.
	//
	// Example:
	//
	//	outcome := oauth.LoginSucceededEvent{Issuer: "https://id.example.com", Subject: "user-42"}
	//	_, _ = outcome.Issuer, outcome.Subject
	Issuer string
}

// LoginFailedEvent describes a rejected callback without retaining request text
// or native/application causes. Err is a bounded classification, not the error
// returned by Callback. Inspect that returned error explicitly for diagnostics;
// its causes can contain credentials or provider-controlled content.
//
// Example:
//
//	_, err := bus.Listen(oauth.LoginFailed, func(_ context.Context, outcome oauth.LoginFailedEvent) error {
//		if errors.Is(outcome.Err, oauth.ErrInvalidCallback) {
//			// Count a rejected login; do not retry the consumed code.
//		}
//		return nil
//	})
//	if err != nil {
//		return err
//	}
type LoginFailedEvent struct {
	// ProviderError is a whitelisted RFC 6749 callback error hint, such as
	// "access_denied". Unknown or absent values become empty. This untrusted
	// hint is not proof of the provider's diagnosis and never contains free text.
	//
	// Example:
	//
	//	outcome := oauth.LoginFailedEvent{ProviderError: "access_denied", Err: oauth.ErrInvalidCallback}
	//	_ = outcome.ProviderError // Bounded hint, not an authenticated diagnosis.
	ProviderError string

	// Err is a bounded classification: ErrInvalidCallback,
	// ErrProviderUnavailable, or an opaque local-login failure. It contains
	// no application/native cause. Callback's returned error retains that cause.
	// The opaque local-login classification has no exported sentinel or cause;
	// do not classify it by comparing its diagnostic text.
	//
	// Example:
	//
	//	outcome := oauth.LoginFailedEvent{Err: oauth.ErrInvalidCallback}
	//	_ = errors.Is(outcome.Err, oauth.ErrInvalidCallback)
	Err error
}

// TokenRejectedEvent reports a failed bearer check with a fixed opaque error,
// not a native diagnostic. Configuration exits such as ErrNoAudiences and
// ErrUnsupported publish no event. The original Verify error is returned only
// to its caller and can contain provider-controlled claims.
//
// Example:
//
//	_, err := bus.Listen(oauth.TokenRejected, func(_ context.Context, outcome oauth.TokenRejectedEvent) error {
//		_ = outcome.Err // Fixed classification; count the outcome, not causes.
//		return nil
//	})
//	if err != nil {
//		return err
//	}
type TokenRejectedEvent struct {
	// Err is an opaque, fixed token-rejection classification with no claims,
	// token or native cause. Verify's returned error retains its original cause.
	// There is no exported rejection sentinel and no cause to unwrap here.
	//
	// Example:
	//
	//	_, err := bus.Listen(oauth.TokenRejected, func(_ context.Context, outcome oauth.TokenRejectedEvent) error {
	//		_ = outcome.Err.Error() // Safe fixed text, not native diagnostics.
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	Err error
}

var (
	// LoginSucceeded is published when [Client.Callback] has stored the user
	// in the session. Persistence still depends on session middleware completing
	// the response. Listener errors cannot change the recorded login outcome.
	//
	// Example:
	//
	//	_, err := bus.Listen(oauth.LoginSucceeded, func(_ context.Context, outcome oauth.LoginSucceededEvent) error {
	//		_ = outcome.Subject
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	LoginSucceeded = event.NewTopic[LoginSucceededEvent]("oauth.login.succeeded")

	// LoginFailed is published when [Client.Callback] rejects a callback: an
	// invalid login cookie, a provider error or missing code, a
	// failed code exchange or token check, or an error from the login
	// function. The payload drops causes and bounds untrusted provider hints;
	// returned errors keep their original causes. Configuration and missing-session
	// exits before cookie consumption publish nothing.
	//
	// Example:
	//
	//	_, err := bus.Listen(oauth.LoginFailed, func(_ context.Context, outcome oauth.LoginFailedEvent) error {
	//		_ = errors.Is(outcome.Err, oauth.ErrProviderUnavailable)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	LoginFailed = event.NewTopic[LoginFailedEvent]("oauth.login.failed")

	// LoggedOut is published when [Client.Logout] has removed the user from
	// the local session. It carries no identity and does not assert provider logout
	// or response-time session persistence.
	//
	// Example:
	//
	//	_, err := bus.Listen(oauth.LoggedOut, func(context.Context, event.Void) error {
	//		return nil // Count local logout without retaining user identifiers.
	//	})
	//	if err != nil {
	//		return err
	//	}
	LoggedOut = event.NewSignal("oauth.logged-out")

	// TokenRejected is published when [Client.Verify] rejects a bearer token.
	// Its error is a fixed opaque classification without claims or native causes;
	// configuration errors before verification publish nothing.
	//
	// Example:
	//
	//	_, err := bus.Listen(oauth.TokenRejected, func(_ context.Context, outcome oauth.TokenRejectedEvent) error {
	//		_ = outcome.Err
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	TokenRejected = event.NewTopic[TokenRejectedEvent]("oauth.token.rejected")
)

// loginFailed publishes a bounded classification and whitelisted callback hint.
// The event retains no cause; this function returns err unchanged so the handler
// caller can still inspect its native or application cause explicitly.
func (client *Client) loginFailed(request *http.Request, err error) error {
	client.config.Events.Emit(request.Context(), LoginFailed, LoginFailedEvent{
		ProviderError: providerErrorCode(param(request, "error")),
		Err:           loginFailureClass(err),
	})

	return err
}

// providerErrorCode bounds untrusted request content to the authorization
// endpoint's standard error vocabulary. It never publishes arbitrary text.
func providerErrorCode(code string) string {
	switch code {
	case "invalid_request", "unauthorized_client", "access_denied", "unsupported_response_type",
		"invalid_scope", "server_error", "temporarily_unavailable":
		return code
	default:
		return ""
	}
}

// errLocalLoginFailed classifies application/session failures without retaining
// their sensitive diagnostics in an event payload. Returned errors stay intact.
var errLocalLoginFailed = errors.New("oauth: local login failed")

// errBearerRejected reports a bounded bearer outcome without retaining native
// verifier diagnostics, which may contain provider-controlled token claims.
var errBearerRejected = errors.New("oauth: bearer token rejected")

// loginFailureClass publishes only safe package templates or the fixed local
// failure class. It intentionally drops causes from observations, not returns.
func loginFailureClass(err error) error {
	switch {
	case errors.Is(err, ErrInvalidCallback):
		return ErrInvalidCallback
	case errors.Is(err, ErrProviderUnavailable):
		return ErrProviderUnavailable
	default:
		return errLocalLoginFailed
	}
}
