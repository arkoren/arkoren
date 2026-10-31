package client_test

import (
	"context"
	"net/http"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/client"
)

// failedRequests returns a bus and the slice its RequestFailed payloads are
// appended to.
func failedRequests(t *testing.T) (*event.Bus, *[]client.RequestFailedEvent) {
	t.Helper()

	bus := event.New()
	var failed []client.RequestFailedEvent

	if _, err := bus.Listen(client.RequestFailed, func(_ context.Context, payload client.RequestFailedEvent) error {
		failed = append(failed, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return bus, &failed
}

// TestTransportPublishesRequestFailedWhenAttemptsRunOut checks that exhausting
// retries publishes one terminal failure event with the completed attempt
// count.
func TestTransportPublishesRequestFailedWhenAttemptsRunOut(t *testing.T) {
	t.Parallel()

	server, _ := sequence(t, nil, http.StatusServiceUnavailable)
	bus, failed := failedRequests(t)
	transport := client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)
	}, func(transport *client.Builder) {
		transport.Events(bus)
	})

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := do(t, transport, request); err != nil {
		t.Fatal(err)
	}

	if len(*failed) != 1 {
		t.Fatalf("failed events = %d, want one", len(*failed))
	}

	if (*failed)[0].Attempts != 3 || (*failed)[0].StatusCode != http.StatusServiceUnavailable ||
		(*failed)[0].Method != http.MethodGet {
		t.Fatalf("RequestFailed = %+v, want one after 3 attempts", *failed)
	}
}

// TestTransportPublishesNothingOnSuccess checks that a completed successful
// request produces no terminal-failure event.
func TestTransportPublishesNothingOnSuccess(t *testing.T) {
	t.Parallel()

	server, _ := sequence(t, nil, http.StatusServiceUnavailable, http.StatusOK)
	bus, failed := failedRequests(t)
	transport := client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)
	}, func(transport *client.Builder) {
		transport.Events(bus)
	})

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := do(t, transport, request); err != nil {
		t.Fatal(err)
	}

	if len(*failed) != 0 {
		t.Fatalf("RequestFailed = %+v, want none after a retried success", *failed)
	}
}
