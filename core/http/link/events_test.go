package link_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/http/router"
)

// TestBindingErrorPreservesTypedCause verifies removing raw event errors does
// not redact or replace the returned problem's error chain for application code.
func TestBindingErrorPreservesTypedCause(t *testing.T) {
	t.Parallel()

	cause := &url.Error{
		Op:  "lookup",
		URL: "https://private.example/?token=private-token",
		Err: errors.New("lookup unavailable"),
	}
	signer := mustNewSigner(t)
	handler := link.Middleware(signer, func(policy *link.MiddlewareBuilder) {
		policy.BindRequest(func(*http.Request) ([]byte, error) {
			return nil, cause
		})
	})(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("binding failure reached protected handler")

		return nil
	})

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/reset", nil))

	if !errors.Is(err, cause) {
		t.Fatalf("error lost its original cause: %v", err)
	}

	value, ok := errors.AsType[*url.Error](err)

	if !ok || value != cause {
		t.Fatalf("typed cause = %v; want original URL error", value)
	}
}

// TestRejectedEventOmitsBindingCause checks that arbitrary application errors
// stay on the returned problem, not in automatic events or the HTTP response.
func TestRejectedEventOmitsBindingCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got []link.RejectedEvent

	if _, err := bus.Listen(link.Rejected, func(_ context.Context, payload link.RejectedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	signer := mustNewSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	cause := errors.New("lookup https://private.example/reset/private-token?signature=private-signature")
	handler := link.Middleware(signer, func(policy *link.MiddlewareBuilder) {
		policy.BindRequest(func(*http.Request) ([]byte, error) {
			return nil, cause
		})
	})(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("rejected binding reached protected handler")

		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/reset/private-token?signature=private-signature", nil)

	if err := handler(httptest.NewRecorder(), request); !errors.Is(err, cause) {
		t.Fatalf("returned error lost binding cause: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("events = %d; want one", len(got))
	}

	if text := fmt.Sprintf("%+v", got[0]); text != "{Method:GET}" {
		t.Fatalf("event exposed request or cause: %s", text)
	}

	response := router.Handler(handler).Record(request)
	t.Cleanup(func() {
		response.Body.Close()
	})

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", response.StatusCode)
	}

	body := readBody(t, response)

	if strings.Contains(body, "private.example") || strings.Contains(body, "private-signature") {
		t.Fatalf("response exposed binding cause: %s", body)
	}

	if !strings.Contains(body, `"instance":"/reset/private-token"`) {
		t.Fatalf("native problem instance changed: %s", body)
	}
}

// TestMiddlewarePublishesRejected verifies a tampered link is published
// without its path, query, or verification cause.
func TestMiddlewarePublishesRejected(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got []link.RejectedEvent

	if _, err := bus.Listen(link.Rejected, func(_ context.Context, payload link.RejectedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	signer := mustNewSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	signedURL := mustSign(t, signer, "/unsubscribe/user_123")
	response, _ := serve(signer, strings.Replace(signedURL, "user_123", "user_456", 1))
	t.Cleanup(func() {
		response.Body.Close()
	})

	if len(got) != 1 || got[0] != (link.RejectedEvent{Method: http.MethodGet}) {
		t.Fatalf("Rejected = %+v", got)
	}
}

// TestMiddlewarePublishesNothingForValidLink verifies an accepted link is not
// published.
func TestMiddlewarePublishesNothingForValidLink(t *testing.T) {
	t.Parallel()

	bus := event.New()
	published := false

	if _, err := bus.Listen(link.Rejected, func(context.Context, link.RejectedEvent) error {
		published = true

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	signer := mustNewSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	response, called := serve(signer, mustSign(t, signer, "/unsubscribe/user_123"))
	t.Cleanup(func() {
		response.Body.Close()
	})

	if !called || published {
		t.Fatalf("called = %t, published = %t, want a call and no event", called, published)
	}
}

// listenConsumed returns a bus and the slice its Consumed payloads are
// appended to.
func listenConsumed(t *testing.T) (*event.Bus, *[]link.ConsumedEvent) {
	t.Helper()

	bus := event.New()
	var got []link.ConsumedEvent

	if _, err := bus.Listen(link.Consumed, func(_ context.Context, payload link.ConsumedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return bus, &got
}

// TestConsumePublishesConsumedWithoutIdentity verifies spending a link does not
// publish its resource path or query, even for calls without an HTTP request.
func TestConsumePublishesConsumedWithoutIdentity(t *testing.T) {
	t.Parallel()

	bus, got := listenConsumed(t)
	signer := newCachedSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	signed := mustTemporary(t, signer, "/reset/private-identity?token=private-token")

	if err := signer.Consume(t.Context(), signed); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 1 || (*got)[0] != (link.ConsumedEvent{}) {
		t.Fatalf("Consumed = %+v, want no request metadata", *got)
	}
}

// TestConsumeRequestPublishesConsumedWithMethod verifies a request-based
// consume adds the request method.
func TestConsumeRequestPublishesConsumedWithMethod(t *testing.T) {
	t.Parallel()

	bus, got := listenConsumed(t)
	signer := newCachedSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	request := httptest.NewRequest(http.MethodPost, mustTemporary(t, signer, "/reset/user_123"), nil)

	if err := signer.ConsumeRequest(request); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 1 || (*got)[0] != (link.ConsumedEvent{
		Method: http.MethodPost,
	}) {
		t.Fatalf("Consumed = %+v", *got)
	}
}

// TestConsumeRepeatPublishesNothing verifies only the call that spends the
// link publishes.
func TestConsumeRepeatPublishesNothing(t *testing.T) {
	t.Parallel()

	bus, got := listenConsumed(t)
	signer := newCachedSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	signed := mustTemporary(t, signer, "/reset/user_123")

	_ = signer.Consume(t.Context(), signed)

	if err := signer.Consume(t.Context(), signed); !errors.Is(err, link.ErrConsumed) {
		t.Fatalf("second Consume() error = %v, want ErrConsumed", err)
	}

	if len(*got) != 1 {
		t.Fatalf("Consumed published %d times, want 1", len(*got))
	}
}

// TestConsumeForgedLinkPublishesNothing verifies a failed consume is silent.
func TestConsumeForgedLinkPublishesNothing(t *testing.T) {
	t.Parallel()

	bus, got := listenConsumed(t)
	signer := newCachedSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	signed := mustTemporary(t, signer, "/reset/user_123")

	if err := signer.Consume(t.Context(), strings.Replace(signed, "user_123", "user_456", 1)); err == nil {
		t.Fatal("Consume() of a forged link succeeded")
	}

	if len(*got) != 0 {
		t.Fatalf("Consumed = %+v, want none", *got)
	}
}

// TestVerifyAndInspectPublishNothing verifies the read-only calls stay silent
// and the middleware never consumes.
func TestVerifyAndInspectPublishNothing(t *testing.T) {
	t.Parallel()

	bus, got := listenConsumed(t)
	signer := newCachedSigner(t, func(signer *link.Builder) {
		signer.Events(bus)
	})

	signed := mustTemporary(t, signer, "/reset/user_123")

	if err := signer.Verify(signed); err != nil {
		t.Fatal(err)
	}

	if _, err := signer.Inspect(t.Context(), signed); err != nil {
		t.Fatal(err)
	}

	response, _ := serveWith(signer, signed)
	t.Cleanup(func() {
		response.Body.Close()
	})

	if len(*got) != 0 {
		t.Fatalf("Consumed = %+v, want none", *got)
	}
}

// TestConsumeWithoutBusSucceeds verifies a signer without a bus still consumes.
func TestConsumeWithoutBusSucceeds(t *testing.T) {
	t.Parallel()

	signer := newCachedSigner(t)

	if err := signer.Consume(t.Context(), mustTemporary(t, signer, "/reset/user_123")); err != nil {
		t.Fatal(err)
	}
}
