package queue_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/queue/internal/measurement"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue"
	"arkoren.dev/core/service/queue/queuetest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// secretAddress is a payload value that must never reach telemetry.
const secretAddress = "alice@example.com"

// tracedQueue is a dispatcher and consumer sharing one fake driver and one
// fake telemetry.
type tracedQueue struct {
	// driver records and delivers the messages.
	driver *queuetest.Fake

	// dispatcher sends jobs.
	dispatcher *queue.Dispatcher

	// consumer runs jobs.
	consumer *queue.Consumer

	// fake records the telemetry.
	fake *telemetrytest.Fake
}

// newTracedQueue returns a tracedQueue whose email handler is fn.
func newTracedQueue(t *testing.T, fn func(context.Context, email) error, system string) tracedQueue {
	t.Helper()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	driver := queuetest.New()
	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
		consumer.Telemetry(service).System(system)
	})

	if err := consumer.Handle(emailJob, fn); err != nil {
		t.Fatal(err)
	}

	dispatcher := queue.NewDispatcher(driver, func(dispatcher *queue.DispatcherBuilder) {
		dispatcher.Telemetry(service).System(system)
	})

	return tracedQueue{
		driver:     driver,
		dispatcher: dispatcher,
		consumer:   consumer,
		fake:       fake,
	}
}

// run dispatches one email and drains the queue.
func (traced tracedQueue) run(t *testing.T) {
	t.Helper()

	if err := traced.dispatcher.Dispatch(t.Context(), emailJob, email{
		ID: secretAddress,
	}); err != nil {
		t.Fatal(err)
	}

	if err := traced.driver.Drain(t.Context(), traced.consumer); err != nil {
		t.Fatal(err)
	}
}

// TestDispatchAndConsumeShareOneTrace checks message metadata parents the
// consumer span to the producer and both operations record messaging metrics.
func TestDispatchAndConsumeShareOneTrace(t *testing.T) {
	t.Parallel()

	traced := newTracedQueue(t, func(ctx context.Context, _ email) error {
		if !telemetry.SpanFromContext(ctx).IsRecording() {
			t.Error("handler context has no recording span")
		}

		return nil
	}, "custom")
	traced.run(t)

	sent, found := traced.fake.Span("send emails")

	if !found {
		t.Fatal("no producer span")
	}

	processed, found := traced.fake.Span("process emails")

	if !found {
		t.Fatal("no consumer span")
	}

	if sent.Kind != telemetry.Producer || processed.Kind != telemetry.Consumer {
		t.Errorf("kinds = %v, %v", sent.Kind, processed.Kind)
	}

	if processed.Context.TraceID != sent.Context.TraceID || processed.Parent.SpanID != sent.Context.SpanID {
		t.Errorf("consumer span %+v is not a child of producer span %+v", processed.Context, sent.Context)
	}

	if message := traced.driver.Dispatched()[0]; message.Metadata["traceparent"] == "" {
		t.Errorf("metadata = %v, want a traceparent", message.Metadata)
	}

	traced.fake.AssertSpan(t, "send emails",
		telemetry.String("messaging.system", "custom"),
		telemetry.String("messaging.operation.name", "send"),
		telemetry.String("messaging.operation.type", "send"),
		telemetry.String("messaging.destination.name", "emails"),
		telemetry.String("arkoren.queue.job", "emails-send"))
	traced.fake.AssertSpan(t, "process emails",
		telemetry.String("messaging.operation.name", "process"),
		telemetry.String("messaging.destination.name", "emails"),
		telemetry.String("arkoren.queue.job", "emails-send"))

	if _, found := processed.Attr("messaging.message.id"); !found {
		t.Error("consumer span has no messaging.message.id")
	}

	metricLabels := []telemetry.Attr{
		telemetry.String("messaging.system", "custom"), telemetry.String("messaging.destination.name", "emails"),
	}

	traced.fake.AssertCounter(t, "messaging.client.sent.messages", 1, metricLabels...)
	traced.fake.AssertCounter(t, "messaging.client.consumed.messages", 1, metricLabels...)
	traced.fake.AssertHistogram(t, "messaging.client.operation.duration", 1, metricLabels...)
	traced.fake.AssertHistogram(t, "messaging.process.duration", 1, metricLabels...)
}

