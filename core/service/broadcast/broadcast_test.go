package broadcast_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/broadcast/broadcasttest"
	"arkoren.dev/core/service/broadcast/memorybroadcast"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// secretText is a payload value that must never reach telemetry.
const secretText = "alice@example.com"

// deliverTraced publishes one message and delivers it to a listener that
// returns listenerErr, all traced by the returned fake.
func deliverTraced(t *testing.T, listenerErr error, opts ...func(*broadcast.ConsumerBuilder)) (*broadcasttest.Fake, *telemetrytest.Fake) {
	t.Helper()

	fake := telemetrytest.New()
	opts = append([]func(*broadcast.ConsumerBuilder){func(consumer *broadcast.ConsumerBuilder) {
		consumer.Telemetry(fake.Telemetry(t))
	}}, opts...)

	driver := broadcasttest.New()
	consumer := broadcast.NewConsumer(driver, opts...)

	if _, err := consumer.Listen(topicString, func(ctx context.Context, _ string) error {
		if !telemetry.SpanFromContext(ctx).IsRecording() {
			t.Error("listener context has no recording span")
		}

		return listenerErr
	}); err != nil {
		t.Fatal(err)
	}

	if err := broadcast.NewPublisher(driver, func(publisher *broadcast.PublisherBuilder) {
		publisher.Telemetry(fake.Telemetry(t))
		publisher.System("custom")
	}).Publish(t.Context(), topicString, secretText); err != nil {
		t.Fatal(err)
	}

	_ = driver.Drain(t.Context(), consumer)

	return driver, fake
}

// TestPublishAndConsumeShareOneTrace checks message metadata links producer and
// consumer spans and both operations record their messaging metrics.
func TestPublishAndConsumeShareOneTrace(t *testing.T) {
	t.Parallel()

	driver, fake := deliverTraced(t, nil, func(consumer *broadcast.ConsumerBuilder) {
		consumer.System("custom")
	})

	sent, found := fake.Span("send users.created")

	if !found {
		t.Fatal("no producer span")
	}

	processed, found := fake.Span("process users.created")

	if !found {
		t.Fatal("no consumer span")
	}

	if sent.Kind != telemetry.Producer || processed.Kind != telemetry.Consumer {
		t.Errorf("kinds = %v, %v", sent.Kind, processed.Kind)
	}

	if processed.Context.TraceID != sent.Context.TraceID || processed.Parent.SpanID != sent.Context.SpanID {
		t.Errorf("consumer span %+v is not a child of producer span %+v", processed.Context, sent.Context)
	}

	if message := driver.Published()[0]; message.Metadata["traceparent"] == "" {
		t.Errorf("metadata = %v, want a traceparent", message.Metadata)
	}

	labels := []telemetry.Attr{
		telemetry.String("messaging.system", "custom"),
		telemetry.String("messaging.destination.name", "users.created"),
	}

	fake.AssertSpan(t, "send users.created", append(labels, telemetry.String("messaging.operation.type", "send"))...)
	fake.AssertSpan(t, "process users.created", append(labels, telemetry.String("messaging.operation.name", "process"))...)
	fake.AssertCounter(t, "messaging.client.sent.messages", 1, labels...)
	fake.AssertCounter(t, "messaging.client.consumed.messages", 1, labels...)
	fake.AssertHistogram(t, "messaging.client.operation.duration", 1, labels...)
	fake.AssertHistogram(t, "messaging.process.duration", 1, labels...)
}

// TestMemoryDriverNamesItsSystem verifies telemetry discovers the memory
// driver's system name without an explicit facade setting.
func TestMemoryDriverNamesItsSystem(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	publisher := broadcast.NewPublisher(memorybroadcast.New(), func(publisher *broadcast.PublisherBuilder) {
		publisher.Telemetry(fake.Telemetry(t))
	})

	if err := publisher.Publish(t.Context(), topicString, "x"); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "send users.created", telemetry.String("messaging.system", "memory"))
}

