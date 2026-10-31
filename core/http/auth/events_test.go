package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/auth"
	"arkoren.dev/foundation/problem"
)

// listenRejected returns the payloads published on bus so far.
func listenRejected(t *testing.T, bus *event.Bus) *[]auth.RejectedEvent {
	t.Helper()

	var got []auth.RejectedEvent

	if _, err := bus.Listen(auth.Rejected, func(_ context.Context, payload auth.RejectedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return &got
}

// TestRequirePublishesRejected checks that anonymous rejection emits the
// request method without leaking path identifiers or its query string.
func TestRequirePublishesRejected(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)
	var seen []string

	response := status(auth.Require[user](func(authenticator *auth.Builder) {
		authenticator.Events(bus)
	})(ok(&seen)), httptest.NewRequest(http.MethodPost, "/admin/private-identity?token=secret", nil))

	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}

	if len(*got) != 1 || (*got)[0] != (auth.RejectedEvent{
		Method: http.MethodPost,
	}) {
		t.Fatalf("Rejected = %+v", *got)
	}
}

// TestRequireRejectedEventOmitsRequestIdentity checks the public event payload,
// not the handler internals, for identifiers embedded in a rejected URL.
func TestRequireRejectedEventOmitsRequestIdentity(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)
	var seen []string
	handler := auth.Require[user](func(authenticator *auth.Builder) {
		authenticator.Events(bus)
	})(ok(&seen))
	status(handler, httptest.NewRequest(http.MethodGet, "/reset/private-token?signature=private-signature", nil))

	if len(*got) != 1 {
		t.Fatalf("events = %d; want one", len(*got))
	}

	if text := fmt.Sprintf("%+v", (*got)[0]); text != "{Method:GET}" {
		t.Fatalf("event exposed request metadata: %s", text)
	}
}

// TestRequireCustomStatusStillPublishesRejected verifies event publication is
// about anonymous admission, not a hard-coded 401 or a forced Bearer challenge.
func TestRequireCustomStatusStillPublishesRejected(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)
	var seen []string
	handler := auth.Require[user](func(authenticator *auth.Builder) {
		authenticator.Events(bus)
		authenticator.ErrorResponse(problem.Forbidden())
	})(ok(&seen))

	response := status(handler, httptest.NewRequest(http.MethodGet, "/private", nil))

	if response.StatusCode != http.StatusForbidden || response.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("status = %d, challenge = %q", response.StatusCode, response.Header.Get("WWW-Authenticate"))
	}

	if len(*got) != 1 || len(seen) != 0 {
		t.Fatalf("events = %d, protected calls = %v", len(*got), seen)
	}
}

// TestRequirePublishesEveryRejection checks that repeated anonymous requests
// each emit an event rather than suppressing later rejections.
func TestRequirePublishesEveryRejection(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)
	var seen []string
	handler := auth.Require[user](func(authenticator *auth.Builder) {
		authenticator.Events(bus)
	})(ok(&seen))

	for range 3 {
		status(handler, httptest.NewRequest(http.MethodGet, "/", nil))
	}

	if len(*got) != 3 {
		t.Fatalf("Rejected = %d events; want 3", len(*got))
	}
}

// TestRequirePublishesNothingForUser checks that an authenticated request
// reaches the handler without emitting a rejection event.
func TestRequirePublishesNothingForUser(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)
	var seen []string
	handler := auth.Middleware(returns("a", nil))(auth.Require[user](func(authenticator *auth.Builder) {
		authenticator.Events(bus)
	})(ok(&seen)))

	status(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if len(*got) != 0 {
		t.Fatalf("Rejected = %+v; want none", *got)
	}
}

// TestRequireWithoutBusPublishesNothing checks that rejecting an anonymous
// request does not implicitly publish to an unrelated bus.
func TestRequireWithoutBusPublishesNothing(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)
	var seen []string

	status(auth.Require[user]()(ok(&seen)), httptest.NewRequest(http.MethodGet, "/", nil))

	if len(*got) != 0 {
		t.Fatalf("Rejected = %+v; want none", *got)
	}
}
