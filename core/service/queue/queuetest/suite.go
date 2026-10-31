package queuetest

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/queue"
)

const (
	// eventTimeout bounds the wait for an expected delivery.
	eventTimeout = 20 * time.Second

	// quietPeriod is how long the suite watches for deliveries that must not
	// happen.
	quietPeriod = 400 * time.Millisecond

	// retryDelay is the delay requested to test delayed retries.
	retryDelay = time.Second

	// defaultQueue and defaultOther name the queues used when [Driver] names none.
	defaultQueue = "conformance"

	// defaultOther is the second queue used when [Driver] names none.
	defaultOther = "conformance-other"
)

// Driver is the system under test of [Run]: the two driver interfaces of the
// queue contract, usually implemented by one value.
type Driver struct {
	// Dispatcher accepts dispatched messages.
	Dispatcher queue.DispatcherDriver

	// Consumer receives them. It must read the queues Dispatcher writes.
	Consumer queue.ConsumerDriver

	// Queue names an empty queue the suite may use. Drivers that must
	// provision or map queue names, such as SQS, set it to a queue created for
	// the test. The default is "conformance".
	Queue string

	// Other names a second empty queue, distinct from Queue. The default is
	// "conformance-other".
	Other string
}

// Option declares a capability the driver under test lacks, so the suite skips
// the behavior that depends on it. Options never change what a behavior
// means.
type Option func(*suiteConfig)

// suiteConfig holds the capabilities declared by [Option] values.
type suiteConfig struct {
	// synctest runs every subtest in a testing/synctest bubble.
	synctest bool

	// noRetryDelay skips the delayed-retry behavior.
	noRetryDelay bool
}

// WithSynctest runs every subtest inside a testing/synctest bubble, so waits
// take no real time and the retry delay is checked exactly. newDriver is
// called inside the bubble. Use it for drivers that live in the process and
// use the time package, such as the memory driver. Never use it with a driver
// that talks to an external system.
//
// # Examples
//
//	queuetest.Run(t, func(t *testing.T) queuetest.Driver {
//		driver := memoryqueue.New()
//		return queuetest.Driver{Dispatcher: driver, Consumer: driver}
//	}, queuetest.WithSynctest())
func WithSynctest() Option {
	return func(config *suiteConfig) {
		config.synctest = true
	}
}

// WithoutRetryDelay declares that the driver cannot delay a retry, as the
// contract allows for [queue.RetryAfterError]. The suite then skips the
// delayed-retry check.
func WithoutRetryDelay() Option {
	return func(config *suiteConfig) {
		config.noRetryDelay = true
	}
}

// Run is the conformance suite for queue drivers. It runs one subtest per
// behavior documented on [queue.DispatcherDriver], [queue.ConsumerDriver],
// and [queue.Delivery], plus the end-to-end behavior of [queue.Consumer] on
// top of the driver. newDriver is called once per subtest and must return a
// driver on empty queues, registering any cleanup with t.
//
// Unless [WithSynctest] is given, the suite uses real time with delays of
// about a second, so infrastructure backed drivers should run it only outside
// short mode.
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
			"DispatchDeliversMessage",
			testDeliversMessage,
		},
		{
			"DispatchPreservesMetadata",
			testPreservesMetadata,
		},
		{
			"DispatchWithoutMetadataDeliversNone",
			testNoMetadata,
		},
		{
			"DispatchRejectsInvalidInput",
			testRejectsInvalidInput,
		},
		{
			"QueuesAreIsolated",
			testQueuesAreIsolated,
		},
		{
			"AcknowledgedIsNotRedelivered",
			testAcknowledgedIsNotRedelivered,
		},
		{
			"RetryRedelivers",
			testRetryRedelivers,
		},
		{
			"RetryAfterWaitsForDelay",
			testRetryAfterWaitsForDelay,
		},
		{
			"FailIsNotRedelivered",
			testFailIsNotRedelivered,
		},
		{
			"RejectSettles",
			testRejectSettles,
		},
		{
			"SettlesOnce",
			testSettlesOnce,
		},
		{
			"ConsumersCompete",
			testConsumersCompete,
		},
		{
			"ConsumeStopsOnCancel",
			testConsumeStopsOnCancel,
		},
		{
			"ConsumerHandlesJob",
			testConsumerHandlesJob,
		},
		{
			"ConsumerRetriesHandlerError",
			testConsumerRetries,
		},
		{
			"ConsumerStopsAtMaxAttempts",
			testConsumerMaxAttempts,
		},
		{
			"ConsumerPermanentIsTerminal",
			testConsumerPermanent,
		},
		{
			"ConsumerRejectIsTerminal",
			testConsumerReject,
		},
		{
			"ConsumerFailsUndecodablePayload",
			testConsumerUndecodable,
		},
	}

	for _, subtest := range subtests {
		t.Run(subtest.name, func(t *testing.T) {
			if config.synctest {
				synctest.Test(t, func(t *testing.T) {
					subtest.run(newHarness(t, newDriver, config))
				})

				return
			}

			subtest.run(newHarness(t, newDriver, config))
		})
	}
}