// TestFailedListenerMarksConsumerSpan checks listener failures mark telemetry
// as erroneous without recording exception payloads.
func TestFailedListenerMarksConsumerSpan(t *testing.T) {
	t.Parallel()

	_, fake := deliverTraced(t, io.ErrUnexpectedEOF)

	span, _ := fake.Span("process users.created")

	if span.Status != telemetry.Error || len(span.Events) != 0 {
		t.Errorf("status = %v, events = %v, want an error without recorded exception", span.Status, span.Events)
	}

	fake.AssertCounter(t, "messaging.client.consumed.messages", 1, telemetry.String("error.type", "*errors.errorString"))
}

// TestPublishFailureMarksProducerSpan checks driver failures reach the caller
// and mark the producer span as erroneous.
func TestPublishFailureMarksProducerSpan(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := broadcasttest.New()
	driver.FailNext(io.ErrClosedPipe)

	err := broadcast.NewPublisher(driver, func(publisher *broadcast.PublisherBuilder) {
		publisher.Telemetry(fake.Telemetry(t))
	}).Publish(t.Context(), topicString, "x")

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("err = %v", err)
	}

	span, _ := fake.Span("send users.created")

	if span.Status != telemetry.Error {
		t.Errorf("status = %v, want error", span.Status)
	}
}

// TestTelemetryNeverRecordsPayloads checks message contents and error text do
// not leak into recorded span attributes or status descriptions.
func TestTelemetryNeverRecordsPayloads(t *testing.T) {
	t.Parallel()

	_, fake := deliverTraced(t, errors.New(secretText))

	for _, span := range fake.Spans() {
		for _, attr := range span.Attrs {
			if strings.Contains(attr.Text(), secretText) {
				t.Errorf("span %q attribute %s leaks the payload", span.Name, attr.Key())
			}
		}

		if strings.Contains(span.StatusDescription, secretText) {
			t.Errorf("span %q status leaks the payload", span.Name)
		}
	}
}

// TestFollowsDefaultTelemetry verifies a facade constructed without telemetry
// uses a process default installed after construction.
func TestFollowsDefaultTelemetry(t *testing.T) {
	publisher := broadcast.NewPublisher(broadcasttest.New())
	fake := telemetrytest.New()

	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	if err := publisher.Publish(t.Context(), topicString, "x"); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "send users.created")
}

// discardPublisher accepts every message.
type discardPublisher struct{}

// Publish accepts the message without retaining it, isolating facade overhead
// from transport allocations in tests and benchmarks.
func (discardPublisher) Publish(context.Context, broadcast.Message) error {
	return nil
}

// TestDisabledTelemetryAllocatesNothing compares explicit disabled telemetry
// with the unconfigured facade to detect instrumentation allocations.
func TestDisabledTelemetryAllocatesNothing(t *testing.T) {
	ctx := context.Background()
	plain := broadcast.NewPublisher(discardPublisher{}, func(publisher *broadcast.PublisherBuilder) {
		publisher.Telemetry(telemetry.New(nil))
	})

	bare := broadcast.NewPublisher(discardPublisher{})

	traced := testing.AllocsPerRun(100, func() {
		_ = plain.Publish(ctx, topicInt, 1)
	})

	baseline := testing.AllocsPerRun(100, func() {
		_ = bare.Publish(ctx, topicInt, 1)
	})

	if traced != baseline {
		t.Errorf("disabled telemetry changed allocations per Publish from %v to %v", baseline, traced)
	}
}

// BenchmarkPublishNoTelemetry measures publication allocations and execution
// cost with instrumentation disabled and no transport work.
func BenchmarkPublishNoTelemetry(b *testing.B) {
	publisher := broadcast.NewPublisher(discardPublisher{}, func(publisher *broadcast.PublisherBuilder) {
		publisher.Telemetry(telemetry.New(nil))
	})

	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		_ = publisher.Publish(ctx, topicInt, 1)
	}
}

// errHandler identifies listener failures in joined delivery errors.
var errHandler = errors.New("handler failed")

// consumer adapts a synchronous delivery callback to the consumer driver contract.
type consumer struct {
	// consume controls the messages delivered during one Run call.
	consume func(context.Context, broadcast.Handler) error
}

