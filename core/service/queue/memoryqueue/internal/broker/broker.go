// Package broker owns memory queue admission, delivery and delayed retries.
package broker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"arkoren.dev/core/service/queue"
)

// Broker implements [queue.DispatcherDriver] and [queue.ConsumerDriver] with
// in-memory data structures. It is non-durable: process exit or crash loses
// queued jobs. A Broker is safe for concurrent use.
// Create it with New; consumers, rather than construction, advance delayed work.
//
//	driver := memoryqueue.New()
//	fmt.Println(driver.MessagingSystem()) // memory
type Broker struct {
	// RetryDelay is the delay applied to retries that request none.
	RetryDelay time.Duration

	// Logger records delivery failures.
	Logger *slog.Logger

	// done is closed when shutdown begins.
	done chan struct{}

	// active counts running Consume calls.
	active sync.WaitGroup

	// mu guards every field below.
	mu sync.Mutex

	// closed reports whether shutdown has begun.
	closed bool

	// changed is closed and replaced whenever a message becomes ready or is
	// delayed, waking idle consumers.
	changed chan struct{}

	// ready holds the messages available now, per queue, in arrival order.
	ready map[string][]queue.Message

	// delayed holds retries that become ready at their due time.
	delayed delayedHeap

	// sequence orders delayed messages due at the same instant and numbers
	// generated message IDs.
	sequence uint64
}

// driverLogger returns value, or the default logger when value is nil.
func driverLogger(value *slog.Logger) *slog.Logger {
	if value == nil {
		return slog.Default()
	}

	return value
}

// Start reports [ErrDriverClosed] after [Broker.Stop] and otherwise does nothing:
// the driver needs no background work. It exists for application lifecycle
// wiring.
// An already-canceled context returns its error before checking closure.
//
//	driver := memoryqueue.New()
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Broker) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.closed {
		return ErrDriverClosed
	}

	return nil
}

// Consume receives and handles deliveries from queue until ctx is canceled or
// the driver shuts down. Concurrent consumers for one queue compete for each
// message; each reserved message is delivered to only one handler. It returns
// the context error on cancellation and [ErrDriverClosed] on shutdown.
//
// Cancellation and shutdown are checked before each reservation. A delivery
// already admitted remains the handler's responsibility to settle; cancellation
// does not revoke it. Messages not yet admitted remain available to other
// consumers after cancellation. [Broker.Stop] instead discards queued messages.
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
func (driver *Broker) Consume(ctx context.Context, name string, handler queue.DeliveryHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if name == "" {
		return ErrInvalidQueue
	}

	if handler == nil {
		return ErrInvalidHandler
	}

	if err := driver.admitConsumer(); err != nil {
		return err
	}

	defer driver.active.Done()

	for {
		if err := driver.consumeNext(ctx, name, handler); err != nil {
			return err
		}
	}
}

// admitConsumer records a consumer unless shutdown has begun.
func (driver *Broker) admitConsumer() error {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.closed {
		return ErrDriverClosed
	}

	driver.active.Add(1)

	return nil
}

// wait blocks until ctx ends, shutdown begins, the queues change, or due
// passes. A zero due waits without a timer.
func (driver *Broker) wait(ctx context.Context, wake <-chan struct{}, due time.Time) error {
	var timeout <-chan time.Time

	if !due.IsZero() {
		timer := time.NewTimer(time.Until(due))
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-driver.done:
		return ErrDriverClosed
	case <-wake:
	case <-timeout:
	}

	return nil
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
func (driver *Broker) Stop(ctx context.Context) error {
	driver.mu.Lock()

	if !driver.closed {
		driver.closed = true
		close(driver.done)
	}

	driver.ready = map[string][]queue.Message{}
	driver.delayed = nil
	driver.mu.Unlock()

	idle := make(chan struct{})

	go func() {
		driver.active.Wait()
		close(idle)
	}()

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NewBroker constructs a broker without background work.
func NewBroker(retryDelay time.Duration, logger *slog.Logger) *Broker {
	return &Broker{RetryDelay: retryDelay, Logger: driverLogger(logger), done: make(chan struct{}), changed: make(chan struct{}), ready: map[string][]queue.Message{}}
}
