package contenttype_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/middleware/contenttype"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ExampleRejected observes rejection notifications even when the application
// deliberately chooses a different HTTP response status.
func ExampleRejected() {
	events := event.New()

	unsubscribe, err := events.Listen(contenttype.Rejected,
		func(_ context.Context, notification contenttype.RejectedEvent) error {
			fmt.Println(notification.Method, notification.MediaType)

			return nil
		})

	if err != nil {
		fmt.Println(err)

		return
	}

	defer unsubscribe()
	handler := router.Handler(contenttype.Middleware(func(content *contenttype.Builder) {
		content.Events(events)
		content.ErrorResponse(problem.Forbidden())
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	}))

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("data"))
	request.Header.Set("Content-Type", "text/plain")

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)
	fmt.Println(recorder.Code)
	// Output:
	// POST text/plain
	// 403
}

// listenRejected returns the payloads published on bus so far.
func listenRejected(t *testing.T, bus *event.Bus) *[]contenttype.RejectedEvent {
	t.Helper()

	var got []contenttype.RejectedEvent

	if _, err := bus.Listen(contenttype.Rejected, func(_ context.Context, payload contenttype.RejectedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return &got
}

// send runs one request with the given Content-Type through the middleware.
func send(path, contentType string, opts ...func(*contenttype.Builder)) {
	handler := router.Handler(contenttype.Middleware(opts...)(func(http.ResponseWriter, *http.Request) error {
		return nil
	}))

	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("a=b"))

	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	handler.Record(request)
}

// TestMiddlewarePublishesRejectedWithoutParameters checks that rejection
// events normalize the media type and omit both URL queries and Content-Type
// parameters.
func TestMiddlewarePublishesRejectedWithoutParameters(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)

	send("/submit?token=secret", "Text/Plain; charset=utf-8", func(content *contenttype.Builder) {
		content.Events(bus)
	})

	want := contenttype.RejectedEvent{
		Method:    http.MethodPost,
		Path:      "/submit",
		MediaType: "text/plain",
	}

	if len(*got) != 1 || (*got)[0] != want {
		t.Fatalf("Rejected = %+v; want %+v", *got, want)
	}
}

// TestMiddlewarePublishesRejectedWithEmptyMediaTypeWhenMissing checks that a
// missing Content-Type is represented by an empty media type in the rejection
// event.
func TestMiddlewarePublishesRejectedWithEmptyMediaTypeWhenMissing(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)

	send("/", "", func(content *contenttype.Builder) {
		content.Events(bus)
	})

	if len(*got) != 1 || (*got)[0].MediaType != "" {
		t.Fatalf("Rejected = %+v; want one event with an empty media type", *got)
	}
}

// TestMiddlewarePublishesEveryRejection checks that each disallowed request
// emits its own rejection event rather than deduplicating requests.
func TestMiddlewarePublishesEveryRejection(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)

	for range 3 {
		send("/", "text/plain", func(content *contenttype.Builder) {
			content.Events(bus)
		})
	}

	if len(*got) != 3 {
		t.Fatalf("Rejected = %d events; want 3", len(*got))
	}
}

// TestMiddlewarePublishesNothingForAllowedType checks that an allowed media
// type passes without emitting a rejection event.
func TestMiddlewarePublishesNothingForAllowedType(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenRejected(t, bus)

	send("/", "application/json", func(content *contenttype.Builder) {
		content.Events(bus)
	})

	if len(*got) != 0 {
		t.Fatalf("Rejected = %+v; want none", *got)
	}
}

// TestMiddlewareWithoutBusPublishesNothing checks that middleware without an
// explicit bus does not publish rejections to an unrelated bus.
func TestMiddlewareWithoutBusPublishesNothing(t *testing.T) {
	t.Parallel()

	got := listenRejected(t, event.New())

	send("/", "text/plain")

	if len(*got) != 0 {
		t.Fatalf("Rejected = %+v; want none", *got)
	}
}