// Consume delegates to the fixture callback without adding transport behavior.
func (consumer consumer) Consume(ctx context.Context, handler broadcast.Handler) error {
	return consumer.consume(ctx, handler)
}

// userCreated is a payload used to check JSON encoding.
type userCreated struct {
	// ID is serialized into the envelope to verify typed payload encoding.
	ID int `json:"id"`
}

var (
	// topicInt selects integer decoding for the shared routing key.
	topicInt = broadcast.NewTopic[int]("users.created")
	// topicString selects string decoding for the same routing key.
	topicString = broadcast.NewTopic[string]("users.created")
)

// TestBroadcastCreatesEnvelope checks the routing key and JSON payload passed
// to the driver, rather than relying on a transport's serialization.
func TestBroadcastCreatesEnvelope(t *testing.T) {
	t.Parallel()

	publisher := broadcasttest.New()

	err := broadcast.NewPublisher(publisher).Publish(context.Background(), broadcast.NewTopic[userCreated]("users.created"), userCreated{
		ID: 7,
	})

	if err != nil {
		t.Fatal(err)
	}

	published := publisher.Published()

	if len(published) != 1 || published[0].Name != "users.created" {
		t.Fatalf("published = %#v", published)
	}

	if string(published[0].Payload) != `{"id":7}` {
		t.Fatalf("payload = %s", published[0].Payload)
	}
}

// TestBroadcastRejectsInvalidName checks wildcard topics fail publication with
// the shared invalid-name sentinel.
func TestBroadcastRejectsInvalidName(t *testing.T) {
	t.Parallel()

	err := broadcast.NewPublisher(broadcasttest.New()).Publish(context.Background(), broadcast.NewTopic[int]("users.*"), 7)

	if !errors.Is(err, broadcast.ErrInvalidName) {
		t.Fatalf("error = %v", err)
	}
}

// TestBroadcastRejectsNilDriver verifies a missing transport returns an error
// instead of attempting publication.
func TestBroadcastRejectsNilDriver(t *testing.T) {
	t.Parallel()

	err := broadcast.NewPublisher(nil).Publish(context.Background(), broadcast.NewTopic[int]("users.created"), 7)

	if err == nil {
		t.Fatal("expected error")
	}
}

