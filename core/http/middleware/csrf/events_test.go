package csrf_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/middleware/csrf"
	"arkoren.dev/core/http/router"
)

// TestMiddlewarePublishesBlocked verifies a rejected request is published.
func TestMiddlewarePublishesBlocked(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got []csrf.BlockedEvent

	if _, err := bus.Listen(csrf.Blocked, func(_ context.Context, payload csrf.BlockedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := csrf.Middleware(func(protection *csrf.Builder) {
		protection.Events(bus)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "/transfer?amount=1", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Origin", "https://evil.example")
	router.Handler(handler).Record(request)

	if len(got) != 1 || got[0].Method != http.MethodPost || got[0].Path != "/transfer" || got[0].Origin != "https://evil.example" || got[0].Err == nil {
		t.Fatalf("Blocked = %+v", got)
	}
}

// TestMiddlewarePublishesNothingForSafeRequest verifies allowed requests are
// not published.
func TestMiddlewarePublishesNothingForSafeRequest(t *testing.T) {
	t.Parallel()

	bus := event.New()
	published := false

	if _, err := bus.Listen(csrf.Blocked, func(context.Context, csrf.BlockedEvent) error {
		published = true

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := csrf.Middleware(func(protection *csrf.Builder) {
		protection.Events(bus)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})
	router.Handler(handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if published {
		t.Fatal("Blocked published for a safe request")
	}
}