// TestMemoryDriverNamesItsSystem verifies the facade discovers the memory
// driver's telemetry system name without an explicit override.
func TestMemoryDriverNamesItsSystem(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := memoryqueue.New()
	dispatcher := queue.NewDispatcher(driver, func(dispatcher *queue.DispatcherBuilder) {
		dispatcher.Telemetry(fake.Telemetry(t))
	})

	if err := dispatcher.Dispatch(t.Context(), emailJob, email{}); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "send emails", telemetry.String("messaging.system", "memory"))
}

// TestUnnamedDriverReportsOther checks a driver without a system name is
// recorded using the messaging convention's fallback value.
func TestUnnamedDriverReportsOther(t *testing.T) {
	t.Parallel()

	traced := newTracedQueue(t, func(context.Context, email) error {
		return nil
	}, "")
	traced.run(t)

	traced.fake.AssertSpan(t, "send emails", telemetry.String("messaging.system", "_OTHER"))
}

// TestFailedJobMarksConsumerSpan checks a rejected handler result marks the
// consumer span as erroneous without recording an exception event.
func TestFailedJobMarksConsumerSpan(t *testing.T) {
	t.Parallel()

	traced := newTracedQueue(t, failWith(queue.Reject(io.ErrUnexpectedEOF)), "")
	traced.run(t)

	span, _ := traced.fake.Span("process emails")

	if span.Status != telemetry.Error || len(span.Events) != 0 {
		t.Errorf("status = %v, events = %v, want an error without recorded exception", span.Status, span.Events)
	}

	traced.fake.AssertSpan(t, "process emails", telemetry.String("error.type", "*errors.errorString"))
	traced.fake.AssertCounter(t, "messaging.client.consumed.messages", 1,
		telemetry.String("error.type", "*errors.errorString"))

	if sent, _ := traced.fake.Span("send emails"); sent.Status == telemetry.Error {
		t.Error("producer span is an error")
	}
}

// TestUnknownJobIsAnError verifies an unregistered job is visible in consumer
// telemetry using the contract's unknown-name error classification.
func TestUnknownJobIsAnError(t *testing.T) {
	t.Parallel()

	traced := newTracedQueue(t, func(context.Context, email) error {
		return nil
	}, "")

	other := queue.NewJob[email]("other-job", func(job *queue.JobBuilder) {
		job.OnQueue("emails")
	})

	if err := traced.dispatcher.Dispatch(t.Context(), other, email{}); err != nil {
		t.Fatal(err)
	}

	_ = traced.driver.Drain(t.Context(), traced.consumer)

	traced.fake.AssertSpan(t, "process emails", telemetry.String("error.type", "queue.ErrUnknownName"))
}

// TestSuccessfulJobKeepsSuccessfulTelemetryWhenAcknowledgementFails separates
// the handler outcome observed by telemetry from the transport error Run returns.
func TestSuccessfulJobKeepsSuccessfulTelemetryWhenAcknowledgementFails(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: &jobDelivery{
			message: queue.Message{
				Name:    emailJob.Name(),
				Queue:   "emails",
				Payload: []byte(`{}`),
			},
			acknowledge: func(context.Context) error {
				return io.ErrClosedPipe
			},
		},
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.Telemetry(fake.Telemetry(t))
	})

	if err := consumer.Handle(emailJob, failWith(nil)); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(t.Context()); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Run = %v, want acknowledgement failure", err)
	}

	span, found := fake.Span("process emails")

	if !found {
		t.Fatal("no consumer span")
	}

	if span.Status == telemetry.Error {
		t.Fatalf("successful handler marked failed: %+v", span)
	}

	if _, found := span.Attr("error.type"); found {
		t.Fatal("broker failure replaced the successful handler outcome")
	}
}

