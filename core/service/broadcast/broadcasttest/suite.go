package broadcasttest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/broadcast"
)

const (
	// eventTimeout bounds the wait for an expected delivery.
	eventTimeout = 20 * time.Second

	// publishInterval separates the repeated publishes that wait for a
	// consumer's subscription to become active.
	publishInterval = 100 * time.Millisecond

	// quietPeriod is how long the suite watches for deliveries that must not
	// happen.
	quietPeriod = 300 * time.Millisecond
)

const (
	// nameCreated is the plain name of most published messages.
	nameCreated = "conformance.created"

	// nameDeep checks that names with many tokens survive transport.
	nameDeep = "conformance.deep.name.with.tokens"

	// nameReading is the name of the typed broadcasts.
	nameReading = "conformance.reading"

	// nameUserCreated matches the pattern listener.
	nameUserCreated = "conformance.users.created"

	// nameOrderCreated does not match the pattern listener.
	nameOrderCreated = "conformance.orders.created"

	// nameProfileUpdated has too many tokens for the pattern listener.
	nameProfileUpdated = "conformance.users.profile.updated"
)

// Names lists every broadcast name the suite publishes. Drivers that route
// by configured name, such as the AWS drivers, must be configured with a
// destination for each.
var Names = []string{nameCreated, nameDeep, nameReading, nameUserCreated, nameOrderCreated, nameProfileUpdated}

// Driver is the system under test of [Run]: the two driver interfaces of the
// broadcast contract. They may be one value, or two values sharing a
// transport scope, such as an SNS publisher and the SQS consumer of a queue
// subscribed to its topic.
type Driver struct {
	// Publisher publishes envelopes.
	Publisher broadcast.PublisherDriver

	// Consumer receives them. It must receive what Publisher publishes.
	Consumer broadcast.ConsumerDriver
}

// Option declares how [Run] runs the suite.
type Option func(*suiteConfig)

// suiteConfig holds the settings declared by [Option] values.
type suiteConfig struct {
	// synctest runs every subtest in a testing/synctest bubble.
	synctest bool
}

// WithSynctest runs every subtest inside a testing/synctest bubble, so waits
// take no real time. newDriver is called inside the bubble. Use it for drivers
// that live in the process and use the time package, such as the memory
// driver. Never use it with a driver that talks to an external system.
//
// # Examples
//
//	broadcasttest.Run(t, func(t *testing.T) broadcasttest.Driver {
//		driver := memorybroadcast.New()
//		t.Cleanup(func() {
//			_ = driver.Stop(context.Background())
//		})
//		return broadcasttest.Driver{Publisher: driver, Consumer: driver}
//	}, broadcasttest.WithSynctest())
func WithSynctest() Option {
	return func(config *suiteConfig) {
		config.synctest = true
	}
}

// Run is the conformance suite for broadcast drivers. It runs one subtest per
// behavior documented on [broadcast.PublisherDriver] and
// [broadcast.ConsumerDriver], plus the end-to-end behavior of
// [broadcast.Consumer] on top of the driver. newDriver is called once per
// subtest and must return a driver on an isolated transport scope,
// registering any cleanup with t.
//
// Transports may drop messages published before a consumer's subscription is
// active, so the suite publishes repeatedly until a message arrives and
// tolerates duplicates. Unless [WithSynctest] is given it uses real time, so
// infrastructure backed drivers should run it only outside short mode.
func Run(t *testing.T, newDriver func(t *testing.T) Driver, opts ...Option) {
	t.Helper()

	var config suiteConfig

	for _, opt := range opts {
		opt(&config)
	}

	subtests := []struct {
		// name labels the transport behavior checked by the subtest.
		name string
		// run checks that behavior against a fresh, isolated transport scope.
		run func(*harness)
	}{
		{
			"PublishDeliversMessage",
			testDeliversMessage,
		},
		{
			"PublishPreservesNameAndPayload",
			testPreservesPayload,
		},
		{
			"PublishPreservesMetadata",
			testPreservesMetadata,
		},
		{
			"PublishWithoutMetadataDeliversNone",
			testNoMetadata,
		},
		{
			"PublishRejectsInvalidNames",
			testRejectsInvalidNames,
		},
		{
			"PublishHonorsContext",
			testHonorsContext,
		},
		{
			"ConsumeContinuesAfterHandlerError",
			testContinuesAfterHandlerError,
		},
		{
			"ConsumeStopsOnCancel",
			testConsumeStopsOnCancel,
		},
		{
			"ConsumerRoutesToListeners",
			testConsumerRoutes,
		},
		{
			"ConsumerMatchesPatterns",
			testConsumerPatterns,
		},
		{
			"ConsumerStopsAfterUnsubscribe",
			testConsumerUnsubscribe,
		},
	}

	for _, subtest := range subtests {
		t.Run(subtest.name, func(t *testing.T) {
			if config.synctest {
				synctest.Test(t, func(t *testing.T) {
					subtest.run(newHarness(t, newDriver))
				})

				return
			}

			subtest.run(newHarness(t, newDriver))
		})
	}
}

