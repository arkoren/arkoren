package oauthauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/session"
)

// TestLogoutClearsLoginAndRedirects verifies the Logout handler.
func TestLogoutClearsLoginAndRedirects(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	sess := newSession()

	if err := localIdentity.Login(sess, "user-1"); err != nil {
		t.Fatal(err)
	}

	request := callbackRequest(http.MethodPost, cookieJar{}, sess, "", "")
	response := client.Logout(localIdentity).Record(request)

	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/" {
		t.Errorf("response = %d %q", response.StatusCode, response.Header.Get("Location"))
	}

	if _, err := userOf(request); !errors.Is(err, auth.ErrNoCredentials) {
		t.Errorf("Session() error = %v, want ErrNoCredentials", err)
	}
}

// bridgeContext counts successful session lookups while retaining all ordinary
// context behavior used by OAuth's local HTTP exchanges.
type bridgeContext struct {
	// Context delegates cancellation, deadlines and ordinary context values.
	context.Context

	// lookups is shared with the test and safe for HTTP client goroutines.
	lookups *atomic.Int64
}

// Value observes successful bridge lookups without interpreting private keys or
// changing the value returned by the underlying context.
func (ctx bridgeContext) Value(key any) any {
	value := ctx.Context.Value(key)

	if _, ok := value.(*session.Session); ok {
		ctx.lookups.Add(1)
	}

	return value
}

// TestCallbackInvalidIdentityHasNoEffects verifies configuration failure wins
// before state consumption, provider exchange, mapper, events or rotation.
func TestCallbackInvalidIdentityHasNoEffects(t *testing.T) {
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

	err := client.Callback(auth.SessionIdentity[string, string]{}, func(context.Context, Identity) (string, error) {
		t.Fatal("invalid definition reached mapper")

		return "", nil
	})(w, r)

	if !errors.Is(err, auth.ErrInvalidSessionIdentity) {
		t.Fatal(err)
	}

	if current.ID() != id || current.HasChanged() || len(w.Header()) != 0 {
		t.Fatal("invalid identity caused rotation, cookie consumption or redirect")
	}

	if provider.exchanges.Load() != 0 || len(*events) != 0 {
		t.Fatal("invalid identity exchanged a code or published an event")
	}

	if got := client.Callback(localIdentity, idLogin).Record(r).StatusCode; got != http.StatusFound {
		t.Fatalf("invalid attempt consumed state: %d", got)
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
	jar := cookieJar{}

	query := login(t, client, jar)

	current := newSession()
	current.MarkAsUnchanged()
	id := current.ID()
	r := callbackRequest(http.MethodGet, jar, current, query.Get("state"), query.Get("nonce"))
	w := httptest.NewRecorder()

	err := client.Callback(localIdentity, nil)(w, r)

	if err == nil || errors.Is(err, ErrInvalidCallback) || errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("nil mapper error = %v", err)
	}

	if current.ID() != id || current.HasChanged() || len(w.Header()) != 0 {
		t.Fatal("nil mapper caused side effects")
	}

	if provider.exchanges.Load() != 0 || len(*events) != 0 {
		t.Fatal("nil mapper exchanged a code or published an event")
	}

	if got := client.Callback(localIdentity, idLogin).Record(r).StatusCode; got != http.StatusFound {
		t.Fatalf("nil mapper consumed state: %d", got)
	}
}

// TestCallbackMissingSelectedSessionDoesNotFallback verifies default sessions
// cannot satisfy a custom selection and rejection leaves state unconsumed.
func TestCallbackMissingSelectedSessionDoesNotFallback(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	jar := cookieJar{}

	query := login(t, client, jar)

	handle := session.NewHandle()
	identity := auth.NewSessionIdentity(func(_ context.Context, id string) (string, error) {
		return id, nil
	}, func(authenticator *auth.SessionBuilder) {
		authenticator.Handle(handle)
	})
	r := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce"))
	w := httptest.NewRecorder()

	err := client.Callback(identity, idLogin)(w, r)

	if !errors.Is(err, auth.ErrUnauthenticated) || len(w.Header()) != 0 {
		t.Fatalf("missing selection: error = %v, headers = %v", err, w.Header())
	}

	r = r.WithContext(session.NewContextFor(r.Context(), handle, newSession()))

	if got := client.Callback(identity, idLogin).Record(r).StatusCode; got != http.StatusFound {
		t.Fatalf("missing selection consumed state: %d", got)
	}
}

// TestLogoutSelectsCustomSession verifies only the selected session loses its
// login and rotates, while a distinct default session remains logged in.
func TestLogoutSelectsCustomSession(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	handle := session.NewHandle()
	identity := auth.NewSessionIdentity(func(_ context.Context, id string) (string, error) {
		return id, nil
	}, func(authenticator *auth.SessionBuilder) {
		authenticator.Handle(handle)
	})
	selected, defaultSession := newSession(), newSession()

	if err := identity.Login(selected, "selected"); err != nil {
		t.Fatal(err)
	}

	if err := localIdentity.Login(defaultSession, "default"); err != nil {
		t.Fatal(err)
	}

	id := selected.ID()
	defaultID := defaultSession.ID()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	ctx := session.NewContext(r.Context(), defaultSession)
	r = r.WithContext(session.NewContextFor(ctx, handle, selected))
	var lookups atomic.Int64
	r = r.WithContext(bridgeContext{Context: r.Context(), lookups: &lookups})

	if response := client.Logout(identity).Record(r); response.StatusCode != http.StatusFound {
		t.Fatal(response.StatusCode)
	}

	if lookups.Load() != 1 {
		t.Fatalf("logout resolved session %d times, want once", lookups.Load())
	}

	if _, err := identity.Authenticator()(r); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatal(err)
	}

	if got, err := userOf(r); err != nil || got != "default" {
		t.Fatalf("default user = %q, error = %v", got, err)
	}

	if selected.ID() == id || defaultSession.ID() != defaultID {
		t.Fatal("logout rotated the wrong session")
	}
}

// TestCallbackMapperTypeDoesNotCompile verifies OAuth cannot map a string key
// into a definition whose loader and login use integers.
func TestCallbackMapperTypeDoesNotCompile(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "go", "test", "./testdata/identity")
	output, err := command.CombinedOutput()

	if err == nil || !strings.Contains(string(output), "does not match inferred type") {
		t.Fatalf("wrong mapper compilation: %v\n%s", err, output)
	}
}

// TestLogoutInvalidIdentityHasNoEffects verifies definition errors precede
// lookup, redirect and rotation even when a usable default session is present.
func TestLogoutInvalidIdentityHasNoEffects(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	current := newSession()
	current.MarkAsUnchanged()
	id := current.ID()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r = r.WithContext(session.NewContext(r.Context(), current))
	var lookups atomic.Int64
	r = r.WithContext(bridgeContext{Context: r.Context(), lookups: &lookups})
	w := httptest.NewRecorder()

	err := client.Logout(auth.SessionIdentity[string, string]{})(w, r)

	if !errors.Is(err, auth.ErrInvalidSessionIdentity) || lookups.Load() != 0 {
		t.Fatalf("error = %v, lookups = %d", err, lookups.Load())
	}

	if current.ID() != id || current.HasChanged() || len(w.Header()) != 0 {
		t.Fatal("invalid logout mutated session or response")
	}
}
