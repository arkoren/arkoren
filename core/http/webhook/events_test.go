package webhook_test

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/webhook"
)

// assertEventJSON checks the complete serialized payload, including field names
// and empty values. Literal expectations keep category bytes independent of the
// constants used by the receiver and typed listener assertions.
func assertEventJSON(t *testing.T, payload any, want string) {
	t.Helper()

	data, err := json.Marshal(payload)

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != want {
		t.Fatalf("event JSON = %s, want %s", data, want)
	}
}

// TestRejectedEventZeroJSON preserves an unspecified reason as an empty string;
// serialization must not invent a category for a manually constructed payload.
func TestRejectedEventZeroJSON(t *testing.T) {
	t.Parallel()

	assertEventJSON(t, webhook.RejectedEvent{}, `{"Label":"","Reason":"","Status":0}`)
}

// TestFailedEventZeroJSON keeps the absent stage empty, with no implicit default
// or change to the existing event field names.
func TestFailedEventZeroJSON(t *testing.T) {
	t.Parallel()

	assertEventJSON(t, webhook.FailedEvent{}, `{"Label":"","Stage":""}`)
}

// TestIgnoredEventZeroJSON keeps the zero reason unspecified, rather than
// silently treating every manually constructed event as an unbound delivery.
func TestIgnoredEventZeroJSON(t *testing.T) {
	t.Parallel()

	assertEventJSON(t, webhook.IgnoredEvent{}, `{"Label":"","Reason":""}`)
}

// TestHandlerPublishesReadFailureStage observes an interrupted body read through
// the public HTTP/event boundary, preserving its 503 and "read" classification.
func TestHandlerPublishesReadFailureStage(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var failed []webhook.FailedEvent

	if _, err := bus.Listen(webhook.Failed, func(_ context.Context, payload webhook.FailedEvent) error {
		failed = append(failed, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := newHandler(t, headerSource(), bindEcho(&recorder{}, func(receiver *webhook.Builder[string]) {
		receiver.Events(bus).Label("orders")
	}))

	req := httptest.NewRequest(http.MethodPost, "/hook", failingReader{err: timeoutError{}})

	if status := handler.Record(req).StatusCode; status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}

	if len(failed) != 1 || failed[0].Stage != webhook.FailureStageRead {
		t.Fatalf("failed = %+v, want one read failure", failed)
	}

	assertEventJSON(t, failed, `[{"Label":"orders","Stage":"read"}]`)
}
