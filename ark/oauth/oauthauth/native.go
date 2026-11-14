package oauthauth

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
)

// maxNativeBody bounds the JSON body of [Client.NativeHandler]. A signed ID
// token is a few kilobytes; the cap only keeps a hostile client from streaming
// more.
const maxNativeBody = 64 << 10

// nativeNonce is the single-use nonce [Client.NonceHandler] keeps in the
// session until [Client.NativeHandler] spends it.
type nativeNonce struct {
	// Value is the random nonce handed to the app.
	Value string `json:"value"`

	// Expires is when the nonce stops being accepted. It uses the lifetime of a
	// browser login: the user has to finish the provider's native prompt.
	Expires time.Time `json:"expires"`
}

// nativeNonceBody is the JSON response of [Client.NonceHandler].
type nativeNonceBody struct {
	// Nonce is the raw nonce the app passes to the provider's SDK.
	Nonce string `json:"nonce"`
}

// nativeLoginBody is the JSON request of [Client.NativeHandler].
type nativeLoginBody struct {
	// IDToken is the compact JWT the provider's SDK returned to the app.
	IDToken string `json:"id_token"`
}

// nonceField returns the session field that holds the nonce of this client.
// It is per client name, so a Google and an Apple client of one application
// keep separate nonces in the same session.
func (client *Client) nonceField() session.Field[nativeNonce] {
	return session.NewField[nativeNonce]("arkoren:oauth:native-nonce:" + client.config.Name)
}

// nativeReady reports why the native handlers cannot serve: the provider has
// no native login ([ErrUnsupported]) or the application listed no audiences
// ([ErrNoNativeAudiences]).
func (client *Client) nativeReady() error {
	if client.provider.nativeNonce == nil {
		return ErrUnsupported
	}

	if len(client.config.NativeAudiences) == 0 {
		return ErrNoNativeAudiences
	}

	return nil
}

// NonceHandler is the first step of a native ID-token login. A GET issues a
// fresh random nonce, stores it in the request's session and answers
// {"nonce": "..."} with Cache-Control: no-store. The mobile app keeps the
// session cookie (its cookie jar), passes the nonce to the provider's SDK, and
// later calls [Client.NativeHandler] on the same session. Asking again replaces
// the nonce; a nonce expires after ten minutes and is spent by its first use.
//
// What the app passes to the SDK depends on the provider:
//   - Google (Credential Manager setNonce, GoogleSignIn nonce): the nonce
//     itself. Google returns it unchanged in the ID token's nonce claim.
//   - Apple (ASAuthorizationOpenIDRequest.nonce): the lowercase hexadecimal
//     SHA-256 of the nonce. Apple puts the request nonce into the identity
//     token unchanged, and the client requires the hash form so that the
//     nonce the token carries is not the secret kept in the session.
//
// The session is the default session of [session.Middleware]; a guard built
// with a custom GuardBuilder.Handle needs the default session to hold the
// nonce, so run the session middleware for both. A request without a session
// returns auth.ErrUnauthenticated. Presets without native login (every one but
// Google and Apple) return [ErrUnsupported]; without
// [ClientBuilder.NativeAudiences] the handler returns [ErrNoNativeAudiences]
// before touching the session. Mount it behind the same rate limit as login.
//
// Example:
//
//	r.Get("/auth/google/nonce", client.NonceHandler())
func (client *Client) NonceHandler() router.Handler {
	return func(writer http.ResponseWriter, httpRequest *http.Request) error {
		if err := client.nativeReady(); err != nil {
			return err
		}

		current, ok := session.FromContext(httpRequest.Context())

		if !ok {
			return auth.ErrUnauthenticated
		}

		value, err := random()

		if err != nil {
			return err
		}

		if err := current.Put(client.nonceField(), nativeNonce{Value: value, Expires: time.Now().Add(loginTTL)}); err != nil {
			return err
		}

		writer.Header().Set("Cache-Control", "no-store")

		return response.JSON(writer, http.StatusOK, nativeNonceBody{Nonce: value})
	}
}

