package queue

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"fmt"

	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/internal/messaging"
	"arkoren.dev/core/service/telemetry"
)

// Dispatcher JSON-encodes job payloads and dispatches their transport
// envelopes through a [DispatcherDriver].
// It does not own native driver lifecycle or start a consumer.
//
//	dispatcher := queue.NewDispatcher(driver)
//	if err := dispatcher.Dispatch(ctx, queue.NewJob[string]("send"), "hello"); err != nil {
//		return err
//	}
type Dispatcher struct {
	// driver accepts the dispatched messages.
	driver DispatcherDriver

	// config holds the telemetry settings.
	config settings

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[messaging.Instruments]
}

// NewDispatcher creates a [Dispatcher] that delegates to driver. Callbacks
// configure its telemetry: every dispatch emits a producer span "send {queue}"
// and the messaging.client.sent.messages and
// messaging.client.operation.duration metrics, and injects the trace context
// into [Message.Metadata], when the telemetry is enabled. It costs nothing
// otherwise. Payloads and metadata values are never recorded.
//
// Callbacks run directly once, synchronously in order on one fresh draft. Nil
// entries panic; expanded nil or empty slices omit configuration. Publication
// detaches the draft; telemetry remains a shared service, resolved dynamically
// through telemetry.Default when nil. Construction performs no I/O.
//
//	dispatcher := queue.NewDispatcher(driver, func(dispatcher *queue.DispatcherBuilder) {
//		dispatcher.Telemetry(app.Telemetry())
//	})
func NewDispatcher(driver DispatcherDriver, configure ...DispatcherBuilderFunc) *Dispatcher {
	dispatcher := DispatcherBuilder{}

	for _, configure := range configure {
		configure(&dispatcher)
	}

	return &Dispatcher{
		driver:      driver,
		config:      dispatcher.settings,
		instruments: telemetry.NewInstruments(newInstruments),
	}
}

// Driver returns the underlying [DispatcherDriver].
// Raw calls bypass typed encoding and facade telemetry.
//
//	dispatcher := queue.NewDispatcher(driver)
//	fmt.Println(dispatcher.Driver() == driver)
func (dispatcher *Dispatcher) Driver() DispatcherDriver {
	return dispatcher.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() DispatcherDriver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver.
//
//	if driver, ok := dispatcher.DriverAs[*memoryqueue.Driver](); ok {
//		fmt.Printf("memory dispatch: %T\n", driver)
//	}
func (dispatcher *Dispatcher) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](dispatcher.Driver())
}

// Dispatch JSON-encodes payload and sends it to the queue of job with a
// new random message ID. It returns [ErrInvalidName], wrapping [Job.Err], for
// an invalid job. With enabled telemetry the message carries the trace
// context of the dispatch span in [Message.Metadata].
// Encoding errors and driver failures are returned without losing their causes;
// a successful call confirms acceptance, not handler completion. Invalid jobs
// are rejected before encoding. The facade does not start or stop the driver.
//
//	job := queue.NewJob[string]("send")
//	if err := dispatcher.Dispatch(ctx, job, "hello"); err != nil {
//		return err
//	}
func (dispatcher *Dispatcher) Dispatch[T any](ctx context.Context, job Job[T], payload T) error {
	if err := job.Err(); err != nil {
		return fmt.Errorf("dispatch %s: %w: %w", job.name, ErrInvalidName, err)
	}

	encoded, err := json.Marshal(payload)

	if err != nil {
		return fmt.Errorf("marshal job payload: %w", err)
	}

	message := Message{
		ID:      rand.Text(),
		Name:    job.name,
		Queue:   job.queue,
		Payload: encoded,
	}

	ctx, metadata, op := messaging.Send(ctx, cmp.Or(dispatcher.config.Telemetry, telemetry.Default()), dispatcher.instruments, messaging.Target{
		Driver:      dispatcher.driver,
		System:      dispatcher.config.System,
		Destination: message.Queue,
		ID:          message.ID,
		Job:         message.Name,
	})
	message.Metadata = metadata

	err = dispatcher.driver.Dispatch(ctx, message)
	op.End(ctx, err, errorSentinels...)

	return err
}
