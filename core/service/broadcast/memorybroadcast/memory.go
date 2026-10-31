package memorybroadcast

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"arkoren.dev/core/service/broadcast"
)

// ErrBrokerClosed reports use after shutdown begins.
// Consume gives an already-canceled context precedence over this state.
//
//	if err := publisher.Publish(ctx, topic, payload); errors.Is(err, memorybroadcast.ErrBrokerClosed) {
//		return err
//	}
var ErrBrokerClosed = errors.New("broker is closed")

// DefaultMaxConcurrentDeliveries is the default delivery limit.
// Nonpositive Builder limits select this value after callbacks complete.
//
//	driver := memorybroadcast.New(func(broker *memorybroadcast.Builder) {
//		broker.MaxConcurrentDeliveries(memorybroadcast.DefaultMaxConcurrentDeliveries)
//	})
const DefaultMaxConcurrentDeliveries = 1024

// memoryQueueCapacity is the buffer size of each consumer.
const memoryQueueCapacity = 1024

// settings configures an in-memory asynchronous broadcast broker.
type settings struct {
	// MaxConcurrentDeliveries limits concurrent handlers.
	MaxConcurrentDeliveries int
	// Logger records recovered delivery panics.
	Logger *slog.Logger
}

// defaultConfig returns the default configuration.
func defaultConfig() settings {
	return settings{
		MaxConcurrentDeliveries: DefaultMaxConcurrentDeliveries,
	}
}

// Driver provides asynchronous in-process broadcast delivery. Every running
// Consume call receives its own copy of each published message. Shutdown
// closes delivery admission before waiting for already-admitted handlers;
// those handlers retain their detached consumer contexts until they finish.
// Construct a Driver with [New]; its zero value is not ready for use.
//
// Example:
//
//	driver := memorybroadcast.New()
//	if err := driver.Stop(context.Background()); err != nil {
//		log.Fatal(err)
//	}
type Driver struct {
	// mu guards closed and subscribers, and serializes delivery registration
	// with shutdown. No wait-group task may be added after closed becomes true.
	mu sync.Mutex

	// closed reports whether shutdown has begun.
	closed bool

	// done is closed when shutdown begins.
	done chan struct{}

	// subscribers holds the buffer of every running Consume call.
	subscribers map[chan broadcast.Message]struct{}

	// logger records handler failures and panics.
	logger *slog.Logger

	// wg tracks admitted deliveries, including handlers not yet scheduled.
	// Additions occur under mu before shutdown can begin waiting.
	wg sync.WaitGroup

	// limit bounds concurrent deliveries.
	limit chan struct{}
}

var _ broadcast.PublisherDriver = (*Driver)(nil)
var _ broadcast.ConsumerDriver = (*Driver)(nil)

// New creates an asynchronous memory broker. A nil Logger uses slog.Default.
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
//
// Example:
//
//	memorybroadcast.New(func(broker *memorybroadcast.Builder) {
//		broker.MaxConcurrentDeliveries(8)
//	})
func New(configure ...BuilderFunc) *Driver {
	config := newSettings(configure...)

	if config.MaxConcurrentDeliveries <= 0 {
		config.MaxConcurrentDeliveries = DefaultMaxConcurrentDeliveries
	}

	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	return &Driver{
		done:        make(chan struct{}),
		subscribers: map[chan broadcast.Message]struct{}{},
		logger:      config.Logger,
		limit:       make(chan struct{}, config.MaxConcurrentDeliveries),
	}
}

