package oauthauth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/session"
	"arkoren.dev/foundation/problem"
)

// TestOIDCLoginSelectsSessionHandle verifies callback login rotates and writes
// the selected session, leaving a distinct default session untouched.
func TestOIDCLoginSelectsSessionHandle(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	jar := cookieJar{}

	query := login(t, client, jar)

	current := newSession()
	before := current.ID()
	defaultSession := newSession()
	defaultSession.MarkAsUnchanged()
	defaultID := defaultSession.ID()

	handle := session.NewHandle()
	identity := auth.NewSessionIdentity(func(_ context.Context, id string) (string, error) {
		return id, nil
	}, func(authenticator *auth.SessionBuilder) {
		authenticator.Handle(handle)
	})

	httpRequest := callbackRequest(http.MethodGet, jar, defaultSession, query.Get("state"), query.Get("nonce"))
	httpRequest = httpRequest.WithContext(session.NewContextFor(httpRequest.Context(), handle, current))
	var lookups atomic.Int64
	httpRequest = httpRequest.WithContext(bridgeContext{Context: httpRequest.Context(), lookups: &lookups})

	if status := client.Callback(identity, idLogin).Record(httpRequest).StatusCode; status != http.StatusFound {
		t.Fatalf("callback status = %d", status)
	}

	if lookups.Load() != 1 {
		t.Fatalf("callback resolved session %d times, want once", lookups.Load())
	}

	if request.MustSession(httpRequest, request.WithSessionHandle(handle)) != current {
		t.Fatal("request did not select the keyed login session")
	}

	id, err := identity.Authenticator()(httpRequest)

	if err != nil || id != "user-1" {
		t.Fatalf("user = %q, err = %v", id, err)
	}

	if current.ID() == before || defaultSession.ID() != defaultID || defaultSession.HasChanged() {
		t.Fatal("callback mutated the wrong session or failed to rotate selected session")
	}
}

// TestOIDCFlow verifies the whole browser login: cookie attributes, PKCE,
// identity, session login, redirect, and that the cookie is spent.
func TestOIDCFlow(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	jar := cookieJar{}

	query := login(t, client, jar)

	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("nonce") == "" {
		t.Fatalf("login URL lacks PKCE or nonce: %v", query)
	}

	if len(jar) != 1 {
		t.Fatalf("cookies = %d, want 1", len(jar))
	}

	for name, cookie := range jar {
		if name != client.cookieName(query.Get("state")) || !strings.HasPrefix(name, "__Host-arkoren.oauth.") || len(name) != len("__Host-arkoren.oauth.")+12 {
			t.Errorf("cookie name = %q", name)
		}

		if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" || cookie.MaxAge != 600 || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie attributes = %+v", cookie)
		}
	}

	sess := newSession()
	before := sess.ID()
	httpRequest := callbackRequest(http.MethodGet, jar, sess, query.Get("state"), query.Get("nonce"))

	response := client.Callback(localIdentity, func(_ context.Context, identity Identity) (string, error) {
		if identity.Name != "Ada" || identity.Issuer == "" {
			t.Errorf("identity = %+v", identity)
		}

		if ok, _ := HasScopes("openid", "read")(t.Context(), identity); !ok {
			t.Errorf("scopes = %v", identity.Scopes)
		}

		return identity.Subject, nil
	}).Record(httpRequest)

	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/dash" {
		t.Fatalf("response = %d %q", response.StatusCode, response.Header.Get("Location"))
	}

	if sess.ID() == before {
		t.Error("session ID was not regenerated")
	}

	if id, err := userOf(httpRequest); err != nil || id != "user-1" {
		t.Fatalf("user = %q, %v", id, err)
	}

	jar.apply(response)

	if len(jar) != 0 {
		t.Errorf("cookie was not deleted: %v", jar)
	}

	replay := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce"))

	if got := client.Callback(localIdentity, idLogin).Record(replay).StatusCode; got != http.StatusBadRequest {
		t.Errorf("replay status = %d, want 400", got)
	}
}

