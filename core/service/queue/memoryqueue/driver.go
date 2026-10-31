package memoryqueue

import (
	"context"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue/internal/broker"
)

var (
	// ErrDriverClosed rejects startup or queue admission after shutdown begins.
	// Already-canceled contexts can take precedence at admission boundaries.
	//
	//	if errors.Is(err, memoryqueue.ErrDriverClosed) {
	//		return err
	//	}
	ErrDriverClosed = broker.ErrDriverClosed

	// ErrInvalidQueue is returned when a queue name is empty.
	//
	//	err := driver.Dispatch(ctx, queue.Message{})
	//	fmt.Println(errors.Is(err, memoryqueue.ErrInvalidQueue))
	ErrInvalidQueue = broker.ErrInvalidQueue

	// ErrInvalidHandler is returned when Consume is given a nil handler.
	//
	//	err := driver.Consume(ctx, "work", nil)
	//	fmt.Println(errors.Is(err, memoryqueue.ErrInvalidHandler))
	ErrInvalidHandler = broker.ErrInvalidHandler

	// ErrDeliverySettled is returned when a delivery is settled more than once.
	// It also rejects a concurrent settlement already in progress.
	//
	//	if err := delivery.Acknowledge(ctx); errors.Is(err, memoryqueue.ErrDeliverySettled) {
	//		return err
	//	}
	ErrDeliverySettled = broker.ErrDeliverySettled

	// ErrDeliveryUnsettled is returned when a handler returns without settling a delivery.
	// The driver attempts terminal cleanup before ending Consume with this error.
	//
	//	if errors.Is(err, memoryqueue.ErrDeliveryUnsettled) {
	//		return err
	//	}
	ErrDeliveryUnsettled = broker.ErrDeliveryUnsettled
)

// Driver implements [queue.DispatcherDriver] and [queue.ConsumerDriver] with
// in-memory data structures. It is non-durable: process exit or crash loses
// queued jobs. A Driver is safe for concurrent use.
// Create it with New; consumers, rather than construction, advance delayed work.
//
//	driver := memoryqueue.New()
//	fmt.Println(driver.MessagingSystem()) // memory
type Driver struct {
	// broker owns all queue state and admission/settlement locks.
	broker *broker.Broker
}

var _ queue.DispatcherDriver = (*Driver)(nil)

var _ queue.ConsumerDriver = (*Driver)(nil)

// Start reports [ErrDriverClosed] after [Driver.Stop] and otherwise does nothing:
// the driver needs no background work. It exists for application lifecycle
// wiring.
// An already-canceled context returns its error before checking closure.
//
//	driver := memoryqueue.New()
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	return driver.broker.Start(ctx)
}

// Dispatch adds message to its named queue. Payload is copied before
// acceptance and a missing ID is generated.
// Metadata is copied too; caller mutations after acceptance do not change the
// delivery. Input Attempts is ignored. An empty queue, canceled context, or
// closed driver returns an error without adding a message.
//
//	if err := driver.Dispatch(ctx, queue.Message{
//		Name: "send", Queue: "work", Payload: []byte(`"hello"`),
//	}); err != nil {
//		return err
//	}
func (driver *Driver) Dispatch(ctx context.Context, message queue.Message) error {
	return driver.broker.Dispatch(ctx, message)
}

// Consume receives and handles deliveries from queue until ctx is canceled or
// the driver shuts down. Concurrent consumers for one queue compete for each
// message; each reserved message is delivered to only one handler. It returns
// the context error on cancellation and [ErrDriverClosed] on shutdown.
//
// Cancellation and shutdown are checked before each reservation. A delivery
// already admitted remains the handler's responsibility to settle; cancellation
// does not revoke it. Messages not yet admitted remain available to other
// consumers after cancellation. [Driver.Stop] instead discards queued messages.
//
// Example:
//
//	driver := memoryqueue.New()
//	if err := driver.Dispatch(context.Background(), queue.Message{Queue: "work"}); err != nil {
//		log.Fatal(err)
//	}
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	err := driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
//		defer cancel()
//		return delivery.Acknowledge(ctx)
//	})
//	if !errors.Is(err, context.Canceled) {
//		log.Fatal(err)
//	}
func (driver *Driver) Consume(ctx context.Context, name string, handler queue.DeliveryHandler) error {
	return driver.broker.Consume(ctx, name, handler)
}

// Stop stops new admissions, discards queued jobs, and waits for active
// consumers and handlers until ctx expires, then returns the context's error.
// The release happens even when ctx is already expired: Stop then returns
// without waiting for handlers still running. Stop is idempotent.
// It cannot forcibly stop a handler; handlers must cooperate with their own
// contexts. Repeated Stop calls can wait again for remaining consumers.
//
//	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
//	defer cancel()
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	return driver.broker.Stop(ctx)
}

// MessagingSystem returns "memory", the messaging.system telemetry value of
// the driver.
// This is a telemetry classification, not a persistence capability.
//
//	fmt.Println(memoryqueue.New().MessagingSystem()) // memory
func (driver *Driver) MessagingSystem() string {
	return "memory"
}
