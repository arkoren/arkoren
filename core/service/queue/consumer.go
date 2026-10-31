package queue

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"arkoren.dev/core/service/queue/internal/execution"

	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/internal/messaging"
	"arkoren.dev/core/service/telemetry"
)

// handler decodes and handles one delivery of a registered job.
type handler struct {
	// queue is the queue the job is consumed from.
	queue string

	// attempts is the job's total allowed attempts. Zero uses the driver
	// default.
	attempts int

	// backoff is the job's retry delay. Nil uses the driver default.
	backoff func(attempt int) time.Duration

	// timeout bounds one attempt. Zero means no limit.
	timeout time.Duration

	// run decodes the payload and calls the application handler. A
	// decoding failure is returned as a [decodeError].
	run func(context.Context, []byte) error
}

// decodeError marks a payload that does not decode into the job type.
// Redelivery cannot fix it, so the consumer fails the delivery.
type decodeError struct {
	// cause is the JSON decoding error.
	cause error
}

// Error returns the decoding failure message.
func (decodeError decodeError) Error() string {
	return fmt.Sprintf("decode job payload: %v", decodeError.cause)
}

// Unwrap returns the JSON decoding error.
func (decodeError decodeError) Unwrap() error {
	return decodeError.cause
}

// Consumer decodes, handles, and settles deliveries received through a
// [ConsumerDriver].
// It owns registration and per-delivery policy, not native driver lifecycle.
//
//	consumer := queue.NewConsumer(driver)
//	if err := consumer.Handle(job, send); err != nil {
//		return err
//	}
//	compose.Run(consumer.Run)
type Consumer struct {
	// driver receives broker deliveries for the consumer.
	driver ConsumerDriver

	// config holds the telemetry settings.
	config settings

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[messaging.Instruments]

	// mu guards handlers.
	mu sync.RWMutex

	// handlers maps job names to their handlers.
	handlers map[string]handler
}

// NewConsumer creates a [Consumer] that consumes through driver. Register
// handlers with [Consumer.Handle]. Callbacks configure its telemetry: every
// delivery attempt emits a consumer span "process {queue}", parented to the
// trace context in [Message.Metadata], and the
// messaging.client.consumed.messages and messaging.process.duration metrics
// when the telemetry is enabled. It costs nothing otherwise.
//
// Callbacks run directly once, synchronously in order on one fresh draft. Nil
// entries panic; expanded nil or empty slices omit configuration. Construction
// performs no I/O. Publication detaches the draft and queue restrictions, but
// telemetry and event buses remain shared services. Nil telemetry resolves
// telemetry.Default at each operation; nil events publish nothing.
//
//	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
//		consumer.Telemetry(app.Telemetry())
//	})
func NewConsumer(driver ConsumerDriver, configure ...ConsumerBuilderFunc) *Consumer {
	consumer := ConsumerBuilder{}

	for _, configure := range configure {
		configure(&consumer)
	}

	config := consumer.settings
	config.Queues = slices.Clone(config.Queues)

	return &Consumer{
		driver:      driver,
		config:      config,
		instruments: telemetry.NewInstruments(newInstruments),
		handlers:    map[string]handler{},
	}
}

// Handle registers fn as the handler of job. A nil result acknowledges the
// delivery. An ordinary error requests retry according to the job's
// [JobBuilder.Retry] and [JobBuilder.Backoff] settings. Use [RetryAfter], [Reject], or
// [Permanent] to request an explicit settlement. An invalid job returns
// [ErrInvalidName] wrapping [Job.Err]; registering one name twice returns
// [ErrDuplicateName]. Run discovers handled queues once at startup. A handler
// added later can handle subsequent deliveries on a queue already being consumed;
// a new queue is not consumed until the next Run. A nil fn returns an error without
// registering the job.
//
//	job := queue.NewJob[string]("send")
//	if err := consumer.Handle(job, func(ctx context.Context, text string) error {
//		return sendText(ctx, text)
//	}); err != nil {
//		return err
//	}
func (consumer *Consumer) Handle[T any](job Job[T], fn func(context.Context, T) error) error {
	if err := job.Err(); err != nil {
		return fmt.Errorf("handle %s: %w: %w", job.name, ErrInvalidName, err)
	}

	if fn == nil {
		return fmt.Errorf("handle %s: nil handler", job.name)
	}

	consumer.mu.Lock()
	defer consumer.mu.Unlock()

	if _, found := consumer.handlers[job.name]; found {
		return fmt.Errorf("handle %s: %w", job.name, ErrDuplicateName)
	}

	consumer.handlers[job.name] = handler{
		queue:    job.queue,
		attempts: job.attempts,
		backoff:  job.backoff,
		timeout:  job.timeout,
		run: func(ctx context.Context, raw []byte) error {
			var payload T

			if err := json.Unmarshal(raw, &payload); err != nil {
				return decodeError{
					cause: err,
				}
			}

			return fn(ctx, payload)
		},
	}

	return nil
}