// TestConsumerRoutesMatchingListenersInOrder checks overlapping wildcard
// registrations run in registration order for one delivery.
func TestConsumerRoutesMatchingListenersInOrder(t *testing.T) {
	t.Parallel()

	var calls []int
	consumer := broadcast.NewConsumer(consumer{
		consume: func(ctx context.Context, handler broadcast.Handler) error {
			return handler(ctx, broadcast.Message{
				Name:    "users.created",
				Payload: []byte(`7`),
			})
		},
	})

	_, err := consumer.ListenPattern("users.*", func(_ context.Context, message broadcast.Message) error {
		calls = append(calls, len(message.Payload))

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = consumer.ListenPattern("users.#", func(_ context.Context, message broadcast.Message) error {
		calls = append(calls, len(message.Payload)+1)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 || calls[0] != 1 || calls[1] != 2 {
		t.Fatalf("calls = %v", calls)
	}
}

// TestConsumerJoinsListenerErrors verifies independent decoding and listener
// failures preserve the original listener cause in the combined result.
func TestConsumerJoinsListenerErrors(t *testing.T) {
	t.Parallel()

	consumer := broadcast.NewConsumer(consumer{
		consume: func(ctx context.Context, handler broadcast.Handler) error {
			return handler(ctx, broadcast.Message{
				Name:    "users.created",
				Payload: []byte(`"not a number"`),
			})
		},
	})

	_, err := consumer.Listen(topicString, func(_ context.Context, _ string) error {
		return errHandler
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = consumer.Listen(topicInt, func(_ context.Context, _ int) error {
		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	err = consumer.Run(context.Background())

	if !errors.Is(err, errHandler) {
		t.Fatalf("error = %v", err)
	}
}

// TestConsumerRecoversListenerPanic checks a listener panic becomes a delivery
// error rather than escaping the consumer.
func TestConsumerRecoversListenerPanic(t *testing.T) {
	t.Parallel()

	consumer := broadcast.NewConsumer(consumer{
		consume: func(ctx context.Context, handler broadcast.Handler) error {
			return handler(ctx, broadcast.Message{
				Name:    "users.created",
				Payload: []byte(`1`),
			})
		},
	})

	_, err := consumer.Listen(topicInt, func(_ context.Context, _ int) error {
		panic("boom")
	})

	if err != nil {
		t.Fatal(err)
	}

	err = consumer.Run(context.Background())

	if err == nil {
		t.Fatal("expected panic error")
	}
}

// TestConsumerUnsubscribeIsIdempotent verifies a registration can be removed
// repeatedly without a panic.
func TestConsumerUnsubscribeIsIdempotent(t *testing.T) {
	t.Parallel()

	consumer := broadcast.NewConsumer(consumer{})

	unsubscribe, err := consumer.Listen(topicInt, func(_ context.Context, _ int) error {
		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	unsubscribe()
	unsubscribe()
}

// TestConsumerSnapshotsConcurrentRegistration checks registrations completed
// before routing begins are both included in that delivery's snapshot.
func TestConsumerSnapshotsConcurrentRegistration(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	consumer := broadcast.NewConsumer(consumer{
		consume: func(ctx context.Context, handler broadcast.Handler) error {
			close(started)
			<-release

			return handler(ctx, broadcast.Message{
				Name:    "users.created",
				Payload: []byte(`1`),
			})
		},
	})

	var first, second bool

	_, err := consumer.Listen(topicInt, func(_ context.Context, _ int) error {
		first = true

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	group.Go(func() {
		_ = consumer.Run(context.Background())
	})
	<-started

	_, err = consumer.Listen(topicInt, func(_ context.Context, _ int) error {
		second = true

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	close(release)
	group.Wait()

	if !first || !second {
		t.Fatalf("listeners called = %t, %t", first, second)
	}
}

// TestConsumerAllowsNoListeners verifies an unmatched delivery succeeds without
// requiring an application listener.
func TestConsumerAllowsNoListeners(t *testing.T) {
	t.Parallel()

	consumer := broadcast.NewConsumer(consumer{
		consume: func(ctx context.Context, handler broadcast.Handler) error {
			return handler(ctx, broadcast.Message{
				Name:    "users.created",
				Payload: []byte(`1`),
			})
		},
	})

	if err := consumer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestMatchSupportsWildcards checks single-token matching and the zero-token
// case of a trailing multi-token wildcard.
func TestMatchSupportsWildcards(t *testing.T) {
	t.Parallel()

	if !broadcast.Match("users.*", "users.created") {
		t.Fatal("single wildcard did not match")
	}

	if !broadcast.Match("users.#", "users") {
		t.Fatal("tail wildcard did not match zero tokens")
	}
}

// TestPermanentDecodeError verifies incompatible JSON is terminal because
// redelivery cannot change the listener's payload type.
func TestPermanentDecodeError(t *testing.T) {
	t.Parallel()

	consumer := broadcast.NewConsumer(consumer{
		consume: func(ctx context.Context, handler broadcast.Handler) error {
			return handler(ctx, broadcast.Message{
				Name:    "users.created",
				Payload: []byte(`"text"`),
			})
		},
	})

	_, err := consumer.Listen(topicInt, func(context.Context, int) error {
		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	err = consumer.Run(context.Background())

	if !errors.Is(err, broadcast.ErrPermanent) {
		t.Fatalf("error = %v, want permanent", err)
	}
}

// TestPermanentWrapsListenerError checks the retry marker preserves its cause
// and leaves a nil success unchanged.
func TestPermanentWrapsListenerError(t *testing.T) {
	t.Parallel()

	cause := errors.New("bad payload")

	err := broadcast.Permanent(cause)

	if !errors.Is(err, broadcast.ErrPermanent) || !errors.Is(err, cause) {
		t.Fatalf("Permanent = %v, want a permanent error wrapping its cause", err)
	}

	if broadcast.Permanent(nil) != nil {
		t.Fatal("Permanent(nil) is not nil")
	}
}

// runListeners delivers one message to listeners that fail with errs, in
// order, and returns the error of the delivery.
func runListeners(t *testing.T, errs ...error) error {
	t.Helper()

	consumer := broadcast.NewConsumer(consumer{
		consume: func(ctx context.Context, handler broadcast.Handler) error {
			return handler(ctx, broadcast.Message{
				Name:    "users.created",
				Payload: []byte(`1`),
			})
		},
	})

	for _, listenerErr := range errs {
		_, err := consumer.Listen(topicInt, func(context.Context, int) error {
			return listenerErr
		})

		if err != nil {
			t.Fatal(err)
		}
	}

	return consumer.Run(context.Background())
}

// checkJoinedPermanent runs one listener per error and fails the test unless
// the joined result matches ErrPermanent exactly when want is true.
func checkJoinedPermanent(t *testing.T, want bool, errs ...error) {
	t.Helper()

	if got := errors.Is(runListeners(t, errs...), broadcast.ErrPermanent); got != want {
		t.Errorf("permanent = %t, want %t", got, want)
	}
}

// TestPermanentJoinedTransient checks an ordinary listener failure remains
// retryable after aggregation.
func TestPermanentJoinedTransient(t *testing.T) {
	t.Parallel()

	checkJoinedPermanent(t, false, errors.New("database down"))
}

// TestPermanentJoinedPermanent checks a single terminal listener failure
// remains terminal after aggregation.
func TestPermanentJoinedPermanent(t *testing.T) {
	t.Parallel()

	checkJoinedPermanent(t, true, broadcast.Permanent(errors.New("bad payload")))
}

// TestPermanentJoinedAllPermanent verifies multiple terminal failures do not
// make a delivery retryable.
func TestPermanentJoinedAllPermanent(t *testing.T) {
	t.Parallel()

	permanent := broadcast.Permanent(errors.New("bad payload"))

	checkJoinedPermanent(t, true, permanent, permanent)
}

// TestPermanentJoinedMixed verifies a transient failure keeps a delivery
// retryable even when another listener fails permanently.
func TestPermanentJoinedMixed(t *testing.T) {
	t.Parallel()

	checkJoinedPermanent(t, false, broadcast.Permanent(errors.New("bad payload")), errors.New("database down"))
}

// TestPermanentJoinedMixedWithSuccess checks successful listeners do not
// change the permanent classification of the only failure.
func TestPermanentJoinedMixedWithSuccess(t *testing.T) {
	t.Parallel()

	checkJoinedPermanent(t, true, nil, broadcast.Permanent(errors.New("bad payload")), nil)
}

// TestPermanentJoinedMixedKeepsTransientCause verifies suppressing the terminal
// marker does not hide the retryable cause from errors.Is.
func TestPermanentJoinedMixedKeepsTransientCause(t *testing.T) {
	t.Parallel()

	transient := errors.New("database down")
	permanent := broadcast.Permanent(errors.New("bad payload"))

	if err := runListeners(t, permanent, transient); !errors.Is(err, transient) {
		t.Errorf("mixed error %v lost its transient cause", err)
	}
}

// TestValidateNameUsesSharedRules checks exact names and wildcard patterns use
// their respective shared naming constraints.
func TestValidateNameUsesSharedRules(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"users.created", "a", "a-b.c1"} {
		if err := broadcast.ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v", name, err)
		}
	}

	for _, name := range []string{"", "Users.created", "users.signed_up", "users..created", "users.*", "-a"} {
		if err := broadcast.ValidateName(name); !errors.Is(err, broadcast.ErrInvalidName) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidName", name, err)
		}
	}

	if err := broadcast.ValidatePattern("users.*.#"); err != nil {
		t.Errorf("ValidatePattern = %v", err)
	}

	if err := broadcast.ValidatePattern("#.users"); !errors.Is(err, broadcast.ErrInvalidName) {
		t.Errorf("ValidatePattern(#.users) = %v, want ErrInvalidName", err)
	}
}
