package natsbroadcast_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/driver/nats/natsbroadcast"

	"github.com/nats-io/nats.go"
)

// TestNATSConsumerRejectsEnvelopeSubjectMismatch exercises the real broker
// boundary with metrics enabled and tracing disabled. A producer authorized for
// one subject must not manufacture arbitrary core routing or metric identities.
func TestNATSConsumerRejectsEnvelopeSubjectMismatch(t *testing.T) {
	t.Parallel()

	exerciseEnvelopeBinding(t, "#", "security")
}

// TestExactListenerDoesNotCreateUnknownDestinationSeries verifies rejected
// names never reach core instrumentation, which runs before listener matching.
// No tracing or scraping is needed to reach the metric recording path.
func TestExactListenerDoesNotCreateUnknownDestinationSeries(t *testing.T) {
	t.Parallel()

	exerciseEnvelopeBinding(t, "users.created", "")
}

// exerciseEnvelopeBinding sends mismatched and valid envelopes through an actual
// NATS subscription and instrumented core consumer. The trailing valid delivery
// is a barrier for earlier callbacks, allowing rejection assertions without sleeps.
func exerciseEnvelopeBinding(t *testing.T, pattern, queueGroup string) {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test")
	}

	conn, err := nats.Connect(natsTestURL)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(conn.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	prefix := fmt.Sprintf("security.%s", nats.NewInbox())
	driver, err := natsbroadcast.NewFrom(conn, func(broker *natsbroadcast.Builder) {
		broker.Prefix(prefix)
		broker.QueueGroup(queueGroup)
	})

	if err != nil {
		t.Fatal(err)
	}

	metrics := memorytelemetry.New()
	facade := telemetry.New(telemetry.Compose(telemetry.Metrics(metrics)))

	if err := facade.Start(ctx, telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = facade.Stop(context.Background())
	})
	consumer := broadcast.NewConsumer(driver, func(consumer *broadcast.ConsumerBuilder) {
		consumer.Telemetry(facade)
	})
	received := make(chan broadcast.Message, 8)

	if _, err := consumer.ListenPattern(pattern, func(_ context.Context, message broadcast.Message) error {
		received <- message

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		done <- consumer.Run(ctx)
	})
	t.Cleanup(func() {
		cancel()
		group.Wait()
	})

	for conn.NumSubscriptions() == 0 && ctx.Err() == nil {
		runtime.Gosched()
	}

	if err := conn.FlushWithContext(ctx); err != nil {
		t.Fatal(err)
	}

	for index := range 3 {
		body := fmt.Sprintf(`{"name":"x%d","payload":null}`, index)

		if err := conn.Publish(prefix+".users.created", []byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := conn.Publish(prefix+".nested.users.created", []byte(`{"name":"users.created","payload":null}`)); err != nil {
		t.Fatal(err)
	}

	if err := conn.Publish(prefix+".users.created", []byte(`{"name":`)); err != nil {
		t.Fatal(err)
	}

	want := broadcast.Message{
		Name:     "users.created",
		ID:       "kept",
		Payload:  []byte(`{"id":7}`),
		Metadata: map[string]string{"source": "test"},
	}
	body, err := json.Marshal(want)

	if err != nil {
		t.Fatal(err)
	}

	if err := conn.Publish(prefix+".users.created", body); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-received:
		if got.Name != want.Name || got.ID != want.ID || string(got.Payload) != string(want.Payload) || got.Metadata["source"] != "test" {
			t.Fatalf("received %+v, want %+v", got, want)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	// A second callback cannot run until core has finished recording the first.
	if err := conn.Publish(prefix+".users.created", body); err != nil {
		t.Fatal(err)
	}

	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}

	found := false

	for _, metric := range metrics.Metrics() {
		for _, point := range metric.Points {
			for _, value := range point.Values {
				if value == "users.created" {
					found = true
				}

				if value == "x0" || value == "x1" || value == "x2" {
					t.Fatalf("forged destination retained in metrics: %+v", metric)
				}
			}
		}
	}

	if !found {
		t.Fatal("valid destination missing from metrics-only consumer")
	}
}

// TestRawSubscribePreservesWildcardPayloads verifies the low-level API still
// accepts non-envelope bytes and portable final-# patterns match both the root
// topic and deeper deliveries. Envelope identity checks belong only to Consume.
func TestRawSubscribePreservesWildcardPayloads(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	conn, err := nats.Connect(natsTestURL)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(conn.Close)
	prefix := nats.NewInbox()
	driver, err := natsbroadcast.NewFrom(conn, func(broker *natsbroadcast.Builder) {
		broker.Prefix(prefix)
	})

	if err != nil {
		t.Fatal(err)
	}

	received := make(chan string, 3)
	unsubscribe, err := driver.Subscribe(t.Context(), "users.#", func(payload []byte) {
		received <- string(payload)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = unsubscribe()
	})

	if err := conn.Flush(); err != nil {
		t.Fatal(err)
	}

	for _, subject := range []string{"users", "users.created", "users.created.deep"} {
		if err := conn.Publish(prefix+"."+subject, []byte("raw")); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	for range 3 {
		select {
		case got := <-received:
			if got != "raw" {
				t.Fatalf("raw payload = %q", got)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