// harness carries the state of one subtest.
type harness struct {
	// t is the subtest.
	t *testing.T

	// driver is the driver under test.
	driver Driver

	// config holds the declared capabilities.
	config suiteConfig

	// ctx bounds the whole subtest.
	ctx context.Context
}

// newHarness builds the driver for one subtest.
func newHarness(t *testing.T, newDriver func(t *testing.T) Driver, config suiteConfig) *harness {
	t.Helper()

	driver := newDriver(t)

	if driver.Dispatcher == nil || driver.Consumer == nil {
		t.Fatal("newDriver returned a Driver without Dispatcher or Consumer")
	}

	driver.Queue = cmp.Or(driver.Queue, defaultQueue)
	driver.Other = cmp.Or(driver.Other, defaultOther)

	ctx, cancel := context.WithTimeout(context.Background(), 3*eventTimeout)
	t.Cleanup(cancel)

	return &harness{
		t:      t,
		driver: driver,
		config: config,
		ctx:    ctx,
	}
}

// job is the payload of the typed jobs of the suite.
type job struct {
	// N distinguishes jobs.
	N int `json:"n"`
}

// message returns a new message for queueName with a unique ID.
func (harness *harness) message(queueName string) queue.Message {
	return queue.Message{
		ID:      rand.Text(),
		Name:    "conformance-job",
		Queue:   queueName,
		Payload: []byte(`{"n":1,"nonce":"` + rand.Text() + `","text":"héllo"}`),
	}
}

// dispatch dispatches message and fails the subtest on error.
func (harness *harness) dispatch(message queue.Message) {
	harness.t.Helper()

	if err := harness.driver.Dispatcher.Dispatch(harness.ctx, message); err != nil {
		harness.t.Fatalf("Dispatch(%s): %v", message.ID, err)
	}
}

// consume runs driver.Consume on queueName until the subtest ends and
// returns a function that stops it and returns its error.
func (harness *harness) consume(queueName string, handler queue.DeliveryHandler) (stop func() error) {
	harness.t.Helper()

	ctx, cancel := context.WithCancel(harness.ctx)
	result := make(chan error, 1)

	go func() {
		result <- harness.driver.Consumer.Consume(ctx, queueName, handler)
	}()

	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()

			select {
			case err = <-result:
			case <-time.After(eventTimeout):
				harness.t.Error("Consume did not return after its context was canceled")
			}
		})

		return err
	}
	harness.t.Cleanup(func() {
		_ = stop()
	})

	return stop
}

