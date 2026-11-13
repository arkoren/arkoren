package oauthauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/oauth2"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
)

// Callback completes a login. It accepts the provider's GET redirect or POST
// form, finds and deletes the login cookie, exchanges the code and verifies
// the user (an ID token for OIDC, the user info function for OAuth2). It then
// calls resolve to map the external [Identity] to the application's user
// subject and hands that subject to login.Begin with a federated factor, which
// logs the user in and regenerates the session ID.
//
// A completed login publishes [LoginSucceeded] and redirects to the validated
// return_to target. When login requires a second factor, Begin parks the login
// as pending and the request stays anonymous: Callback then redirects to the
// path set with [ClientBuilder.ChallengeURL], publishes no [LoginSucceeded] and
// does not carry return_to along; the challenge page finishes the login with
// [auth.Guard.Complete]. Without a challenge URL it ends the pending login with
// login.Logout and fails with [ErrNoChallengeURL]. It never falls back to a
// login without the second factor.
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
// Applications with their own users return the subject of their user record,
// for example from their typed user driver Subject method;
// the external [Identity] is the input, never the subject itself. Linking
// accounts by email is unsafe.
//
// Example:
//
//	r.Get("/oauth/callback", client.Callback(guard, func(ctx context.Context, external oauthauth.Identity) (string, error) {
//		user, err := users.UpsertBySubject(ctx, external.Issuer, external.Subject, external.Email)
//		if err != nil {
//			return "", err
//		}
//
//		return driver.Subject(user) // the application's typed user driver
//	}))
func (client *Client) Callback(guard *auth.Guard, resolve func(ctx context.Context, external Identity) (subject string, err error)) router.Handler {
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

		saved, err := client.take(writer, httpRequest, param(httpRequest, "state"))

		if err != nil {
			return client.loginFailed(httpRequest, invalidCallback(err))
		}

		code := param(httpRequest, "code")

		if code == "" || param(httpRequest, "error") != "" {
			return client.loginFailed(httpRequest, invalidCallback(nil))
		}

		external, err := client.callbackIdentity(httpRequest, ready, code, saved)

		if err != nil {
			return client.loginFailed(httpRequest, err)
		}

		subject, err := resolve(httpRequest.Context(), external)

		if err != nil {
			return client.loginFailed(httpRequest, callbackMapperError(err))
		}

		complete, err := guard.Begin(httpRequest, subject, auth.Factor{Method: auth.MethodFederated, Kind: auth.KindFederated})

		if errors.Is(err, session.ErrNoSession) {
			return auth.ErrUnauthenticated
		}

		if err != nil {
			return client.loginFailed(httpRequest, err)
		}

		if complete {
			client.config.Events.Emit(httpRequest.Context(), LoginSucceeded, LoginSucceededEvent{
				Subject: external.Subject,
				Issuer:  external.Issuer,
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
func (client *Client) callbackIdentity(request *http.Request, ready *endpoints, code string, saved loginState) (Identity, error) {
	external, err := client.identity(request.Context(), ready, code, saved)

	if err != nil {
		return Identity{}, err
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
func (client *Client) identity(ctx context.Context, ready *endpoints, code string, saved loginState) (Identity, error) {
	config := ready.oauth

	if secret := client.provider.clientSecret; secret != nil {
		signed, err := secret(config.ClientID, config.ClientSecret)

		if err != nil {
			return Identity{}, fmt.Errorf("derive client secret: %w", err)
		}

		config.ClientSecret = signed
	}

	var options []oauth2.AuthCodeOption

	if saved.Verifier != "" {
		options = append(options, oauth2.VerifierOption(saved.Verifier))
	}

	token, err := config.Exchange(ctx, code, options...)

	if err != nil {
		return Identity{}, exchangeFailure(err)
	}

	var identity Identity

	if client.provider.isOIDC() {
		identity, err = client.idTokenIdentity(ctx, token, saved.Nonce)
	} else {
		identity, err = client.provider.userInfo(ctx, config.Client(ctx, token))

		if err != nil {
			err = providerUnavailable(err)
		}
	}

	if err != nil {
		return Identity{}, err
	}

	// The token response lists the granted scopes; when a provider omits
	// it, the requested scopes were granted (RFC 6749 section 5.1).
	scope, _ := token.Extra("scope").(string)
	identity.Scopes = strings.Fields(scope)

	if len(identity.Scopes) == 0 {
		identity.Scopes = slices.Clone(config.Scopes)
	}

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
func (client *Client) idTokenIdentity(ctx context.Context, token *oauth2.Token, nonce string) (Identity, error) {
	raw, ok := token.Extra("id_token").(string)

	if !ok {
		return Identity{}, invalidCallback(nil)
	}

	identity, err := client.verify(ctx, raw, []string{client.clientID})

	if err != nil {
		return Identity{}, invalidCallback(err)
	}

	if got, _ := identity.Claims["nonce"].(string); nonce == "" || got != nonce {
		return Identity{}, invalidCallback(nil)
	}

	return identity, nil
}
