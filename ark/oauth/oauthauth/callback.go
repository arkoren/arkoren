package oauthauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/oauth2"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
)

// CallbackHandler completes a browser login. It accepts the provider's GET
// redirect or POST form, finds and deletes the login cookie, exchanges the code
// and verifies the user (an ID token for OIDC, the user info function for
// OAuth2). It then calls resolve to map the verified [auth.External] identity to
// the application's user subject and hands that subject to guard.Begin with a
// federated factor ([MethodFederated], [auth.KindFederated]), which logs the
// user in and regenerates the session ID.
//
// The factor time is this server's clock at the callback, stamped by the guard.
// What the provider says about when the user authenticated (the auth_time claim)
// is ignored, so a silent single sign-on approval and a fresh password prompt
// both count as a login now. Whether that satisfies [auth.Guard.RequireFresh]
// follows the core rule: federated is not a step-up method by default, so a
// user with a step-up method such as a password or TOTP is not fresh after a
// silent federated login. The fresh query parameter of [Client.LoginHandler]
// is only a hint to the provider.
//
// The verified claims reach resolve in External.Claims. The granted scopes are
// added to them as the space-delimited string claim "scope": the token response
// scope or, when the provider omits it, the requested scopes (RFC 6749
// section 5.1). The display name, when the provider has one, is Claims["name"];
// Sign in with Apple's first-authorization name replaces the (absent) claim.
//
// A completed login publishes [LoginSucceeded] and redirects to the validated
// return_to target. When login requires a second factor, Begin parks the login
// as pending and the request stays anonymous: the handler then redirects to the
// path set with [ClientBuilder.ChallengeURL], publishes no [LoginSucceeded] and
// does not carry return_to along; the page there finishes the login through
// the core challenge route ([auth.Guard.ChallengeHandler]). Without a challenge
// URL it ends the pending login with guard.Logout and fails with
// [ErrNoChallengeURL]. It never falls back to a login without the second factor.
//
// A form_post provider such as Apple takes two requests (route both methods to
// the same handler). The provider's cross-site POST carries neither the session
// cookie (SameSite=Lax is withheld from cross-site POSTs) nor, therefore, the
// logged-in user of a connect flow, so the POST only validates the login cookie,
// exchanges the code and verifies the identity. It then keeps the verified
// identity in a sealed, HttpOnly, SameSite=Lax hand-off cookie that lives 30
// seconds, and answers 303 See Other to the callback path with the state. The
// browser follows with a top-level GET navigation, which carries the session
// cookie; that request deletes the hand-off cookie, runs resolve and
// guard.Begin, and continues as described above, so a logged-in user who
// connects Apple is linked instead of getting a second account. The identity
// links to the session present at that GET: the one that started the login
// normally, but whichever session the browser holds then. Connecting needs the
// session cookie to be SameSite=Lax (the default); with Strict it is not sent
// on the cross-site hop and the callback sees an anonymous request. The
// hand-off cookie is named after the state, bound to its name, and only that GET
// reads it; a form_post client never completes a login from a GET that carries
// a code. Like the login cookie it is deleted, not tracked: it is not
// single-use, and a copy of it stays valid until its 30 seconds pass (the check tolerates 5
// more seconds of clock difference between servers). A
// verified identity too large for one cookie (about 3800 bytes) fails the POST
// with an error. resolve runs once, in the GET, and sees the claims after a
// JSON round trip.
//
// With [ClientBuilder.Events] a rejected callback publishes [LoginFailed].
//
// A missing, tampered or expired login cookie, a failed ID-token check, or a
// native invalid_grant code-exchange rejection is reported as ErrInvalidCallback.
// Cookie deletion is browser cleanup, not server-side replay tracking: a copied
// cookie remains valid until expiry, and the provider must enforce one-time codes.
// Other exchange failures wrap ErrProviderUnavailable. Returned errors retain
// native causes, but LoginFailed contains only bounded classifications and hints.
// Resolver errors matching auth.ErrNoCredentials also become
// ErrInvalidCallback; other resolver errors pass through unchanged. A subject
// the user provider does not know, or that is disabled, returns
// auth.ErrUnauthenticated from Begin; an invalid subject returns
// auth.ErrInvalidSubject. A request without a session returns
// auth.ErrUnauthenticated before consuming state. A nil guard or resolve is a
// configuration error returned per request before any side effects. A valid
// handler still requires Start.
//
// resolve is the only code that decides which local account an outside
// identity may log into. Return the subject of the user record, for example
// from a federation table keyed by Issuer and Subject. Linking accounts by email
// is unsafe unless EmailVerified is true and the application's policy allows it.
//
// Example:
//
//	r.Get("/auth/google/callback", client.CallbackHandler(guard, func(ctx context.Context, external auth.External) (string, error) {
//		return links.SubjectFor(ctx, external.Issuer, external.Subject) // create or refuse per policy
//	}))
func (client *Client) CallbackHandler(guard *auth.Guard, resolve auth.Resolver) router.Handler {
	var configurationErr error

	switch {
	case guard == nil:
		configurationErr = errors.New("oauth: nil guard")
	case resolve == nil:
		configurationErr = errors.New("oauth: nil login mapper")
	}

	return func(writer http.ResponseWriter, httpRequest *http.Request) error {
		if configurationErr != nil {
			return configurationErr
		}

		ready, err := client.started()

		if err != nil {
			return err
		}

		// Logins reads only the session: without one there is nothing to log in
		// to, and the state cookie must stay unconsumed. A request with no
		// logged-in user returns ErrUnauthenticated, which is not a failure here.
		if _, err := guard.Logins(httpRequest); errors.Is(err, session.ErrNoSession) {
			return auth.ErrUnauthenticated
		}

		state := param(httpRequest, "state")

		var saved loginState

		var external auth.External

		if client.provider.formPost && httpRequest.Method == http.MethodGet {
			// The top-level GET navigation that follows the POST: the identity was
			// verified there, and this request carries the session cookie.
			saved, err = client.takeHandoff(writer, httpRequest, state)

			if err != nil {
				return client.loginFailed(httpRequest, invalidCallback(err))
			}

			external = *saved.Identity
		} else {
			saved, err = client.take(writer, httpRequest, state)

			if err != nil {
				return client.loginFailed(httpRequest, invalidCallback(err))
			}

			code := param(httpRequest, "code")

			if code == "" || param(httpRequest, "error") != "" {
				return client.loginFailed(httpRequest, invalidCallback(nil))
			}

			external, err = client.callbackIdentity(httpRequest, ready, code, saved)

			if err != nil {
				return client.loginFailed(httpRequest, err)
			}

			if client.provider.formPost {
				if err := client.handOff(writer, saved, external); err != nil {
					return client.loginFailed(httpRequest, err)
				}

				return nil
			}
		}

		subject, err := resolve(httpRequest.Context(), external)

		if err != nil {
			return client.loginFailed(httpRequest, callbackMapperError(err))
		}

		outcome, err := guard.Begin(httpRequest, subject, federatedFactor)

		if errors.Is(err, session.ErrNoSession) {
			return auth.ErrUnauthenticated
		}

		if err != nil {
			return client.loginFailed(httpRequest, err)
		}

		if outcome.Complete() {
			client.config.Events.Emit(httpRequest.Context(), LoginSucceeded, LoginSucceededEvent{
				Subject:      external.Subject,
				Issuer:       external.Issuer,
				LocalSubject: subject,
			})

			return response.SafeRedirect(writer, http.StatusFound, saved.ReturnTo)
		}

		if client.config.ChallengeURL == "" {
			// Begin parked the login as pending; end it so no half login lingers.
			return client.loginFailed(httpRequest, errors.Join(ErrNoChallengeURL, guard.Logout(httpRequest)))
		}

		return response.SafeRedirect(writer, http.StatusFound, client.config.ChallengeURL)
	}
}

