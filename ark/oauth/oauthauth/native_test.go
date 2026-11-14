package oauthauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/foundation/key"
)

// nativeAudience is the audience of the fixture mobile app's ID tokens. It is
// deliberately not the client ID "cid" that browser logins use.
const nativeAudience = "mobile-app"

// withSession attaches current as the default session of request.
func withSession(request *http.Request, current *session.Session) *http.Request {
	return request.WithContext(session.NewContext(request.Context(), current))
}

// nativeNonceFor requests a nonce on current the way a mobile app does.
func nativeNonceFor(t *testing.T, client *Client, current *session.Session) string {
	t.Helper()

	response := client.NonceHandler().Record(withSession(httptest.NewRequest(http.MethodGet, "/nonce", nil), current))

	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("nonce = %d, Cache-Control %q", response.StatusCode, response.Header.Get("Cache-Control"))
	}

	var body struct {
		Nonce string `json:"nonce"`
	}

	if err := json.UnmarshalRead(response.Body, &body); err != nil || body.Nonce == "" {
		t.Fatalf("nonce body = %+v, %v", body, err)
	}

	return body.Nonce
}

// nativePost builds the native login request for idToken on current.
func nativePost(idToken string, current *session.Session) *http.Request {
	body := `{"id_token":"` + idToken + `"}`

	return withSession(httptest.NewRequest(http.MethodPost, "/native", strings.NewReader(body)), current)
}

// nativeGoogle returns a started Google client for the fixture provider that
// accepts nativeAudience, with the events bus of recordEvents.
func nativeGoogle(t *testing.T, provider *testProvider, configure ...func(*ClientBuilder)) (*Client, *[]string) {
	t.Helper()

	bus, seen := recordEvents(t)
	policies := append([]func(*ClientBuilder){func(client *ClientBuilder) {
		client.NativeAudiences(nativeAudience)
		client.Events(bus)
	}}, configure...)

	return newClient(t, google(provider.server.URL), policies...), seen
}

// resolveCapturing maps the identity to its subject and keeps the External.
func resolveCapturing(external *auth.External) auth.Resolver {
	return func(_ context.Context, verified auth.External) (string, error) {
		*external = verified

		return verified.Subject, nil
	}
}

// TestNativeGoogleThenExchange logs in with a Google ID token, then checks that
// a user who also has a password passes guard.RecentLogin, as the exchange of a
// session for a device token requires, and that the verified identity carries
// the claims an application resolves on.
func TestNativeGoogleThenExchange(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client, seen := nativeGoogle(t, provider)
	guard := newGuard(t, func(policy *auth.GuardBuilder) {
		policy.StepUp(testCredential(auth.MethodPassword, auth.KindKnowledge, true))
	})
	current := newSession()
	nonce := nativeNonceFor(t, client, current)

	token := provider.signFor(nativeAudience, map[string]any{
		"nonce":          nonce,
		"email":          "ada@example.com",
		"email_verified": true,
	})

	var external auth.External

	request := nativePost(token, current)
	response := client.NativeHandler(guard, resolveCapturing(&external)).Record(request)

	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("native status = %d, want 204", response.StatusCode)
	}

	if external.Issuer != provider.server.URL || external.Subject != "user-1" || external.Email != "ada@example.com" || !external.EmailVerified {
		t.Fatalf("external = %+v", external)
	}

	if subject, err := userOf(guard, request); err != nil || subject != "user-1" {
		t.Fatalf("user = %q, %v", subject, err)
	}

	if err := guard.RecentLogin(request, time.Minute); err != nil {
		t.Fatalf("RecentLogin after a native login = %v", err)
	}

	if !slices.Equal(*seen, []string{"succeeded:user-1"}) {
		t.Fatalf("events = %v", *seen)
	}
}

// TestNativeRejectsWrongAudience verifies a token issued to another client is
// refused, including a browser token for the client ID given to New, because
// only the configured native audiences count.
func TestNativeRejectsWrongAudience(t *testing.T) {
	t.Parallel()

	for _, audience := range []string{"someone-else", "cid"} {
		provider := newTestProvider(t)
		client, seen := nativeGoogle(t, provider)
		guard := newGuard(t)
		current := newSession()
		nonce := nativeNonceFor(t, client, current)
		request := nativePost(provider.signFor(audience, map[string]any{"nonce": nonce}), current)

		err := client.NativeHandler(guard, idLogin)(httptest.NewRecorder(), request)

		if !errors.Is(err, ErrInvalidCallback) {
			t.Errorf("audience %q: error = %v, want ErrInvalidCallback", audience, err)
		}

		if _, err := userOf(guard, request); !errors.Is(err, auth.ErrNoCredentials) {
			t.Errorf("audience %q: logged in: %v", audience, err)
		}

		if !slices.Equal(*seen, []string{"failed"}) {
			t.Errorf("audience %q: events = %v", audience, *seen)
		}
	}
}

