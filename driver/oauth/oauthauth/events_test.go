package oauthauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/event"
)

// recordEvents returns a bus and the topic names it receives in order.
func recordEvents(t *testing.T) (*event.Bus, *[]string) {
	t.Helper()

	bus := event.New()
	var seen []string

	if _, err := bus.Listen(LoginSucceeded, func(_ context.Context, payload LoginSucceededEvent) error {
		seen = append(seen, "succeeded:"+payload.Subject)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Listen(LoginFailed, func(_ context.Context, payload LoginFailedEvent) error {
		if !errors.Is(payload.Err, ErrInvalidCallback) {
			t.Errorf("LoginFailed error = %v, want ErrInvalidCallback", payload.Err)
		}

		seen = append(seen, "failed")

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Listen(TokenRejected, func(context.Context, TokenRejectedEvent) error {
		seen = append(seen, "token")

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return bus, &seen
}

// TestCallbackPublishesLoginEvents verifies a login publishes LoginSucceeded
// and its replay publishes LoginFailed.
func TestCallbackPublishesLoginEvents(t *testing.T) {
	t.Parallel()

	bus, seen := recordEvents(t)
	client := newClient(t, OIDC(newTestProvider(t).server.URL), func(client *ClientBuilder) {
		client.Events(bus)
	})
	jar := cookieJar{}

	query := login(t, client, jar)

	response := client.Callback(localIdentity, idLogin).Record(callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce")))

	if response.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", response.StatusCode)
	}

	jar.apply(response)
	client.Callback(localIdentity, idLogin).Record(callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce")))

	if len(*seen) != 2 || (*seen)[0] != "succeeded:user-1" || (*seen)[1] != "failed" {
		t.Fatalf("events = %v, want a success then a failure", *seen)
	}
}

// TestVerifyPublishesTokenRejected verifies a bad bearer token is published.
func TestVerifyPublishesTokenRejected(t *testing.T) {
	t.Parallel()

	bus, seen := recordEvents(t)
	client := newClient(t, OIDC(newTestProvider(t).server.URL), func(client *ClientBuilder) {
		client.Events(bus)
	})

	if _, err := client.Verify(t.Context(), "not-a-token"); err == nil {
		t.Fatal("Verify accepted a malformed token")
	}

	if len(*seen) != 1 || (*seen)[0] != "token" {
		t.Fatalf("events = %v, want one token rejection", *seen)
	}
}

// TestLoginFailedDoesNotPublishMapperCause keeps sensitive application errors
// out of observations while preserving the original cause for the caller.
func TestLoginFailedDoesNotPublishMapperCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var observed LoginFailedEvent

	if _, err := bus.Listen(LoginFailed, func(_ context.Context, value LoginFailedEvent) error {
		observed = value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	client := newClient(t, OIDC(newTestProvider(t).server.URL), func(client *ClientBuilder) {
		client.Events(bus)
	})
	jar := cookieJar{}

	query := login(t, client, jar)

	request := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce"))
	cause := errors.New("synthetic-private-mapper-detail")

	err := client.Callback(localIdentity, func(context.Context, Identity) (string, error) {
		return "", cause
	})(httptest.NewRecorder(), request)

	if !errors.Is(err, cause) {
		t.Fatalf("returned mapper cause lost: %v", err)
	}

	if errors.Is(observed.Err, cause) || strings.Contains(fmt.Sprint(observed), cause.Error()) {
		t.Fatalf("event leaked mapper cause: %v", observed)
	}
}

// TestLoginFailedBoundsProviderError prevents arbitrary callback query content
// from becoming an event field, even when it looks like a provider error hint.
func TestLoginFailedBoundsProviderError(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var observed LoginFailedEvent

	if _, err := bus.Listen(LoginFailed, func(_ context.Context, value LoginFailedEvent) error {
		observed = value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	client := newClient(t, GitHub(), func(client *ClientBuilder) {
		client.Events(bus)
	})
	jar := cookieJar{}

	query := login(t, client, jar)

	request := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), "unused")
	parameters := request.URL.Query()
	parameters.Set("error", "synthetic-private-request-content")
	request.URL.RawQuery = parameters.Encode()

	err := client.Callback(localIdentity, idLogin)(httptest.NewRecorder(), request)

	if !errors.Is(err, ErrInvalidCallback) || observed.ProviderError != "" {
		t.Fatalf("untrusted provider hint published: %+v, %v", observed, err)
	}
}

// TestLoginFailedKeepsStandardProviderHint preserves the bounded RFC callback
// vocabulary while reporting a provider-denied login as an invalid callback.
func TestLoginFailedKeepsStandardProviderHint(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var observed LoginFailedEvent

	if _, err := bus.Listen(LoginFailed, func(_ context.Context, value LoginFailedEvent) error {
		observed = value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	client := newClient(t, GitHub(), func(client *ClientBuilder) {
		client.Events(bus)
	})
	jar := cookieJar{}

	query := login(t, client, jar)

	request := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), "unused")
	parameters := request.URL.Query()
	parameters.Set("error", "access_denied")
	request.URL.RawQuery = parameters.Encode()

	err := client.Callback(localIdentity, idLogin)(httptest.NewRecorder(), request)

	if !errors.Is(err, ErrInvalidCallback) || observed.ProviderError != "access_denied" {
		t.Fatalf("standard provider hint lost: %+v, %v", observed, err)
	}
}

// TestTokenRejectedDoesNotPublishClaims keeps claim-derived native diagnostics
// on the returned error only, not in an event consumed by ordinary logging.
func TestTokenRejectedDoesNotPublishClaims(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var observed TokenRejectedEvent

	if _, err := bus.Listen(TokenRejected, func(_ context.Context, value TokenRejectedEvent) error {
		observed = value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Events(bus)
	})

	_, err := client.Verify(t.Context(), provider.sign(map[string]any{"iss": "synthetic-private-issuer"}))

	if err == nil || !strings.Contains(err.Error(), "synthetic-private-issuer") {
		t.Fatalf("original native diagnostic lost: %v", err)
	}

	if observed.Err == nil || strings.Contains(fmt.Sprint(observed), "synthetic-private-issuer") {
		t.Fatalf("event leaked native diagnostic: %v", observed)
	}
}