// MethodFederated is the [auth.Method] of a login proved by an external
// identity provider (browser callback or native ID token). Core defines only
// the RFC 8176 methods, so this module names its own. It is not a step-up
// method unless the application registers a credential for it.
//
// Example:
//
//	factor := auth.Factor{Method: oauth.MethodFederated, Kind: auth.KindFederated}
//	_ = factor
const MethodFederated auth.Method = "federated"

// federatedFactor is the factor every login of this package proves. The guard
// stamps its time; see [Client.CallbackHandler].
var federatedFactor = auth.Factor{Method: MethodFederated, Kind: auth.KindFederated}

// callbackMapperError preserves the browser callback's credential rejection
// contract, including wrapped causes. Other application failures keep their
// original identity; this conversion never handles configuration or bridge errors.
func callbackMapperError(err error) error {
	if errors.Is(err, auth.ErrNoCredentials) {
		return invalidCallback(err)
	}

	return err
}

// callbackIdentity resolves the verified provider identity, including optional
// POST-only provider data. This keeps protocol completion before local mapping.
func (client *Client) callbackIdentity(request *http.Request, ready *endpoints, code string, saved loginState) (auth.External, error) {
	external, err := client.identity(request.Context(), ready, code, saved)

	if err != nil {
		return auth.External{}, err
	}

	if complete := client.provider.formIdentity; complete != nil && request.Method == http.MethodPost {
		external = complete(request, external)
	}

	return external, nil
}