// Publish queues a copy of message for every active consumer. It blocks while
// a consumer's buffer is full, until ctx ends.
// An invalid name matches broadcast.ErrInvalidName; an already-canceled context
// or closed broker returns its error. Each subscriber receives independent
// payload and metadata containers. Publication can fail after some subscribers
// have accepted their copies; fan-out is not an atomic transaction.
//
//	if err := driver.Publish(ctx, broadcast.Message{
//		Name: "orders.created", Payload: []byte(`42`),
//	}); err != nil {
//		return err
//	}
func (driver *Driver) Publish(ctx context.Context, message broadcast.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := broadcast.ValidateName(message.Name); err != nil {
		return err
	}

	driver.mu.Lock()

	if driver.closed {
		driver.mu.Unlock()

		return ErrBrokerClosed
	}

	subscribers := make([]chan broadcast.Message, 0, len(driver.subscribers))

	for subscriber := range driver.subscribers {
		subscribers = append(subscribers, subscriber)
	}

	driver.mu.Unlock()

	for _, subscriber := range subscribers {
		copied := message
		copied.Payload = slices.Clone(message.Payload)
		copied.Metadata = maps.Clone(message.Metadata)

		select {
		case subscriber <- copied:
		case <-driver.done:
			return ErrBrokerClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// Consume delivers messages to handler until ctx ends or the broker shuts
// down. An already-canceled ctx takes precedence over handler validation and
// shutdown. Otherwise a nil handler is rejected before subscribing.
// Messages published before Consume starts are not delivered.
//
// Waiting for a handler slot is cancelable: ctx cancellation returns its
// error and shutdown returns [ErrBrokerClosed], without admitting the pending
// message. Already-admitted handlers keep ctx's values but not its deadline
// or cancellation. Consume does not wait for those handlers; [Driver.Stop]
// supplies the bounded drain wait.
//
// Example:
//
//	driver := memorybroadcast.New()
//	ctx, cancel := context.WithCancel(context.Background())
//	cancel()
//	err := driver.Consume(ctx, func(context.Context, broadcast.Message) error {
//		return nil
//	})
//	if !errors.Is(err, context.Canceled) {
//		log.Fatal(err)
//	}
func (driver *Driver) Consume(ctx context.Context, handler broadcast.Handler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if handler == nil {
		return errors.New("nil broadcast handler")
	}

	messages := make(chan broadcast.Message, memoryQueueCapacity)

	driver.mu.Lock()

	if driver.closed {
		driver.mu.Unlock()

		return ErrBrokerClosed
	}

	driver.subscribers[messages] = struct{}{}
	driver.mu.Unlock()

	defer func() {
		driver.mu.Lock()
		delete(driver.subscribers, messages)
		driver.mu.Unlock()
	}()

	// err ends reception when cancellation, shutdown, or admission rejects
	// the next message, without waiting for handlers already running.
	var err error

	for err == nil {
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-driver.done:
			err = ErrBrokerClosed
		case message := <-messages:
			err = driver.deliver(ctx, message, handler)
		}
	}

	return err
}

// deliver waits for a handler slot until ctx ends or shutdown begins, then
// registers the delivery under the shutdown mutex. Rechecking after slot
// acquisition prevents a ready slot from bypassing cancellation or closure.
// Rejected admission releases the slot and returns the context error or
// ErrBrokerClosed. Admitted handlers run on detached contexts so cancellation
// stops admission, not work that Stop must drain.
func (driver *Driver) deliver(ctx context.Context, message broadcast.Message, handler broadcast.Handler) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-driver.done:
		return ErrBrokerClosed
	case driver.limit <- struct{}{}:
	}

	if err := ctx.Err(); err != nil {
		<-driver.limit

		return err
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.closed {
		<-driver.limit

		return ErrBrokerClosed
	}

	driver.wg.Go(func() {
		defer func() {
			<-driver.limit
		}()
		defer driver.recoverPanic(ctx, message)

		if err := handler(context.WithoutCancel(ctx), message); err != nil {
			driver.logger.ErrorContext(ctx, "broadcast handler failed", "name", message.Name, "error", err)
		}
	})

	return nil
}

// recoverPanic logs a panic raised while handling message.
func (driver *Driver) recoverPanic(ctx context.Context, message broadcast.Message) {
	if recovered := recover(); recovered != nil {
		driver.logger.ErrorContext(ctx, "broadcast handler panicked", "name", message.Name, "error", recovered)
	}
}

// Start does nothing because the broker starts no goroutines until
// messages are consumed. It exists so the driver satisfies the application
// lifecycle.
// It performs no I/O or state transition, including after Stop.
//
//	driver := memorybroadcast.New()
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(context.Context) error {
	return nil
}

// Stop closes delivery admission, signals consumers, rejects new messages,
// and waits for already-admitted handlers until ctx ends, then returns the
// context's error. No further deliveries are admitted after shutdown begins;
// an admitted handler may still start or finish after Stop returns.
//
// Consumers are signaled even when ctx is already expired; Stop then returns
// without waiting for handlers still running. Handler contexts are not
// canceled, and Stop does not wait for Consume calls to return. Stop is
// idempotent; a later call can wait again for admitted handlers to finish.
//
// Example:
//
//	driver := memorybroadcast.New()
//	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
//	defer cancel()
//	if err := driver.Stop(ctx); err != nil {
//		log.Printf("broadcast drain incomplete: %v", err)
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	driver.mu.Lock()

	if !driver.closed {
		driver.closed = true
		close(driver.done)
	}

	driver.mu.Unlock()

	done := make(chan struct{})

	go func() {
		driver.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MessagingSystem returns "memory", the messaging.system telemetry value of
// the driver.
// This classification does not imply durable retention or cross-process delivery.
//
//	fmt.Println(memorybroadcast.New().MessagingSystem()) // memory
func (driver *Driver) MessagingSystem() string {
	return "memory"
}
