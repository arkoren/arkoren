package oauthauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/session"
)

// federated is the factor the callback records, reused to log users in by hand.
var federated = auth.Factor{Method: auth.MethodFederated, Kind: auth.KindFederated}

// TestLogoutClearsLoginAndRedirects verifies the Logout handler.
func TestLogoutClearsLoginAndRedirects(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	guard := newGuard(t)
	request := callbackRequest(http.MethodPost, cookieJar{}, newSession(), "", "")

	if complete, err := guard.Begin(request, "user-1", federated); err != nil || !complete {
		t.Fatalf("Begin = %t, %v", complete, err)
	}

	response := client.Logout(guard).Record(request)

	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/" {
		t.Errorf("response = %d %q", response.StatusCode, response.Header.Get("Location"))
	}

	if _, err := userOf(guard, request); !errors.Is(err, auth.ErrNoCredentials) {
		t.Errorf("Authenticator() error = %v, want ErrNoCredentials", err)
	}
}

// TestLogoutPublishesLoggedOut verifies a successful logout signals LoggedOut.
func TestLogoutPublishesLoggedOut(t *testing.T) {
	t.Parallel()

	bus, events := recordEvents(t)
	client := newClient(t, OIDC(newTestProvider(t).server.URL), func(client *ClientBuilder) {
		client.Events(bus)
	})
	guard := newGuard(t)
	request := callbackRequest(http.MethodPost, cookieJar{}, newSession(), "", "")

	if _, err := guard.Begin(request, "user-1", federated); err != nil {
		t.Fatal(err)
	}

	client.Logout(guard).Record(request)

	if len(*events) != 1 {
		t.Fatalf("events = %v, want [logged-out]", *events)
	}
}

// TestLogoutWithoutSessionIsUnauthenticated verifies a request without a
// session fails with ErrUnauthenticated and publishes no event.
func TestLogoutWithoutSessionIsUnauthenticated(t *testing.T) {
	t.Parallel()

	bus, events := recordEvents(t)
	client := newClient(t, OIDC(newTestProvider(t).server.URL), func(client *ClientBuilder) {
		client.Events(bus)
	})
	request := httptest.NewRequest(http.MethodPost, "/logout", nil)
	writer := httptest.NewRecorder()

	err := client.Logout(newGuard(t))(writer, request)

	if !errors.Is(err, auth.ErrUnauthenticated) || len(*events) != 0 || len(writer.Header()) != 0 {
		t.Fatalf("error = %v, events = %d, headers = %v", err, len(*events), writer.Header())
	}
}

// TestLogoutNilGuardFails verifies a nil guard is a configuration error.
func TestLogoutNilGuardFails(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	request := callbackRequest(http.MethodPost, cookieJar{}, newSession(), "", "")

	err := client.Logout(nil)(httptest.NewRecorder(), request)

	if err == nil || errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("nil guard error = %v", err)
	}
}

// TestCallbackNilGuardHasNoEffects verifies the guard is configuration, not a
// deferred panic, and the saved flow stays usable after the rejection.
func TestCallbackNilGuardHasNoEffects(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	bus, events := recordEvents(t)
	client := newClient(t, OIDC(provider.server.URL), func(application *ClientBuilder) {
		application.Events(bus)
	})
	jar := cookieJar{}

	query := login(t, client, jar)

	current := newSession()
	current.MarkAsUnchanged()
	id := current.ID()
	r := callbackRequest(http.MethodGet, jar, current, query.Get("state"), query.Get("nonce"))
	w := httptest.NewRecorder()

	err := client.Callback(nil, idLogin)(w, r)

	if err == nil || errors.Is(err, ErrInvalidCallback) || errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("nil guard error = %v", err)
	}

	if current.ID() != id || current.HasChanged() || len(w.Header()) != 0 {
		t.Fatal("nil guard caused side effects")
	}

	if provider.exchanges.Load() != 0 || len(*events) != 0 {
		t.Fatal("nil guard exchanged a code or published an event")
	}

	if got := client.Callback(newGuard(t), idLogin).Record(r).StatusCode; got != http.StatusFound {
		t.Fatalf("nil guard consumed state: %d", got)
	}
}