// param reads a callback parameter from the query of a GET or the form body
// of a POST, never both.
func param(httpRequest *http.Request, key string) string {
	if httpRequest.Method == http.MethodPost {
		return httpRequest.PostFormValue(key)
	}

	return httpRequest.URL.Query().Get(key)
}

// identity exchanges code and returns the verified identity of the login.
func (client *Client) identity(ctx context.Context, ready *endpoints, code string, saved loginState) (auth.External, error) {
	config := ready.oauth

	if secret := client.provider.clientSecret; secret != nil {
		signed, err := secret(config.ClientID, config.ClientSecret)

		if err != nil {
			return auth.External{}, fmt.Errorf("derive client secret: %w", err)
		}

		config.ClientSecret = signed
	}

	var options []oauth2.AuthCodeOption

	if saved.Verifier != "" {
		options = append(options, oauth2.VerifierOption(saved.Verifier))
	}

	token, err := config.Exchange(ctx, code, options...)

	if err != nil {
		return auth.External{}, exchangeFailure(err)
	}

	var identity auth.External

	if client.provider.isOIDC() {
		identity, err = client.idTokenIdentity(ctx, token, saved.Nonce)
	} else {
		identity, err = client.provider.userInfo(ctx, config.Client(ctx, token))

		if err != nil {
			err = providerUnavailable(err)
		}
	}

	if err != nil {
		return auth.External{}, err
	}

	// The token response lists the granted scopes; when a provider omits
	// it, the requested scopes were granted (RFC 6749 section 5.1).
	scope, _ := token.Extra("scope").(string)

	if strings.TrimSpace(scope) == "" {
		scope = strings.Join(config.Scopes, " ")
	}

	if identity.Claims == nil {
		identity.Claims = map[string]any{}
	}

	identity.Claims["scope"] = strings.Join(strings.Fields(scope), " ")

	return identity, nil
}

// exchangeFailure distinguishes an invalid, expired or consumed authorization
// grant from provider availability failures. Both paths retain the native cause;
// callers may inspect it explicitly, but must not log sensitive response text.
func exchangeFailure(err error) error {
	var failure *oauth2.RetrieveError

	if errors.As(err, &failure) && failure.ErrorCode == "invalid_grant" {
		return invalidCallback(err)
	}

	return providerUnavailable(err)
}

// idTokenIdentity verifies token's ID token signature, expiry, issuer and
// positional client-ID audience before matching the saved, nonempty nonce.
// Configured API audiences do not apply to browser login. Missing ID tokens
// and failed checks return ErrInvalidCallback before local identity mapping.
func (client *Client) idTokenIdentity(ctx context.Context, token *oauth2.Token, nonce string) (auth.External, error) {
	raw, ok := token.Extra("id_token").(string)

	if !ok {
		return auth.External{}, invalidCallback(nil)
	}

	identity, err := client.verify(ctx, raw, []string{client.clientID})

	if err != nil {
		return auth.External{}, invalidCallback(err)
	}

	if got, _ := identity.Claims["nonce"].(string); nonce == "" || got != nonce {
		return auth.External{}, invalidCallback(nil)
	}

	return identity, nil
}