// harness carries the state of one subtest.
type harness struct {
	// t is the subtest.
	t *testing.T

	// driver is the driver under test.
	driver Driver

	// ctx bounds the whole subtest.
	ctx context.Context
}

// newHarness builds the driver for one subtest.
func newHarness(t *testing.T, newDriver func(t *testing.T) Driver) *harness {
	t.Helper()

	driver := newDriver(t)

	if driver.Publisher == nil || driver.Consumer == nil {
		t.Fatal("newDriver returned a Driver without Publisher or Consumer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*eventTimeout)
	t.Cleanup(cancel)

	return &harness{
		t:      t,
		driver: driver,
		ctx:    ctx,
	}
}

// consume runs driver.Consume until the subtest ends and returns a function
// that stops it and returns its error.
func (harness *harness) consume(handler broadcast.Handler) (stop func() error) {
	harness.t.Helper()

	ctx, cancel := context.WithCancel(harness.ctx)
	result := make(chan error, 1)

	go func() {
		result <- harness.driver.Consumer.Consume(ctx, handler)
	}()

	var stopped atomic.Bool
	var err error
	stop = func() error {
		if stopped.Swap(true) {
			return err
		}

		cancel()

		select {
		case err = <-result:
		case <-time.After(eventTimeout):
			harness.t.Error("Consume did not return after its context was canceled")
		}

		return err
	}
	harness.t.Cleanup(func() {
		_ = stop()
	})

	return stop
}

// publishUntil publishes message every publishInterval until received
// delivers a value, then returns it. Re-publishing covers transports that
// drop messages sent before the consumer's subscription is active.
func publishUntil[T any](h *harness, message broadcast.Message, received <-chan T) T {
	h.t.Helper()

	deadline := time.After(eventTimeout)
	ticker := time.NewTicker(publishInterval)
	defer ticker.Stop()

	for {
		if err := h.driver.Publisher.Publish(h.ctx, message); err != nil {
			h.t.Fatalf("Publish(%s): %v", message.Name, err)
		}

		select {
		case value := <-received:
			return value
		case <-ticker.C:
		case <-deadline:
			h.t.Fatalf("no delivery of %s within %s", message.Name, eventTimeout)
		}
	}
}

// recordTo returns a handler that sends every message to received.
func recordTo(received chan<- broadcast.Message) broadcast.Handler {
	return func(_ context.Context, message broadcast.Message) error {
		received <- message

		return nil
	}
}

// testDeliversMessage checks that a published message arrives.
func testDeliversMessage(h *harness) {
	received := make(chan broadcast.Message, 64)
	h.consume(recordTo(received))

	message := broadcast.Message{
		Name:    nameCreated,
		Payload: []byte(`{"id":"` + rand.Text() + `"}`),
	}
	got := publishUntil(h, message, received)

	if got.Name != message.Name || !bytes.Equal(got.Payload, message.Payload) {
		h.t.Errorf("delivered %s %s, want %s %s", got.Name, got.Payload, message.Name, message.Payload)
	}
}

// testPreservesPayload checks that names and payloads survive transport.
func testPreservesPayload(h *harness) {
	received := make(chan broadcast.Message, 64)
	h.consume(recordTo(received))

	payload := `{"text":"héllo 世界","items":[` + strings.Repeat(`"0123456789",`, 200) + `""]}`
	message := broadcast.Message{
		Name:    nameDeep,
		Payload: []byte(payload),
	}
	got := publishUntil(h, message, received)

	if got.Name != message.Name || string(got.Payload) != payload {
		h.t.Errorf("delivered %s with %d payload bytes, want %s with %d", got.Name, len(got.Payload), message.Name, len(payload))
	}
}

// metadataCases returns metadata that every driver must carry unchanged: the
// W3C trace context keys, the limits of [broadcast.Message.Metadata], and
// awkward values.
func metadataCases() map[string]string {
	metadata := map[string]string{
		"traceparent":      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"tracestate":       "vendor=opaque,other=1",
		"Mixed.Case_key-1": "Wert mit Überlänge 世界 \"quotes\" and\nnewline",
		"empty":            "",
	}

	for index := range broadcast.MaxMetadataEntries - len(metadata) {
		metadata[fmt.Sprintf("k%02d", index)] = strings.Repeat("v", 100)
	}

	return metadata
}

// testPreservesMetadata checks that Metadata survives transport unchanged.
func testPreservesMetadata(h *harness) {
	received := make(chan broadcast.Message, 64)
	h.consume(recordTo(received))

	want := metadataCases()
	message := broadcast.Message{
		Name:     nameCreated,
		Payload:  []byte(`{"id":"` + rand.Text() + `"}`),
		Metadata: maps.Clone(want),
	}
	got := publishUntil(h, message, received)

	if !maps.Equal(got.Metadata, want) {
		h.t.Errorf("delivered metadata %v, want %v", got.Metadata, want)
	}

	if !maps.Equal(message.Metadata, want) {
		h.t.Error("Publish modified the metadata of the caller")
	}
}

// testNoMetadata checks that a message without Metadata arrives without any.
func testNoMetadata(h *harness) {
	received := make(chan broadcast.Message, 64)
	h.consume(recordTo(received))

	got := publishUntil(h, broadcast.Message{
		Name:    nameCreated,
		Payload: []byte(`{}`),
	}, received)

	if len(got.Metadata) != 0 {
		h.t.Errorf("delivered metadata %v, want none", got.Metadata)
	}
}

// testRejectsInvalidNames checks that Publish validates names.
func testRejectsInvalidNames(h *harness) {
	for _, name := range []string{"", "a..b", ".a", "a.", "users.*", "users.#", "a\x00b"} {
		err := h.driver.Publisher.Publish(h.ctx, broadcast.Message{
			Name:    name,
			Payload: []byte(`{}`),
		})

		if !errors.Is(err, broadcast.ErrInvalidName) {
			h.t.Errorf("Publish(%q) = %v, want ErrInvalidName", name, err)
		}
	}
}

// testHonorsContext checks that canceled contexts are respected.
func testHonorsContext(h *harness) {
	canceled, cancel := context.WithCancel(h.ctx)
	cancel()

	err := h.driver.Publisher.Publish(canceled, broadcast.Message{
		Name:    nameCreated,
		Payload: []byte(`{}`),
	})

	if !errors.Is(err, context.Canceled) {
		h.t.Errorf("Publish with a canceled context = %v, want context.Canceled", err)
	}

	err = h.driver.Consumer.Consume(canceled, func(context.Context, broadcast.Message) error {
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		h.t.Errorf("Consume with a canceled context = %v, want context.Canceled", err)
	}
}

// testContinuesAfterHandlerError checks that a failing handler does not end Consume.
func testContinuesAfterHandlerError(h *harness) {
	received := make(chan broadcast.Message, 64)
	var failed atomic.Bool
	h.consume(func(_ context.Context, message broadcast.Message) error {
		if !failed.Swap(true) {
			return errors.New("conformance handler failure")
		}

		received <- message

		return nil
	})

	message := broadcast.Message{
		Name:    nameCreated,
		Payload: []byte(`{}`),
	}
	publishUntil(h, message, received)
}

// testConsumeStopsOnCancel checks that cancellation ends Consume with the context error.
func testConsumeStopsOnCancel(h *harness) {
	stop := h.consume(func(context.Context, broadcast.Message) error {
		return nil
	})
	time.Sleep(quietPeriod / 3)

	if err := stop(); !errors.Is(err, context.Canceled) {
		h.t.Errorf("Consume returned %v after cancellation, want context.Canceled", err)
	}
}

// reading is the payload of the typed broadcasts of the suite.
type reading struct {
	// Value distinguishes readings.
	Value int `json:"value"`
}

// runConsumer works consumer until the subtest ends.
func (harness *harness) runConsumer(consumer *broadcast.Consumer) {
	harness.t.Helper()

	ctx, cancel := context.WithCancel(harness.ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = consumer.Run(ctx)
	}()

	harness.t.Cleanup(func() {
		cancel()
		<-done
	})
}

// testConsumerRoutes checks that typed listeners receive decoded payloads.
func testConsumerRoutes(h *harness) {
	topic := broadcast.NewTopic[reading](nameReading)
	received := make(chan reading, 64)
	consumer := broadcast.NewConsumer(h.driver.Consumer)

	if _, err := consumer.Listen(topic, func(_ context.Context, payload reading) error {
		received <- payload

		return nil
	}); err != nil {
		h.t.Fatal(err)
	}

	h.runConsumer(consumer)

	publisher := broadcast.NewPublisher(h.driver.Publisher)
	deadline := time.After(eventTimeout)

	for {
		if err := publisher.Publish(h.ctx, topic, reading{
			Value: 42,
		}); err != nil {
			h.t.Fatal(err)
		}

		select {
		case got := <-received:
			if got.Value != 42 {
				h.t.Fatalf("listener received %+v, want value 42", got)
			}

			return
		case <-time.After(publishInterval):
		case <-deadline:
			h.t.Fatal("listener received nothing")
		}
	}
}

// testConsumerPatterns checks that pattern listeners receive matching names only.
func testConsumerPatterns(h *harness) {
	matched := make(chan string, 64)
	consumer := broadcast.NewConsumer(h.driver.Consumer)

	if _, err := consumer.ListenPattern("conformance.users.*", func(_ context.Context, message broadcast.Message) error {
		matched <- message.Name

		return nil
	}); err != nil {
		h.t.Fatal(err)
	}

	h.runConsumer(consumer)

	publishUntil(h, broadcast.Message{
		Name:    nameUserCreated,
		Payload: []byte(`{}`),
	}, matched)

	// Messages are ordered per publisher, so once this arrives the earlier
	// non-matching ones had their chance.
	for _, name := range []string{nameOrderCreated, nameProfileUpdated} {
		if err := h.driver.Publisher.Publish(h.ctx, broadcast.Message{
			Name:    name,
			Payload: []byte(`{}`),
		}); err != nil {
			h.t.Fatal(err)
		}
	}

	for {
		select {
		case name := <-matched:
			if name != nameUserCreated {
				h.t.Fatalf("pattern listener received %q, which does not match", name)
			}
		case <-time.After(quietPeriod):
			return
		}
	}
}

// testConsumerUnsubscribe checks that an unsubscribed listener stops receiving.
func testConsumerUnsubscribe(h *harness) {
	topic := broadcast.NewTopic[reading](nameReading)
	first := make(chan reading, 64)
	second := make(chan reading, 64)
	consumer := broadcast.NewConsumer(h.driver.Consumer)

	unsubscribe, err := consumer.Listen(topic, func(_ context.Context, payload reading) error {
		first <- payload

		return nil
	})

	if err != nil {
		h.t.Fatal(err)
	}

	if _, err := consumer.Listen(topic, func(_ context.Context, payload reading) error {
		second <- payload

		return nil
	}); err != nil {
		h.t.Fatal(err)
	}

	h.runConsumer(consumer)

	message := broadcast.Message{
		Name:    topic.Name(),
		Payload: []byte(`{"value":1}`),
	}
	publishUntil(h, message, second)
	unsubscribe()
	time.Sleep(quietPeriod) // let in-flight duplicates land
	drain(first)

	publishUntil(h, message, second)
	time.Sleep(quietPeriod)

	if len(first) > 0 {
		h.t.Error("unsubscribed listener still received a message")
	}
}

// drain discards everything buffered in received.
func drain[T any](received <-chan T) {
	for {
		select {
		case <-received:
		default:
			return
		}
	}
}