// TestNativeRejectsReplayedNonce verifies a nonce serves one attempt: a valid
// token is refused when replayed on the same session, on a session that never
// asked for a nonce, and after a failed attempt spent the nonce.
func TestNativeRejectsReplayedNonce(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client, _ := nativeGoogle(t, provider)
	guard := newGuard(t)
	handler := client.NativeHandler(guard, idLogin)
	current := newSession()
	token := provider.signFor(nativeAudience, map[string]any{"nonce": nativeNonceFor(t, client, current)})

	if status := handler.Record(nativePost(token, current)).StatusCode; status != http.StatusNoContent {
		t.Fatalf("first use = %d, want 204", status)
	}

	if err := handler(httptest.NewRecorder(), nativePost(token, current)); !errors.Is(err, ErrInvalidCallback) {
		t.Errorf("replay on the same session = %v, want ErrInvalidCallback", err)
	}

	if err := handler(httptest.NewRecorder(), nativePost(token, newSession())); !errors.Is(err, ErrInvalidCallback) {
		t.Errorf("replay on another session = %v, want ErrInvalidCallback", err)
	}

	// A token with the wrong nonce fails, and spends the issued one.
	other := newSession()
	issued := nativeNonceFor(t, client, other)

	if err := handler(httptest.NewRecorder(), nativePost(provider.signFor(nativeAudience, map[string]any{"nonce": "guess"}), other)); !errors.Is(err, ErrInvalidCallback) {
		t.Errorf("wrong nonce = %v, want ErrInvalidCallback", err)
	}

	valid := provider.signFor(nativeAudience, map[string]any{"nonce": issued})

	if err := handler(httptest.NewRecorder(), nativePost(valid, other)); !errors.Is(err, ErrInvalidCallback) {
		t.Errorf("nonce after a failed attempt = %v, want ErrInvalidCallback", err)
	}

	// A token without any nonce claim never passes.
	third := newSession()
	_ = nativeNonceFor(t, client, third)

	if err := handler(httptest.NewRecorder(), nativePost(provider.signFor(nativeAudience, nil), third)); !errors.Is(err, ErrInvalidCallback) {
		t.Errorf("missing nonce claim = %v, want ErrInvalidCallback", err)
	}
}

// TestNativeRejectsExpiredNonce verifies a nonce older than the login lifetime
// is refused even with a matching token.
func TestNativeRejectsExpiredNonce(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client, _ := nativeGoogle(t, provider)
	current := newSession()
	nonce := nativeNonceFor(t, client, current)

	if err := current.Put(client.nonceField(), nativeNonce{Value: nonce, Expires: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}

	request := nativePost(provider.signFor(nativeAudience, map[string]any{"nonce": nonce}), current)

	if err := client.NativeHandler(newGuard(t), idLogin)(httptest.NewRecorder(), request); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("expired nonce = %v, want ErrInvalidCallback", err)
	}
}

// TestNativeAppleRequiresHashedNonce verifies Apple tokens carry the lowercase
// hex SHA-256 of the issued nonce: the hash passes and the raw nonce does not.
// Apple's string email_verified counts as verified.
func TestNativeAppleRequiresHashedNonce(t *testing.T) {
	t.Parallel()

	_, pemKey := appleKey(t)
	signing, err := key.ParseSigning(pemKey)

	if err != nil {
		t.Fatal(err)
	}

	provider := newTestProvider(t)
	apple := Apple("TEAM", "KEYID", signing)
	apple.issuer = provider.server.URL
	apple.clientSecret = nil
	client := newClient(t, apple, func(client *ClientBuilder) {
		client.NativeAudiences(nativeAudience)
	})
	guard := newGuard(t)

	for _, test := range []struct {
		name string
		hash bool
		want bool
	}{
		{name: "hashed", hash: true, want: true},
		{name: "raw", hash: false, want: false},
	} {
		current := newSession()
		nonce := nativeNonceFor(t, client, current)
		claim := nonce

		if test.hash {
			sum := sha256.Sum256([]byte(nonce))
			claim = hex.EncodeToString(sum[:])
		}

		var external auth.External

		token := provider.signFor(nativeAudience, map[string]any{"nonce": claim, "email": "x@privaterelay.appleid.com", "email_verified": "true"})
		err := client.NativeHandler(guard, resolveCapturing(&external))(httptest.NewRecorder(), nativePost(token, current))

		if test.want && (err != nil || !external.EmailVerified) {
			t.Errorf("%s: error = %v, external = %+v", test.name, err, external)
		}

		if !test.want && !errors.Is(err, ErrInvalidCallback) {
			t.Errorf("%s: error = %v, want ErrInvalidCallback", test.name, err)
		}
	}
}

