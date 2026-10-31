package broker_test

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue"
)

// checks groups the test fixtures' fatal checks using only the standard testing
// API. Each method attributes failures to its caller through Helper.
type checks struct{}

// check is the stateless assertion fixture shared by independent tests.
var check checks

// NoError stops the calling test when an operation unexpectedly fails, keeping
// later assertions from obscuring the original error.
func (checks) NoError(t testing.TB, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

// Error stops the calling test when an operation unexpectedly succeeds; the
// error's concrete identity is not relevant to this check.
func (checks) Error(t testing.TB, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected an error")
	}
}

// ErrorIs checks an error chain against target so wrapped driver failures are
// accepted without requiring an exact error value.
func (checks) ErrorIs(t testing.TB, err, target error) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

// Equal compares complete fixture values with reflect.DeepEqual, including
// slice payloads that cannot be compared directly.
func (checks) Equal(t testing.TB, want, got any) {
	t.Helper()

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// NotEmpty verifies generated identifiers contain a value without constraining
// the driver's identifier format.
func (checks) NotEmpty(t testing.TB, value string) {
	t.Helper()

	if value == "" {
		t.Fatal("value is empty")
	}
}

// Eventually polls condition at interval until it succeeds or timeout elapses.
// It bounds asynchronous test waits using the existing wall-clock timing.
func (checks) Eventually(t testing.TB, condition func() bool, timeout, interval time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met before the deadline")
		}

		time.Sleep(interval)
	}
}

// GreaterOrEqual checks a measured duration meets its minimum, allowing
// scheduling overhead beyond the driver's requested delay.
func (checks) GreaterOrEqual(t testing.TB, got, want time.Duration) {
	t.Helper()

	if got < want {
		t.Fatalf("got %s, want at least %s", got, want)
	}
}

// True stops the calling test when a composite fixture invariant is false.
// Callers use it where the predicate already names the settlement property.
func (checks) True(t testing.TB, value bool) {
	t.Helper()

	if !value {
		t.Fatal("condition is false")
	}
}

// channelHandler is a slog.Handler that sends each record message to errors.
type channelHandler struct {
	// errors receives log messages so panic recovery can be observed by the test.
	errors chan string
}

// Enabled accepts every level.
func (channelHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

// Handle sends the record message.
func (handler channelHandler) Handle(_ context.Context, record slog.Record) error {
	handler.errors <- record.Message

	return nil
}

// WithAttrs returns the handler unchanged.
func (handler channelHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

// WithGroup returns the handler unchanged.
func (handler channelHandler) WithGroup(string) slog.Handler {
	return handler
}

// TestMemoryDriverConsumersCompeteForEachMessage verifies two consumers share
// work without duplicating a message across both subscriptions.
func TestMemoryDriverConsumersCompeteForEachMessage(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()

	check.NoError(t, driver.Start(context.Background()))
	t.Cleanup(func() {
		check.NoError(t, driver.Stop(context.Background()))
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var delivered atomic.Int64
	var consumers sync.WaitGroup

	for range 2 {
		consumers.Go(func() {
			err := driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
				delivered.Add(1)

				return delivery.Acknowledge(ctx)
			})

			check.ErrorIs(t, err, context.Canceled)
		})
	}

	for range 40 {
		check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
			Queue: "work",
		}))
	}

	check.Eventually(t, func() bool {
		return delivered.Load() == 40
	}, time.Second, time.Millisecond)
	cancel()
	consumers.Wait()
}

// TestMemoryDriverIntegratesWithConsumerMaxAttempts checks real redelivery
// stops at the job definition's attempt cap after repeated handler failures.
func TestMemoryDriverIntegratesWithConsumerMaxAttempts(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()

	check.NoError(t, driver.Start(context.Background()))
	t.Cleanup(func() {
		check.NoError(t, driver.Stop(context.Background()))
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var handled atomic.Int64
	consumer := queue.NewConsumer(driver)

	check.NoError(t, consumer.Handle(failingJob, func(context.Context, failingPayload) error {
		handled.Add(1)

		return errors.New("failed")
	}))

	worked := make(chan error, 1)

	go func() {
		worked <- consumer.Run(ctx)
	}()

	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Name:    "failing",
		Queue:   "work",
		Payload: []byte(`{"value":"x"}`),
	}))
	check.Eventually(t, func() bool {
		return handled.Load() == 2
	}, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	check.Equal(t, int64(2), handled.Load())
	cancel()

	check.ErrorIs(t, <-worked, context.Canceled)
}