// requireForgedCallbackRejected verifies a callback whose state and nonce pass
// through forge, which may replace either, yields 400.
func requireForgedCallbackRejected(t *testing.T, forge func(state, nonce string) (string, string)) {
	t.Helper()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	jar := cookieJar{}

	query := login(t, client, jar)

	state, nonce := forge(query.Get("state"), query.Get("nonce"))

	response := client.Callback(localIdentity, idLogin).Record(callbackRequest(http.MethodGet, jar, newSession(), state, nonce))

	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.StatusCode)
	}
}

// TestCallbackRejectsForgedState verifies a forged state yields 400.
func TestCallbackRejectsForgedState(t *testing.T) {
	t.Parallel()

	requireForgedCallbackRejected(t, func(_, nonce string) (string, string) {
		return "forged", nonce
	})
}

// TestCallbackRejectsForgedNonce verifies a forged nonce yields 400.
func TestCallbackRejectsForgedNonce(t *testing.T) {
	t.Parallel()

	requireForgedCallbackRejected(t, func(state, _ string) (string, string) {
		return state, "forged"
	})
}

// TestParallelLogins verifies two logins in one browser both complete.
func TestParallelLogins(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	jar := cookieJar{}

	first, second := login(t, client, jar), login(t, client, jar)

	if len(jar) != 2 {
		t.Fatalf("cookies = %d, want 2", len(jar))
	}

	for _, query := range []url.Values{second, first} {
		response := client.Callback(localIdentity, idLogin).Record(callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce")))

		if response.StatusCode != http.StatusFound {
			t.Fatalf("status = %d, want 302", response.StatusCode)
		}

		jar.apply(response)
	}

	if len(jar) != 0 {
		t.Errorf("cookies left = %d", len(jar))
	}
}

// requireBadCookieRejected starts a login, lets mutate damage the browser's
// cookies, and verifies the callback yields 400.
func requireBadCookieRejected(t *testing.T, mutate func(t *testing.T, client *Client, jar cookieJar, query url.Values)) {
	t.Helper()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	jar := cookieJar{}

	query := login(t, client, jar)

	mutate(t, client, jar, query)

	response := client.Callback(localIdentity, idLogin).Record(callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce")))

	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.StatusCode)
	}
}

// TestCallbackRejectsMissingCookie verifies a missing login cookie yields 400.
func TestCallbackRejectsMissingCookie(t *testing.T) {
	t.Parallel()

	requireBadCookieRejected(t, func(_ *testing.T, _ *Client, jar cookieJar, _ url.Values) {
		clear(jar)
	})
}

// TestCallbackRejectsTamperedCookie verifies a tampered login cookie yields 400.
func TestCallbackRejectsTamperedCookie(t *testing.T) {
	t.Parallel()

	requireBadCookieRejected(t, func(t *testing.T, _ *Client, jar cookieJar, _ url.Values) {
		for _, cookie := range jar {
			sealed, err := base64.RawURLEncoding.DecodeString(cookie.Value)

			if err != nil {
				t.Fatal(err)
			}

			// Change an actual authenticated byte, not possibly unused base64 bits.
			sealed[len(sealed)/2] ^= 1
			cookie.Value = base64.RawURLEncoding.EncodeToString(sealed)
		}
	})
}

// TestCallbackRejectsExpiredCookie verifies an expired login cookie yields 400.
func TestCallbackRejectsExpiredCookie(t *testing.T) {
	t.Parallel()

	requireBadCookieRejected(t, func(t *testing.T, client *Client, jar cookieJar, query url.Values) {
		clear(jar)

		recorder := httptest.NewRecorder()
		state := loginState{
			Cookie: client.cookieName(query.Get("state")),
			State:  query.Get("state"),
			Nonce:  query.Get("nonce"),
		}

		if err := client.save(recorder, state); err != nil {
			t.Fatal(err)
		}

		jar.apply(recorder.Result())
	})
}