// TestNativeParksSecondFactor verifies a native login of a user with a second
// factor answers 202 with the methods and logs nobody in, like every other
// login method, and publishes no LoginSucceeded.
func TestNativeParksSecondFactor(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client, seen := nativeGoogle(t, provider)
	guard := newGuard(t, withSecondFactor)
	current := newSession()
	token := provider.signFor(nativeAudience, map[string]any{"nonce": nativeNonceFor(t, client, current)})
	request := nativePost(token, current)

	response := client.NativeHandler(guard, idLogin).Record(request)

	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.StatusCode)
	}

	if _, err := userOf(guard, request); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("parked login authenticated: %v", err)
	}

	if len(*seen) != 0 {
		t.Fatalf("events = %v", *seen)
	}
}

// TestNativeConfiguration verifies the native handlers refuse to serve without
// audiences, for providers without native login, and for a missing session,
// guard or resolver, before any session change.
func TestNativeConfiguration(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	guard := newGuard(t)
	current := newSession()
	current.MarkAsUnchanged()

	noAudiences := newClient(t, google(provider.server.URL))
	generic := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.NativeAudiences(nativeAudience)
	})
	ready, _ := nativeGoogle(t, provider)

	for name, test := range map[string]struct {
		handler router.Handler
		request *http.Request
		want    error
	}{
		"nonce without audiences":  {noAudiences.NonceHandler(), withSession(httptest.NewRequest(http.MethodGet, "/", nil), current), ErrNoNativeAudiences},
		"native without audiences": {noAudiences.NativeHandler(guard, idLogin), nativePost("x", current), ErrNoNativeAudiences},
		"nonce unsupported":        {generic.NonceHandler(), withSession(httptest.NewRequest(http.MethodGet, "/", nil), current), ErrUnsupported},
		"native unsupported":       {generic.NativeHandler(guard, idLogin), nativePost("x", current), ErrUnsupported},
		"nonce without session":    {ready.NonceHandler(), httptest.NewRequest(http.MethodGet, "/", nil), auth.ErrUnauthenticated},
		"native without session":   {ready.NativeHandler(guard, idLogin), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)), auth.ErrUnauthenticated},
	} {
		if err := test.handler(httptest.NewRecorder(), test.request); !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", name, err, test.want)
		}
	}

	if current.HasChanged() {
		t.Error("a refused request changed the session")
	}

	for name, handler := range map[string]router.Handler{
		"nil guard":    ready.NativeHandler(nil, idLogin),
		"nil resolver": ready.NativeHandler(guard, nil),
	} {
		if err := handler(httptest.NewRecorder(), nativePost("x", current)); err == nil || errors.Is(err, ErrInvalidCallback) {
			t.Errorf("%s: error = %v, want a configuration error", name, err)
		}
	}
}

// TestNativeRejectsBadBody verifies a malformed body or an empty token is a
// bad request that leaves the nonce usable.
func TestNativeRejectsBadBody(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client, _ := nativeGoogle(t, provider)
	handler := client.NativeHandler(newGuard(t), idLogin)
	current := newSession()
	nonce := nativeNonceFor(t, client, current)

	for body, want := range map[string]error{
		`not json`:        nil,
		`{"id_token":""}`: ErrInvalidLoginRequest,
	} {
		request := withSession(httptest.NewRequest(http.MethodPost, "/native", strings.NewReader(body)), current)
		err := handler(httptest.NewRecorder(), request)

		if err == nil || (want != nil && !errors.Is(err, want)) {
			t.Errorf("body %q: error = %v", body, err)
		}
	}

	token := provider.signFor(nativeAudience, map[string]any{"nonce": nonce})

	if status := handler.Record(nativePost(token, current)).StatusCode; status != http.StatusNoContent {
		t.Fatalf("nonce was spent by a bad body: %d", status)
	}
}

// TestNativeNonceSurvivesSessionPersistence runs both native routes behind the
// real session middleware, so the nonce is saved after the first request and
// loaded from the store for the second, and a replay is refused afterwards.
func TestNativeNonceSurvivesSessionPersistence(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client, _ := nativeGoogle(t, provider)
	guard := newGuard(t)
	middleware := session.Middleware(cache.New(memorycache.New()), func(cookies *session.Builder) {
		cookies.AllowInsecure(true)
	})
	jar := cookieJar{}

	response := middleware(client.NonceHandler()).Record(jar.attach(httptest.NewRequest(http.MethodGet, "/nonce", nil)))
	jar.apply(response)

	var body struct {
		Nonce string `json:"nonce"`
	}

	if err := json.UnmarshalRead(response.Body, &body); err != nil || body.Nonce == "" {
		t.Fatalf("nonce = %+v, %v", body, err)
	}

	token := provider.signFor(nativeAudience, map[string]any{"nonce": body.Nonce})
	post := func() *http.Response {
		request := jar.attach(httptest.NewRequest(http.MethodPost, "/native", strings.NewReader(`{"id_token":"`+token+`"}`)))
		response := middleware(client.NativeHandler(guard, idLogin)).Record(request)
		jar.apply(response)

		return response
	}

	if status := post().StatusCode; status != http.StatusNoContent {
		t.Fatalf("native login = %d, want 204", status)
	}

	if status := post().StatusCode; status != http.StatusBadRequest {
		t.Fatalf("replay = %d, want 400", status)
	}
}