// Driver returns the underlying [ConsumerDriver].
// Raw consumption bypasses the facade's typed handlers and outcome events.
//
//	consumer := queue.NewConsumer(driver)
//	fmt.Println(consumer.Driver() == driver)
func (consumer *Consumer) Driver() ConsumerDriver {
	return consumer.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() ConsumerDriver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver.
//
//	if driver, ok := consumer.DriverAs[*memoryqueue.Driver](); ok {
//		fmt.Printf("memory consumption: %T\n", driver)
//	}
func (consumer *Consumer) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](consumer.Driver())
}

// Run consumes and handles deliveries from every queue that has a handled
// job, with one [ConsumerDriver.Consume] call per queue, until ctx is
// canceled or any consume fails. The first failure cancels the others and is
// returned once they all stop; cancellation returns the error the driver
// reports for it. Use [ConsumerBuilder.Queues] to restrict the queues. It returns
// [ErrNoQueues] when there is nothing to consume.
//
// Register a runner during application composition:
//
//	compose.Run(consumer.Run)
func (consumer *Consumer) Run(ctx context.Context) (err error) {
	consumer.mu.RLock()

	handled := map[string]bool{}

	for _, registered := range consumer.handlers {
		handled[registered.queue] = true
	}

	consumer.mu.RUnlock()

	queues := slices.Sorted(maps.Keys(handled))

	if len(consumer.config.Queues) > 0 {
		queues = slices.DeleteFunc(queues, func(queue string) bool {
			return !slices.Contains(consumer.config.Queues, queue)
		})
	}

	if len(queues) == 0 {
		return ErrNoQueues
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		group sync.WaitGroup
		once  sync.Once
	)

	for _, queue := range queues {
		group.Go(func() {
			// e avoids shadowing Run's named err, which retains the first
			// driver failure until every queue consumer has stopped.
			e := consumer.driver.Consume(ctx, queue, consumer.handle)

			if e == nil {
				return
			}

			once.Do(func() {
				err = e
			})
			cancel()
		})
	}

	group.Wait()

	return err
}

// handle decodes, executes, and settles one delivery. A job interrupted by
// consumer shutdown bypasses the immediate attempt cap when retried. Reported
// delivery counts still advance. A panic is settled like any other job error.
// The named e result is the settlement failure returned to the driver; local err
// remains the logical outcome reported to telemetry. The short named alternative
// keeps both errors available without shadowing the execution result.
func (consumer *Consumer) handle(ctx context.Context, delivery Delivery) (e error) {
	message := delivery.Message()

	ctx, op := messaging.Process(ctx, cmp.Or(consumer.config.Telemetry, telemetry.Default()), consumer.instruments, messaging.Target{
		Driver:      consumer.driver,
		System:      consumer.config.System,
		Destination: message.Queue,
		ID:          message.ID,
		Job:         message.Name,
		Attempt:     message.Attempts,
		Metadata:    message.Metadata,
	})

	err, e := consumer.process(ctx, delivery, message)
	op.End(ctx, err, errorSentinels...)

	return e
}

// process adapts public deliveries and captured handlers to the private pipeline.
func (consumer *Consumer) process(ctx context.Context, delivery Delivery, message Message) (err, e error) {
	consumer.mu.RLock()

	registered, found := consumer.handlers[message.Name]
	consumer.mu.RUnlock()

	return execution.Process(ctx, delivery, message.Name, message.Payload, message.Attempts,
		found, consumer.config.UnknownNameAttempts,
		execution.Handler{Attempts: registered.attempts, Backoff: registered.backoff, Timeout: registered.timeout, Run: registered.run},
		execution.Classification{
			Unknown:   func(name string) error { return fmt.Errorf("%w: %s", ErrUnknownName, name) },
			Decode:    func(err error) (error, bool) { failure, ok := errors.AsType[decodeError](err); return failure, ok },
			Rejected:  func(err error) bool { _, ok := errors.AsType[*RejectedError](err); return ok },
			Permanent: func(err error) bool { return errors.Is(err, ErrPermanent) },
			RetryAfter: func(err error) (time.Duration, bool) {
				delayed, ok := errors.AsType[*RetryAfterError](err)

				if !ok {
					return 0, false
				}

				return delayed.RetryAfter(), true
			},
		}, consumer.notifications(delivery, message, registered.timeout))
}