// TestCallbackRejectsMovedCookie verifies a login cookie value moved to another
// login's cookie name yields 400.
func TestCallbackRejectsMovedCookie(t *testing.T) {
	t.Parallel()

	requireBadCookieRejected(t, func(t *testing.T, client *Client, jar cookieJar, query url.Values) {
		other := login(t, client, jar)
		state := client.cookieName(query.Get("state"))
		otherName := client.cookieName(other.Get("state"))

		jar[state].Value = jar[otherName].Value
		delete(jar, otherName)
	})
}

// TestFormPostCallback verifies a form_post provider: SameSite=None and a
// POST callback that ignores the query string.
func TestFormPostCallback(t *testing.T) {
	t.Parallel()

	provider := OIDC(newTestProvider(t).server.URL)
	provider.formPost = true
	client := newClient(t, provider)
	jar := cookieJar{}

	query := login(t, client, jar)

	if query.Get("response_mode") != "form_post" {
		t.Errorf("response_mode = %q", query.Get("response_mode"))
	}

	for _, cookie := range jar {
		if cookie.SameSite != http.SameSiteNoneMode {
			t.Errorf("SameSite = %v, want None", cookie.SameSite)
		}
	}

	queryOnly := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce"))
	queryOnly.Method = http.MethodPost

	if got := client.Callback(localIdentity, idLogin).Record(queryOnly).StatusCode; got != http.StatusBadRequest {
		t.Errorf("query-only POST status = %d, want 400", got)
	}

	response := client.Callback(localIdentity, idLogin).Record(callbackRequest(http.MethodPost, jar, newSession(), query.Get("state"), query.Get("nonce")))

	if response.StatusCode != http.StatusFound {
		t.Errorf("POST status = %d, want 302", response.StatusCode)
	}
}

// TestOAuth2Flow verifies a plain OAuth2 provider uses the user info
// function and does not support bearer verification.
func TestOAuth2Flow(t *testing.T) {
	t.Parallel()

	base := newTestProvider(t).server.URL
	client := newClient(t, OAuth2(Endpoint{
		AuthURL:  base + "/auth",
		TokenURL: base + "/token",
	}, userInfoStub))
	jar := cookieJar{}

	query := login(t, client, jar)

	if query.Has("nonce") {
		t.Error("OAuth2 login sent a nonce")
	}

	sess := newSession()
	httpRequest := callbackRequest(http.MethodGet, jar, sess, query.Get("state"), "code")

	response := client.Callback(externalIdentity, func(_ context.Context, identity Identity) (Identity, error) {
		return identity, nil
	}).Record(httpRequest)

	if response.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", response.StatusCode)
	}

	identity, err := userOfIdentity(httpRequest)

	if err != nil || identity.Subject != "stub-1" || identity.Issuer != "stub" || len(identity.Scopes) != 2 {
		t.Fatalf("identity = %+v, %v", identity, err)
	}

	if _, err := client.Verify(t.Context(), "token"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Verify error = %v, want ErrUnsupported", err)
	}
}

// requireLoginErrorMapped verifies a mapper failing with mapperErr makes the
// callback return want.
func requireLoginErrorMapped(t *testing.T, mapperErr, want error) {
	t.Helper()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	jar := cookieJar{}

	query := login(t, client, jar)

	httpRequest := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce"))

	err := client.Callback(localIdentity, func(context.Context, Identity) (string, error) {
		return "", mapperErr
	})(httptest.NewRecorder(), httpRequest)

	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}

	if !errors.Is(err, mapperErr) {
		t.Fatalf("error = %v, lost mapper cause %v", err, mapperErr)
	}

	if errors.Is(err, ErrInvalidCallback) != errors.Is(want, ErrInvalidCallback) {
		t.Fatalf("error = %v, incorrect callback classification for %v", err, mapperErr)
	}

	if errors.Is(want, ErrInvalidCallback) {
		var details problem.Details

		if !errors.As(err, &details) || details.Status() != http.StatusBadRequest {
			t.Fatalf("error = %v, want callback problem with status 400", err)
		}
	}
}