// TestMemoryDriverRespectsCanceledContexts checks dispatch and consumption
// reject canceled calls with context.Canceled.
func TestMemoryDriverRespectsCanceledContexts(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()

	check.NoError(t, driver.Start(context.Background()))
	t.Cleanup(func() {
		check.NoError(t, driver.Stop(context.Background()))
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	check.ErrorIs(t, driver.Dispatch(ctx, queue.Message{
		Queue: "work",
	}), context.Canceled)
	check.ErrorIs(t, driver.Consume(ctx, "work", func(context.Context, queue.Delivery) error {
		return nil
	}), context.Canceled)
}

// TestMemoryDriverStopRejectsAdmissionsAndDiscardsDelayedJobs verifies shutdown
// rejects new work and a blocked handler cannot enqueue a delayed retry.
func TestMemoryDriverStopRejectsAdmissionsAndDiscardsDelayedJobs(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	started := make(chan struct{})
	release := make(chan struct{})
	consumer := make(chan error, 1)

	go func() {
		consumer <- driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
			close(started)
			<-release

			return delivery.Retry(ctx, time.Hour)
		})
	}()

	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Queue: "work",
	}))
	<-started

	shutdown := make(chan error, 1)

	go func() {
		shutdown <- driver.Stop(context.Background())
	}()

	check.Eventually(t, func() bool {
		return errors.Is(driver.Dispatch(context.Background(), queue.Message{
			Queue: "work",
		}), memoryqueue.ErrDriverClosed)
	}, time.Second, time.Millisecond)
	close(release)

	check.ErrorIs(t, <-consumer, memoryqueue.ErrDriverClosed)
	check.NoError(t, <-shutdown)
}

// TestMemoryDriverStopHonorsDeadline checks a blocked handler bounds shutdown
// by the caller's deadline, while a later stop can complete after release.
func TestMemoryDriverStopHonorsDeadline(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()
	started := make(chan struct{})
	release := make(chan struct{})
	consumer := make(chan error, 1)

	go func() {
		consumer <- driver.Consume(context.Background(), "work", func(context.Context, queue.Delivery) error {
			close(started)
			<-release

			return nil
		})
	}()

	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Queue: "work",
	}))
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	t.Cleanup(cancel)

	check.ErrorIs(t, driver.Stop(ctx), context.DeadlineExceeded)
	close(release)

	check.Error(t, <-consumer)
	check.NoError(t, driver.Stop(context.Background()))
}

// TestMemoryDriverStopDiscardsScheduledRetry verifies stopping a driver with a
// future retry releases its consumer without waiting for that retry to be due.
func TestMemoryDriverStopDiscardsScheduledRetry(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	scheduled := make(chan struct{})
	consumer := make(chan error, 1)

	go func() {
		consumer <- driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
			defer close(scheduled)

			return delivery.Retry(ctx, time.Hour)
		})
	}()

	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Queue: "work",
	}))
	<-scheduled

	check.NoError(t, driver.Stop(context.Background()))
	check.ErrorIs(t, <-consumer, memoryqueue.ErrDriverClosed)
}

// TestMemoryDriverRejectsInvalidConfigurationInputs checks missing queue names
// and nil delivery handlers retain their driver-specific errors.
func TestMemoryDriverRejectsInvalidConfigurationInputs(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New(func(consumer *memoryqueue.Builder) {
		consumer.RetryDelay(time.Millisecond)
	})

	check.NoError(t, driver.Start(context.Background()))
	t.Cleanup(func() {
		check.NoError(t, driver.Stop(context.Background()))
	})

	check.ErrorIs(t, driver.Dispatch(context.Background(), queue.Message{}), memoryqueue.ErrInvalidQueue)
	check.ErrorIs(t, driver.Consume(context.Background(), "", func(context.Context, queue.Delivery) error {
		return nil
	}), memoryqueue.ErrInvalidQueue)
	check.ErrorIs(t, driver.Consume(context.Background(), "work", nil), memoryqueue.ErrInvalidHandler)
}

// acknowledgeConcurrently sends two simultaneous acknowledgement results to results.
func acknowledgeConcurrently(ctx context.Context, delivery queue.Delivery, results chan<- error) {
	var calls sync.WaitGroup

	for range 2 {
		calls.Go(func() {
			results <- delivery.Acknowledge(ctx)
		})
	}

	calls.Wait()
}

// settledExactlyOnce reports whether exactly one settlement completed successfully.
func settledExactlyOnce(first, second error) bool {
	return first == nil && errors.Is(second, memoryqueue.ErrDeliverySettled) || second == nil && errors.Is(first, memoryqueue.ErrDeliverySettled)
}

// failingPayload is the payload decoded for failingJob deliveries.
type failingPayload struct {
	// Value is an arbitrary test value.
	Value string `json:"value"`
}

// failingJob routes to the work queue and allows two attempts.
var failingJob = queue.NewJob[failingPayload]("failing", func(job *queue.JobBuilder) {
	job.OnQueue("work").Retry(2)
})

// TestMemoryDriverStopWithoutStart verifies Stop does not wait for a
// scheduler that never ran.
func TestMemoryDriverStopWithoutStart(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	check.NoError(t, driver.Stop(ctx))
}

// TestMemoryDriverStartAfterStop verifies Start rejects a stopped driver.
func TestMemoryDriverStartAfterStop(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()

	check.NoError(t, driver.Stop(context.Background()))
	check.ErrorIs(t, driver.Start(context.Background()), memoryqueue.ErrDriverClosed)
}