// TestCallbackNilMapperHasNoEffects verifies the mapper is configuration, not a
// deferred panic, and the saved flow is still usable after rejection.
func TestCallbackNilMapperHasNoEffects(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	bus, events := recordEvents(t)
	client := newClient(t, OIDC(provider.server.URL), func(application *ClientBuilder) {
		application.Events(bus)
	})
	guard := newGuard(t)
	jar := cookieJar{}

	query := login(t, client, jar)

	current := newSession()
	current.MarkAsUnchanged()
	id := current.ID()
	r := callbackRequest(http.MethodGet, jar, current, query.Get("state"), query.Get("nonce"))
	w := httptest.NewRecorder()

	err := client.Callback(guard, nil)(w, r)

	if err == nil || errors.Is(err, ErrInvalidCallback) || errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("nil mapper error = %v", err)
	}

	if current.ID() != id || current.HasChanged() || len(w.Header()) != 0 {
		t.Fatal("nil mapper caused side effects")
	}

	if provider.exchanges.Load() != 0 || len(*events) != 0 {
		t.Fatal("nil mapper exchanged a code or published an event")
	}

	if got := client.Callback(guard, idLogin).Record(r).StatusCode; got != http.StatusFound {
		t.Fatalf("nil mapper consumed state: %d", got)
	}
}

// TestCallbackMissingSelectedSessionDoesNotFallback verifies default sessions
// cannot satisfy a login on a custom handle and rejection leaves state
// unconsumed.
func TestCallbackMissingSelectedSessionDoesNotFallback(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	jar := cookieJar{}

	query := login(t, client, jar)

	handle := session.NewHandle()
	guard := newGuard(t, func(policy *auth.GuardBuilder) {
		policy.Handle(handle)
	})
	r := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce"))
	w := httptest.NewRecorder()

	err := client.Callback(guard, idLogin)(w, r)

	if !errors.Is(err, auth.ErrUnauthenticated) || len(w.Header()) != 0 {
		t.Fatalf("missing selection: error = %v, headers = %v", err, w.Header())
	}

	r = r.WithContext(session.NewContextFor(r.Context(), handle, newSession()))

	if got := client.Callback(guard, idLogin).Record(r).StatusCode; got != http.StatusFound {
		t.Fatalf("missing selection consumed state: %d", got)
	}
}

// TestLogoutSelectsCustomSession verifies only the selected session loses its
// login and rotates, while a distinct default session remains logged in.
func TestLogoutSelectsCustomSession(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	handle := session.NewHandle()
	selectedLogin := newGuard(t, func(policy *auth.GuardBuilder) {
		policy.Handle(handle)
	})
	defaultLogin := newGuard(t)
	selected, defaultSession := newSession(), newSession()

	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	ctx := session.NewContext(r.Context(), defaultSession)
	r = r.WithContext(session.NewContextFor(ctx, handle, selected))

	if _, err := selectedLogin.Begin(r, "selected", federated); err != nil {
		t.Fatal(err)
	}

	if _, err := defaultLogin.Begin(r, "default", federated); err != nil {
		t.Fatal(err)
	}

	id := selected.ID()
	defaultID := defaultSession.ID()

	if response := client.Logout(selectedLogin).Record(r); response.StatusCode != http.StatusFound {
		t.Fatal(response.StatusCode)
	}

	if _, err := userOf(selectedLogin, r); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatal(err)
	}

	if got, err := userOf(defaultLogin, r); err != nil || got != "default" {
		t.Fatalf("default user = %q, error = %v", got, err)
	}

	if selected.ID() == id || defaultSession.ID() != defaultID {
		t.Fatal("logout rotated the wrong session")
	}
}