// TestCallbackMapsNoCredentials preserves the browser callback's invalid-callback
// response for a mapper that refuses the external identity without credentials.
func TestCallbackMapsNoCredentials(t *testing.T) {
	t.Parallel()

	requireLoginErrorMapped(t, auth.ErrNoCredentials, ErrInvalidCallback)
}

// TestCallbackMapsWrappedNoCredentials verifies that adding application context
// to a credential rejection does not change the browser callback response.
func TestCallbackMapsWrappedNoCredentials(t *testing.T) {
	t.Parallel()

	requireLoginErrorMapped(t, fmt.Errorf("account mapping: %w", auth.ErrNoCredentials), ErrInvalidCallback)
}

// TestCallbackKeepsOtherLoginErrors verifies other mapper errors pass through.
func TestCallbackKeepsOtherLoginErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")

	requireLoginErrorMapped(t, boom, boom)
}

// TestCallbackKeepsUnauthenticated verifies an application's explicit HTTP
// rejection is not converted into the credential-specific callback error.
func TestCallbackKeepsUnauthenticated(t *testing.T) {
	t.Parallel()

	requireLoginErrorMapped(t, auth.ErrUnauthenticated, auth.ErrUnauthenticated)
}

// TestCallbackUsesClientIDAudienceWithAPIAudiences exercises a complete browser
// callback on a client also configured to verify tokens for a separate API.
// The provider's ID token names the positional client ID, not that API.
func TestCallbackUsesClientIDAudienceWithAPIAudiences(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences("api")
	})
	jar := cookieJar{}

	query := login(t, client, jar)

	current := newSession()
	before := current.ID()
	request := callbackRequest(http.MethodGet, jar, current, query.Get("state"), query.Get("nonce"))

	response := client.Callback(localIdentity, idLogin).Record(request)

	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/dash" {
		t.Fatalf("callback = %d %q, want redirect to /dash", response.StatusCode, response.Header.Get("Location"))
	}

	if current.ID() == before {
		t.Fatal("callback did not rotate the session")
	}

	if id, err := userOf(request); err != nil || id != "user-1" {
		t.Fatalf("callback user = %q, error = %v", id, err)
	}
}

// TestCallbackRejectsAPIAudienceWithAPIAudiences ensures a valid signature and
// matching nonce cannot make an API-only audience valid for browser login.
func TestCallbackRejectsAPIAudienceWithAPIAudiences(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences("api")
	})

	raw := provider.sign(map[string]any{"aud": "api", "nonce": "saved-nonce"})
	token := (&oauth2.Token{}).WithExtra(map[string]any{"id_token": raw})

	if _, err := client.idTokenIdentity(t.Context(), token, "saved-nonce"); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("ID token error = %v, want ErrInvalidCallback", err)
	}
}

// TestCallbackChecksNonceWithAPIAudiences retains nonce binding when the client
// also has API audiences, even for a correctly signed client-ID token.
func TestCallbackChecksNonceWithAPIAudiences(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences("api")
	})

	raw := provider.sign(map[string]any{"nonce": "different-nonce"})
	token := (&oauth2.Token{}).WithExtra(map[string]any{"id_token": raw})

	if _, err := client.idTokenIdentity(t.Context(), token, "saved-nonce"); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("ID token error = %v, want ErrInvalidCallback", err)
	}
}

// TestCallbackChecksIssuerWithAPIAudiences retains the discovered issuer check
// for client-ID tokens on a client that also accepts API bearer tokens.
func TestCallbackChecksIssuerWithAPIAudiences(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences("api")
	})

	raw := provider.sign(map[string]any{"iss": "https://other.example", "nonce": "saved-nonce"})
	token := (&oauth2.Token{}).WithExtra(map[string]any{"id_token": raw})

	if _, err := client.idTokenIdentity(t.Context(), token, "saved-nonce"); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("ID token error = %v, want ErrInvalidCallback", err)
	}
}