// work runs consumer.Run until the subtest ends.
func (harness *harness) work(consumer *queue.Consumer) {
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

// receive waits for the next value on received.
func receive[T any](h *harness, received <-chan T) T {
	h.t.Helper()

	select {
	case value := <-received:
		return value
	case <-time.After(eventTimeout):
		h.t.Fatal("timed out waiting for a delivery")
		panic("unreachable")
	}
}

// expectQuiet fails the subtest if anything arrives on received within the
// quiet period.
func expectQuiet[T any](h *harness, received <-chan T, what string) {
	h.t.Helper()

	select {
	case value := <-received:
		h.t.Errorf("unexpected %s: %v", what, value)
	case <-time.After(quietPeriod):
	}
}

// ackAndRecord returns a handler that records each message and acknowledges it.
func ackAndRecord(received chan<- queue.Message) queue.DeliveryHandler {
	return func(ctx context.Context, delivery queue.Delivery) error {
		received <- delivery.Message()

		return delivery.Acknowledge(ctx)
	}
}

// testDeliversMessage checks that a dispatched message arrives intact.
func testDeliversMessage(h *harness) {
	message := h.message(h.driver.Queue)
	received := make(chan queue.Message, 8)
	h.consume(h.driver.Queue, ackAndRecord(received))
	h.dispatch(message)

	got := receive(h, received)

	if got.ID == "" || got.Name != message.Name || got.Queue != message.Queue {
		h.t.Errorf("delivered %+v, want an ID, name %q, queue %q", got, message.Name, message.Queue)
	}

	if !bytes.Equal(got.Payload, message.Payload) {
		h.t.Errorf("payload = %s, want %s", got.Payload, message.Payload)
	}

	if got.Attempts != 0 && got.Attempts != 1 {
		h.t.Errorf("first delivery reports %d attempts, want 1 or unavailable (0)", got.Attempts)
	}
}

// metadataCases returns metadata that every driver must carry unchanged: the
// W3C trace context keys, the limits of [queue.Message.Metadata], and awkward
// values.
func metadataCases() map[string]string {
	metadata := map[string]string{
		"traceparent":      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"tracestate":       "vendor=opaque,other=1",
		"Mixed.Case_key-1": "Wert mit Überlänge 世界 \"quotes\" and\nnewline",
		"empty":            "",
	}

	for index := range queue.MaxMetadataEntries - len(metadata) {
		metadata[fmt.Sprintf("k%02d", index)] = strings.Repeat("v", 100)
	}

	return metadata
}

// testPreservesMetadata checks that Metadata survives transport unchanged.
func testPreservesMetadata(h *harness) {
	want := metadataCases()
	message := h.message(h.driver.Queue)
	message.Metadata = maps.Clone(want)
	received := make(chan queue.Message, 8)
	h.consume(h.driver.Queue, ackAndRecord(received))
	h.dispatch(message)

	if got := receive(h, received); !maps.Equal(got.Metadata, want) {
		h.t.Errorf("delivered metadata %v, want %v", got.Metadata, want)
	}

	if !maps.Equal(message.Metadata, want) {
		h.t.Error("Dispatch modified the metadata of the caller")
	}
}

// testNoMetadata checks that a message without Metadata arrives without any.
func testNoMetadata(h *harness) {
	received := make(chan queue.Message, 8)
	h.consume(h.driver.Queue, ackAndRecord(received))
	h.dispatch(h.message(h.driver.Queue))

	if got := receive(h, received); len(got.Metadata) != 0 {
		h.t.Errorf("delivered metadata %v, want none", got.Metadata)
	}
}

// testRejectsInvalidInput checks Dispatch and Consume input validation.
func testRejectsInvalidInput(h *harness) {
	if err := h.driver.Dispatcher.Dispatch(h.ctx, queue.Message{
		ID:   rand.Text(),
		Name: "conformance-job",
	}); err == nil {
		h.t.Error("Dispatch without a queue succeeded")
	}

	canceled, cancel := context.WithCancel(h.ctx)
	cancel()

	if err := h.driver.Dispatcher.Dispatch(canceled, h.message(h.driver.Queue)); !errors.Is(err, context.Canceled) {
		h.t.Errorf("Dispatch with a canceled context = %v, want context.Canceled", err)
	}

	handler := func(context.Context, queue.Delivery) error {
		return nil
	}

	if err := h.driver.Consumer.Consume(canceled, h.driver.Queue, handler); !errors.Is(err, context.Canceled) {
		h.t.Errorf("Consume with a canceled context = %v, want context.Canceled", err)
	}
}

// testQueuesAreIsolated checks that consumers see only their own queue.
func testQueuesAreIsolated(h *harness) {
	other, own := h.message(h.driver.Other), h.message(h.driver.Queue)
	received := make(chan queue.Message, 8)
	h.consume(h.driver.Queue, ackAndRecord(received))
	h.dispatch(other)
	h.dispatch(own)

	if got := receive(h, received); !bytes.Equal(got.Payload, own.Payload) {
		h.t.Errorf("received %s, want %s from its own queue", got.Payload, own.Payload)
	}

	expectQuiet(h, received, "delivery from another queue")
}

// testAcknowledgedIsNotRedelivered checks that acknowledgement is final.
func testAcknowledgedIsNotRedelivered(h *harness) {
	first, second := h.message(h.driver.Queue), h.message(h.driver.Queue)
	received := make(chan queue.Message, 8)
	h.consume(h.driver.Queue, ackAndRecord(received))
	h.dispatch(first)
	receive(h, received)
	h.dispatch(second)

	if got := receive(h, received); !bytes.Equal(got.Payload, second.Payload) {
		h.t.Errorf("received %s again, want only %s", got.Payload, second.Payload)
	}

	expectQuiet(h, received, "redelivery after acknowledgement")
}

// retryOnce returns a handler that records each delivery, retries the first
// with delay, and acknowledges later ones.
func retryOnce(received chan<- queue.Message, delay time.Duration) queue.DeliveryHandler {
	var calls atomic.Int32

	return func(ctx context.Context, delivery queue.Delivery) error {
		received <- delivery.Message()

		if calls.Add(1) == 1 {
			return delivery.Retry(ctx, delay)
		}

		return delivery.Acknowledge(ctx)
	}
}

// testRetryRedelivers checks that an immediate retry delivers the same message again.
func testRetryRedelivers(h *harness) {
	message := h.message(h.driver.Queue)
	received := make(chan queue.Message, 8)
	h.consume(h.driver.Queue, retryOnce(received, 0))
	h.dispatch(message)

	first, second := receive(h, received), receive(h, received)

	if second.ID != first.ID || !bytes.Equal(second.Payload, message.Payload) {
		h.t.Errorf("retried delivery = %+v, want the original message %+v", second, first)
	}

	if first.Attempts != 0 && second.Attempts != first.Attempts+1 {
		h.t.Errorf("attempts = %d then %d, want an increment", first.Attempts, second.Attempts)
	}

	expectQuiet(h, received, "delivery after the retry was acknowledged")
}

// testRetryAfterWaitsForDelay checks that a delayed retry is not delivered early.
func testRetryAfterWaitsForDelay(h *harness) {
	if h.config.noRetryDelay {
		h.t.Skip("driver cannot delay retries")
	}

	received := make(chan queue.Message, 8)
	h.consume(h.driver.Queue, retryOnce(received, retryDelay))
	h.dispatch(h.message(h.driver.Queue))

	receive(h, received)

	retried := time.Now()
	receive(h, received)

	elapsed := time.Since(retried)

	if h.config.synctest && elapsed != retryDelay {
		h.t.Errorf("retry delivered after %s, want exactly %s", elapsed, retryDelay)
	}

	if elapsed < retryDelay-retryDelay/10 {
		h.t.Errorf("retry delivered after %s, want at least %s", elapsed, retryDelay)
	}
}

// testFailIsNotRedelivered checks that failure is terminal.
func testFailIsNotRedelivered(h *harness) {
	received := make(chan queue.Message, 8)
	h.consume(h.driver.Queue, func(ctx context.Context, delivery queue.Delivery) error {
		received <- delivery.Message()

		return delivery.Fail(ctx, errors.New("conformance failure"))
	})
	h.dispatch(h.message(h.driver.Queue))

	receive(h, received)
	expectQuiet(h, received, "redelivery after failure")
}

// testRejectSettles checks that Reject settles and leaves the consumer working.
func testRejectSettles(h *harness) {
	rejected := h.message(h.driver.Queue)
	received := make(chan queue.Message, 8)
	h.consume(h.driver.Queue, func(ctx context.Context, delivery queue.Delivery) error {
		received <- delivery.Message()

		if !bytes.Equal(delivery.Message().Payload, rejected.Payload) {
			return delivery.Acknowledge(ctx)
		}

		return delivery.Reject(ctx)
	})
	h.dispatch(rejected)
	receive(h, received)

	next := h.message(h.driver.Queue)
	h.dispatch(next)

	for got := receive(h, received); !bytes.Equal(got.Payload, next.Payload); got = receive(h, received) {
		// Brokers that redrive rejected messages may deliver it again first.
	}
}

// testSettlesOnce checks that a delivery cannot be settled twice.
func testSettlesOnce(h *harness) {
	results := make(chan error, 1)
	h.consume(h.driver.Queue, func(ctx context.Context, delivery queue.Delivery) error {
		if err := delivery.Acknowledge(ctx); err != nil {
			return err
		}

		results <- delivery.Acknowledge(ctx)

		return nil
	})
	h.dispatch(h.message(h.driver.Queue))

	if err := receive(h, results); err == nil {
		h.t.Error("second settlement succeeded, want an error")
	}
}

// testConsumersCompete checks that concurrent consumers share a queue.
func testConsumersCompete(h *harness) {
	const total = 10

	received := make(chan queue.Message, 4*total)
	h.consume(h.driver.Queue, ackAndRecord(received))
	h.consume(h.driver.Queue, ackAndRecord(received))

	sent := map[string]bool{}

	for range total {
		message := h.message(h.driver.Queue)
		sent[string(message.Payload)] = true
		h.dispatch(message)
	}

	for len(sent) > 0 {
		got := receive(h, received)
		delete(sent, string(got.Payload))
	}
}

// testConsumeStopsOnCancel checks that cancellation ends Consume with the context error.
func testConsumeStopsOnCancel(h *harness) {
	stop := h.consume(h.driver.Queue, ackAndRecord(make(chan queue.Message, 1)))
	time.Sleep(quietPeriod / 4)

	if err := stop(); !errors.Is(err, context.Canceled) {
		h.t.Errorf("Consume returned %v after cancellation, want context.Canceled", err)
	}
}

// typedJob returns the typed job of the suite on queueName, allowing attempts
// attempts (zero for the driver default).
func typedJob(queueName string, attempts int) queue.Job[job] {
	return queue.NewJob[job]("conformance-job", func(job *queue.JobBuilder) {
		job.OnQueue(queueName).Retry(attempts)
	})
}

// startConsumer registers fn for definition and works its queue until the subtest ends.
func startConsumer(h *harness, definition queue.Job[job], fn func(context.Context, job) error) {
	h.t.Helper()

	consumer := queue.NewConsumer(h.driver.Consumer)

	if err := consumer.Handle(definition, fn); err != nil {
		h.t.Fatal(err)
	}

	h.work(consumer)
}

// dispatchJob dispatches a typed job through a [queue.Dispatcher].
func dispatchJob(h *harness, definition queue.Job[job]) {
	h.t.Helper()

	if err := queue.NewDispatcher(h.driver.Dispatcher).Dispatch(h.ctx, definition, job{
		N: 7,
	}); err != nil {
		h.t.Fatal(err)
	}
}

// countCalls returns a handler returning results[i] for call i and the last
// result afterwards, and the channel of call numbers.
func countCalls(results ...error) (func(context.Context, job) error, <-chan int) {
	calls := make(chan int, 64)
	var count atomic.Int32

	return func(_ context.Context, payload job) error {
		call := int(count.Add(1))
		calls <- call

		if payload.N != 7 {
			return fmt.Errorf("unexpected payload %+v", payload)
		}

		return results[min(call, len(results))-1]
	}, calls
}

// testConsumerHandlesJob checks that a successful handler acknowledges the job.
func testConsumerHandlesJob(h *harness) {
	definition := typedJob(h.driver.Queue, 0)
	handler, calls := countCalls(nil)
	startConsumer(h, definition, handler)
	dispatchJob(h, definition)

	receive(h, calls)
	expectQuiet(h, calls, "second handler call after success")
}

// testConsumerRetries checks that a handler error leads to another attempt.
func testConsumerRetries(h *harness) {
	definition := typedJob(h.driver.Queue, 0)
	handler, calls := countCalls(errors.New("transient"), nil)
	startConsumer(h, definition, handler)
	dispatchJob(h, definition)

	receive(h, calls)
	receive(h, calls)
	expectQuiet(h, calls, "handler call after the retry succeeded")
}

// testConsumerMaxAttempts checks that JobBuilder.Retry bounds attempts.
func testConsumerMaxAttempts(h *harness) {
	h.skipUnlessAttemptsReported()

	definition := typedJob(h.driver.Queue, 2)
	handler, calls := countCalls(errors.New("always"))
	startConsumer(h, definition, handler)
	dispatchJob(h, definition)

	receive(h, calls)
	receive(h, calls)
	expectQuiet(h, calls, "attempt beyond JobBuilder.Retry")
}

// skipUnlessAttemptsReported skips the subtest for drivers that leave
// Message.Attempts at zero, since the contract enforces MaxAttempts only for
// drivers that report it.
func (harness *harness) skipUnlessAttemptsReported() {
	harness.t.Helper()

	received := make(chan queue.Message, 1)
	stop := harness.consume(harness.driver.Other, ackAndRecord(received))
	harness.dispatch(harness.message(harness.driver.Other))

	attempts := receive(harness, received).Attempts
	_ = stop()

	if attempts == 0 {
		harness.t.Skip("driver does not report Message.Attempts")
	}
}

// testConsumerPermanent checks that Permanent settles terminally.
func testConsumerPermanent(h *harness) {
	definition := typedJob(h.driver.Queue, 0)
	handler, calls := countCalls(queue.Permanent(errors.New("fatal")))
	startConsumer(h, definition, handler)
	dispatchJob(h, definition)

	receive(h, calls)
	expectQuiet(h, calls, "handler call after Permanent")
}

// testConsumerReject checks that Reject settles terminally.
func testConsumerReject(h *harness) {
	definition := typedJob(h.driver.Queue, 0)
	handler, calls := countCalls(queue.Reject(errors.New("unwanted")))
	startConsumer(h, definition, handler)
	dispatchJob(h, definition)

	receive(h, calls)
	expectQuiet(h, calls, "handler call after Reject")
}

// testConsumerUndecodable checks that an undecodable payload fails without
// reaching the handler and does not block later jobs.
func testConsumerUndecodable(h *harness) {
	definition := typedJob(h.driver.Queue, 0)
	handler, calls := countCalls(nil)
	startConsumer(h, definition, handler)

	broken := h.message(h.driver.Queue)
	broken.Payload = []byte(`"not an object"`)
	h.dispatch(broken)
	dispatchJob(h, definition)

	if call := receive(h, calls); call != 1 {
		h.t.Fatalf("handler call %d first, want call 1 for the valid job", call)
	}

	expectQuiet(h, calls, "handler call for the undecodable payload")
}