// NativeHandler completes a native ID-token login for a mobile app that
// signed in with the provider's own SDK instead of a browser redirect. The
// request is POST {"id_token": "..."} on the session that called
// [Client.NonceHandler]. The handler spends the session's nonce, then verifies
// the token's signature, expiry, issuer (including the preset's identity
// policy such as Google's hosted domain), audience and nonce, calls resolve
// with the verified [auth.External], and hands the local subject to
// guard.Begin with a federated factor ([MethodFederated]). The response is
// outcome.Response: 204 when the user is logged in, or 202 listing the
// second-factor methods when the login is parked, exactly like every other
// login method. The session ID is regenerated and the app keeps using its
// cookie; a successful login publishes [LoginSucceeded].
//
// Accepted audiences are exactly [ClientBuilder.NativeAudiences]; neither the
// client ID nor [ClientBuilder.Audiences] is added. For Google an ID token's
// audience is the server client ID the app passed to the SDK: the web client
// ID when it sets one (the usual case, and then the same as the client ID given
// to [New]), otherwise the iOS or Android client ID. For Apple the audience of
// a native token is the app's bundle ID, not the Services ID. The nonce claim
// must be present and equal the issued nonce (Google), or the lowercase
// hexadecimal SHA-256 of it (Apple); see [Client.NonceHandler]. The Google
// email is verified by email_verified, and Apple's email, which may be a
// private relay address, by email_verified (a boolean or the string "true").
// Apple tokens carry no name: the SDK gives it to the app on the first
// authorization only, so the app has to send it to the application itself.
// Claims["scope"] is not set, because no authorization code is exchanged.
//
// The nonce is spent before the token is verified, so one nonce serves one
// attempt: a replayed, forged or wrong-audience token costs the app a new
// nonce request. A missing, expired or already used nonce, a bad token, or a
// nonce mismatch returns [ErrInvalidCallback] and publishes [LoginFailed]; a
// malformed body or an empty id_token returns [ErrInvalidLoginRequest] without
// spending the nonce. The resolver and guard errors behave as in
// [Client.CallbackHandler]. Presets without native login return
// [ErrUnsupported], a client without audiences [ErrNoNativeAudiences], a nil
// guard or resolve a configuration error, and a client that has not started
// [ErrNotStarted], all before the session is read. A request without a session
// returns auth.ErrUnauthenticated.
//
// Example:
//
//	r.Post("/auth/google/native", client.NativeHandler(guard, resolve))
func (client *Client) NativeHandler(guard *auth.Guard, resolve auth.Resolver) router.Handler {
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

		if err := client.nativeReady(); err != nil {
			return err
		}

		if _, err := client.started(); err != nil {
			return err
		}

		current, ok := session.FromContext(httpRequest.Context())

		if !ok {
			return auth.ErrUnauthenticated
		}

		body, err := request.JSON[nativeLoginBody](httpRequest, request.MaxSize(maxNativeBody))

		if err != nil {
			return err
		}

		if body.IDToken == "" {
			return invalidLoginRequest(errors.New("missing id_token"))
		}

		issued, err := current.Get(client.nonceField())

		// Spend the nonce first so that every outcome below, including a token
		// that fails verification, needs a new one.
		if err == nil {
			err = current.Delete(client.nonceField())
		}

		if err != nil || issued.Value == "" || !issued.Expires.After(time.Now()) {
			return client.loginFailed(httpRequest, invalidCallback(err))
		}

		external, err := client.verify(httpRequest.Context(), body.IDToken, client.config.NativeAudiences)

		if err != nil {
			return client.loginFailed(httpRequest, invalidCallback(err))
		}

		claimed, _ := external.Claims["nonce"].(string)
		expected := client.provider.nativeNonce(issued.Value)

		if claimed == "" || subtle.ConstantTimeCompare([]byte(claimed), []byte(expected)) != 1 {
			return client.loginFailed(httpRequest, invalidCallback(nil))
		}

		subject, err := resolve(httpRequest.Context(), external)

		if err != nil {
			return client.loginFailed(httpRequest, callbackMapperError(err))
		}

		outcome, err := guard.Begin(httpRequest, subject, federatedFactor)

		if err != nil {
			return client.loginFailed(httpRequest, err)
		}

		if outcome.Complete() {
			client.config.Events.Emit(httpRequest.Context(), LoginSucceeded, LoginSucceededEvent{
				Subject:      external.Subject,
				Issuer:       external.Issuer,
				LocalSubject: subject,
			})
		}

		return outcome.Response(writer)
	}
}