// TestCallbackChecksExpiryWithAPIAudiences rejects an expired client-ID token
// even when its nonce and signing key match the pending browser login.
func TestCallbackChecksExpiryWithAPIAudiences(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences("api")
	})

	raw := provider.sign(map[string]any{"exp": time.Now().Add(-time.Hour).Unix(), "nonce": "saved-nonce"})
	token := (&oauth2.Token{}).WithExtra(map[string]any{"id_token": raw})

	if _, err := client.idTokenIdentity(t.Context(), token, "saved-nonce"); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("ID token error = %v, want ErrInvalidCallback", err)
	}
}

// TestCallbackChecksSignatureWithAPIAudiences rejects a token signed with an
// untrusted key despite a correct issuer, client-ID audience, expiry and nonce.
func TestCallbackChecksSignatureWithAPIAudiences(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	untrusted := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences("api")
	})

	raw := untrusted.sign(map[string]any{"iss": provider.server.URL, "nonce": "saved-nonce"})
	token := (&oauth2.Token{}).WithExtra(map[string]any{"id_token": raw})

	if _, err := client.idTokenIdentity(t.Context(), token, "saved-nonce"); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("ID token error = %v, want ErrInvalidCallback", err)
	}
}

// TestCallbackOwnsFallbackScopes ensures mapper-owned identities cannot mutate
// scope parameters of subsequent authorization requests when scope is omitted.
func TestCallbackOwnsFallbackScopes(t *testing.T) {
	t.Parallel()

	client := newClient(t, OAuth2(Endpoint{
		AuthURL: "https://provider.example/auth", TokenURL: "https://provider.example/token",
	}, userInfoStub))
	jar := cookieJar{}

	query := login(t, client, jar)

	request := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), "code")
	transport := protocolTransport(func(*http.Request) (*http.Response, error) {
		return protocolResponse(http.StatusOK, `{"access_token":"synthetic","token_type":"Bearer"}`), nil
	})
	request = request.WithContext(context.WithValue(request.Context(), oauth2.HTTPClient, &http.Client{Transport: transport}))

	response := client.Callback(localIdentity, func(_ context.Context, identity Identity) (string, error) {
		identity.Scopes[0] = "admin"

		return identity.Subject, nil
	}).Record(request)

	if response.StatusCode != http.StatusFound {
		t.Fatal(response.StatusCode)
	}

	if got := login(t, client, cookieJar{}).Get("scope"); got != "openid read" {
		t.Fatalf("subsequent requested scopes = %q", got)
	}
}

// TestCallbackInvalidGrantIsNotAnOutage preserves the native exchange cause
// while classifying an invalid, expired or consumed code as a bad callback.
func TestCallbackInvalidGrantIsNotAnOutage(t *testing.T) {
	t.Parallel()

	client := newClient(t, OAuth2(Endpoint{
		AuthURL: "https://provider.example/auth", TokenURL: "https://provider.example/token",
	}, userInfoStub))
	jar := cookieJar{}

	query := login(t, client, jar)

	transport := protocolTransport(func(*http.Request) (*http.Response, error) {
		return protocolResponse(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"synthetic consumed code"}`), nil
	})
	request := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), "consumed")
	request = request.WithContext(context.WithValue(request.Context(), oauth2.HTTPClient, &http.Client{Transport: transport}))

	err := client.Callback(localIdentity, idLogin)(httptest.NewRecorder(), request)

	if !errors.Is(err, ErrInvalidCallback) || errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("invalid_grant classification = %v", err)
	}

	var failure *oauth2.RetrieveError

	if !errors.As(err, &failure) || failure.ErrorCode != "invalid_grant" {
		t.Fatalf("native invalid_grant cause lost: %v", err)
	}
}