// TestFailedSettlementKeepsHandlerTelemetryCause verifies a failed terminal
// settlement does not replace the logical failure's telemetry classification.
func TestFailedSettlementKeepsHandlerTelemetryCause(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: &jobDelivery{
			message: queue.Message{
				Name:    emailJob.Name(),
				Queue:   "emails",
				Payload: []byte(`{}`),
			},
			fail: func(context.Context, error) error {
				return context.DeadlineExceeded
			},
		},
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.Telemetry(fake.Telemetry(t))
	})

	if err := consumer.Handle(emailJob, failWith(queue.Permanent(io.ErrUnexpectedEOF))); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want settlement deadline failure", err)
	}

	fake.AssertSpan(t, "process emails", telemetry.String("error.type", "*errors.errorString"))
}

// TestDispatchFailureMarksProducerSpan checks transport errors reach the
// caller and mark producer spans and messaging counters as failures.
func TestDispatchFailureMarksProducerSpan(t *testing.T) {
	t.Parallel()

	traced := newTracedQueue(t, func(context.Context, email) error {
		return nil
	}, "")
	traced.driver.FailNext(io.ErrClosedPipe)

	if err := traced.dispatcher.Dispatch(t.Context(), emailJob, email{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("err = %v", err)
	}

	span, _ := traced.fake.Span("send emails")

	if span.Status != telemetry.Error {
		t.Errorf("status = %v, want error", span.Status)
	}

	traced.fake.AssertCounter(t, "messaging.client.sent.messages", 1, telemetry.String("error.type", "*errors.errorString"))
}

// TestTelemetryNeverRecordsPayloads checks message contents and handler error
// text do not leak into span attributes or status descriptions.
func TestTelemetryNeverRecordsPayloads(t *testing.T) {
	t.Parallel()

	traced := newTracedQueue(t, failWith(errors.New(secretAddress)), "")
	traced.run(t)

	for _, span := range traced.fake.Spans() {
		for _, attr := range span.Attrs {
			if strings.Contains(attr.Text(), secretAddress) {
				t.Errorf("span %q attribute %s leaks the payload", span.Name, attr.Key())
			}
		}

		if strings.Contains(span.StatusDescription, secretAddress) {
			t.Errorf("span %q status leaks the payload", span.Name)
		}
	}
}

// TestMessageMetadataJSON checks metadata round-trip compatibility: populated
// metadata is encoded, while empty or absent metadata remains optional.
func TestMessageMetadataJSON(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(queue.Message{
		ID:       "1",
		Metadata: map[string]string{"k": "v"},
	})

	if err != nil || !strings.Contains(string(encoded), `"metadata":{"k":"v"}`) {
		t.Fatalf("encoded = %s, %v", encoded, err)
	}

	encoded, _ = json.Marshal(queue.Message{
		ID: "1",
	})

	if strings.Contains(string(encoded), "metadata") {
		t.Errorf("empty metadata encoded: %s", encoded)
	}

	var decoded queue.Message

	if err := json.Unmarshal([]byte(`{"id":"1","name":"n","queue":"q","payload":"","attempts":0}`), &decoded); err != nil || decoded.Metadata != nil {
		t.Errorf("message without metadata = %+v, %v", decoded, err)
	}
}

// TestFollowsDefaultTelemetry checks both roles resolve a later process default
// at operation time, rather than capturing nil telemetry during construction.
func TestFollowsDefaultTelemetry(t *testing.T) {
	driver := queuetest.New()
	dispatcher := queue.NewDispatcher(driver)
	consumer := queue.NewConsumer(driver)

	if err := consumer.Handle(emailJob, func(context.Context, email) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fake := telemetrytest.New()

	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	if err := dispatcher.Dispatch(t.Context(), emailJob, email{}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Drain(t.Context(), consumer); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "send emails")
	fake.AssertSpan(t, "process emails")
}

// discardDriver accepts every message.
type discardDriver struct{}

// Dispatch accepts messages without retaining them, isolating facade overhead
// from transport work in allocation tests and benchmarks.
func (discardDriver) Dispatch(context.Context, queue.Message) error {
	return nil
}

// TestDisabledTelemetryAllocatesNothing compares disabled and unconfigured
// instrumentation, allowing only the race detector's existing pool variance.
func TestDisabledTelemetryAllocatesNothing(t *testing.T) {
	ctx := context.Background()
	plain := queue.NewDispatcher(discardDriver{}, func(dispatcher *queue.DispatcherBuilder) {
		dispatcher.Telemetry(telemetry.New(nil))
	})

	bare := queue.NewDispatcher(discardDriver{})
	value := email{
		ID: "1",
	}

	traced := measurement.AllocsPerRun(100, func() {
		_ = plain.Dispatch(ctx, emailJob, value)
	})

	baseline := measurement.AllocsPerRun(100, func() {
		_ = bare.Dispatch(ctx, emailJob, value)
	})

	// Under the race detector sync.Pool drops values at random, so the
	// pooled JSON and random-ID buffers that Dispatch uses make each
	// measurement swing by one allocation either way.
	tolerance := 0.0

	if measurement.RaceEnabled {
		tolerance = 1
	}

	if math.Abs(traced-baseline) > tolerance {
		t.Errorf("disabled telemetry changed allocations per Dispatch from %v to %v", baseline, traced)
	}
}

// BenchmarkDispatchNoTelemetry measures dispatch cost and allocations with
// instrumentation disabled and no transport work.
func BenchmarkDispatchNoTelemetry(b *testing.B) {
	dispatcher := queue.NewDispatcher(discardDriver{}, func(dispatcher *queue.DispatcherBuilder) {
		dispatcher.Telemetry(telemetry.New(nil))
	})

	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		_ = dispatcher.Dispatch(ctx, emailJob, email{
			ID: "1",
		})
	}
}

// deliveryDriver hands one fixed delivery to the handler.
type deliveryDriver struct {
	// delivery is passed unchanged to the consumer on each Consume call.
	delivery queue.Delivery
}

// Consume invokes the handler once with the fixed delivery and returns its
// result without scheduling retries or acknowledgements itself.
func (driver deliveryDriver) Consume(ctx context.Context, _ string, handler queue.DeliveryHandler) error {
	return handler(ctx, driver.delivery)
}

// BenchmarkHandleNoTelemetry measures routing, decoding, and acknowledgement
// cost with a reusable synchronous delivery and instrumentation disabled.
func BenchmarkHandleNoTelemetry(b *testing.B) {
	consumer := queue.NewConsumer(deliveryDriver{
		delivery: ackDelivery{},
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.Telemetry(telemetry.New(nil))
	})

	_ = consumer.Handle(emailJob, func(context.Context, email) error {
		return nil
	})

	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		_ = consumer.Run(ctx)
	}
}

// ackDelivery is a delivery of a valid email job that accepts settlement.
type ackDelivery struct{}

// Message returns a valid email fixture for repeated consumer benchmark runs.
// Each call supplies the same routing key and JSON payload.
func (ackDelivery) Message() queue.Message {
	return queue.Message{
		Name:    emailJob.Name(),
		Queue:   "emails",
		Payload: []byte(`{"id":"1"}`),
	}
}

// Acknowledge accepts successful settlement without retaining state so the
// benchmark can reuse the delivery.
func (ackDelivery) Acknowledge(context.Context) error {
	return nil
}

// Retry accepts retry settlement without scheduling work; benchmark handlers
// normally take the acknowledgement path.
func (ackDelivery) Retry(context.Context, time.Duration) error {
	return nil
}

// Reject accepts rejection without retaining state, completing the delivery
// contract for the benchmark fixture.
func (ackDelivery) Reject(context.Context) error {
	return nil
}

// Fail accepts terminal settlement without recording the cause, keeping the
// fixture free of transport allocations.
func (ackDelivery) Fail(context.Context, error) error {
	return nil
}
