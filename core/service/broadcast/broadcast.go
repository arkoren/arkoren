package broadcast

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"slices"
	"sync"

	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/internal/messaging"
	"arkoren.dev/core/service/telemetry"
)

// telemetryScope is the instrumentation scope of broadcast telemetry.
const telemetryScope = "arkoren.dev/core/service/broadcast"

// newInstruments builds the broadcast instruments for one telemetry.
var newInstruments = messaging.Build(telemetryScope)

// Message is the wire envelope shared by all broadcast transports.
// Payload is logical JSON bytes inside the envelope; Metadata is propagation
// data, not routing policy. Use Publisher for typed application values.
//
//	message := broadcast.Message{Name: "orders.created", Payload: []byte(`{"id":42}`)}
//	if err := driver.Publish(ctx, message); err != nil {
//		return err
//	}
type Message struct {
	// ID identifies the message when the transport supplies an identifier.
	ID string `json:"id,omitzero"`
	// Name identifies the broadcast fact and routing key.
	Name string `json:"name"`
	// Payload contains the JSON-encoded application value.
	Payload []byte `json:"payload"`
	// Metadata carries string key-value pairs beside the payload, such as the
	// trace context [Publisher.Publish] injects. Drivers deliver it to
	// consumers unchanged: nil and empty are equivalent, and a message
	// published without metadata arrives with nil Metadata. The guarantee
	// covers at most MaxMetadataEntries entries whose keys are 1 to
	// MaxMetadataKeyLength bytes of [A-Za-z0-9._-] (case-sensitive) and whose
	// values are valid UTF-8, with at most MaxMetadataSize bytes of keys and
	// values together. Beyond those limits a driver may reject the message.
	// Metadata is not part of the payload and never affects routing.
	Metadata map[string]string `json:"metadata,omitzero"`
}

// Limits of the [Message.Metadata] round-trip guarantee.
const (
	// MaxMetadataEntries is the most metadata entries a driver must carry.
	// Larger maps are outside the portable round-trip guarantee.
	//
	//	if len(message.Metadata) > broadcast.MaxMetadataEntries {
	//		return errors.New("too many metadata entries")
	//	}
	MaxMetadataEntries = 16

	// MaxMetadataKeyLength is the longest metadata key a driver must carry,
	// in bytes.
	//
	//	if len(metadataKey) > broadcast.MaxMetadataKeyLength {
	//		return errors.New("metadata key too long")
	//	}
	MaxMetadataKeyLength = 64

	// MaxMetadataSize is the most bytes of keys and values together a driver
	// must carry.
	// This counts keys and values together; it does not include payload bytes.
	//
	//	if metadataBytes > broadcast.MaxMetadataSize {
	//		return errors.New("metadata too large")
	//	}
	MaxMetadataSize = 4096
)

// PublisherDriver publishes complete broadcast envelopes.
// Acceptance does not confirm delivery to any application listener.
//
//	var transport broadcast.PublisherDriver = memorybroadcast.New()
//	publisher := broadcast.NewPublisher(transport)
//	if err := publisher.Publish(ctx, broadcast.NewTopic[int]("orders.created"), 42); err != nil {
//		return err
//	}
type PublisherDriver interface {
	// Publish sends message to the transport scope. It returns an error
	// matching [ErrInvalidName] when message.Name is not a valid name (see
	// [ValidateName]) and the context error when the context is already
	// canceled. Consumers receive Name, Payload, and Metadata unchanged (see
	// [Message.Metadata]).
	//
	//	if err := driver.Publish(ctx, message); err != nil {
	//		return err
	//	}
	Publish(context.Context, Message) error
}

// Handler processes one received broadcast envelope.
// ConsumerDriver determines redelivery; the facade handles decoding and listener
// fan-out. Handler errors do not automatically end a driver's Consume loop.
//
//	handler := broadcast.Handler(func(ctx context.Context, message broadcast.Message) error {
//		return observe(ctx, message.Name)
//	})
//	if err := driver.Consume(ctx, handler); err != nil {
//		return err
//	}
type Handler = func(context.Context, Message) error

// ConsumerDriver receives complete broadcast envelopes from one transport scope.
// It owns subscription timing and retention, not facade listener registration.
//
//	consumer := broadcast.NewConsumer(driver)
//	compose.Run(consumer.Run)
type ConsumerDriver interface {
	// Consume passes envelopes to the handler until the context is canceled
	// or an error occurs. Cancellation ends Consume with an error matching the
	// context error under [errors.Is], and a context that is already canceled
	// is rejected the same way. A handler error does not end Consume; the
	// driver may redeliver the message. Transports without retention drop
	// messages published before the subscription is active.
	//
	//	if err := driver.Consume(ctx, handler); err != nil {
	//		return err
	//	}
	Consume(context.Context, Handler) error
}

// Publisher JSON-encodes values and delegates envelope publishing.
// Construction does not start or take ownership of the supplied driver.
//
//	publisher := broadcast.NewPublisher(driver)
//	if err := publisher.Publish(ctx, broadcast.NewTopic[int]("orders.created"), 42); err != nil {
//		return err
//	}
type Publisher struct {
	// driver publishes the envelopes.
	driver PublisherDriver

	// config holds the telemetry settings.
	config settings

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[messaging.Instruments]
}

// NewPublisher creates a publisher around driver. Construction policies configure its
// telemetry: every publish emits a producer span "send {name}" and the
// messaging.client.sent.messages and messaging.client.operation.duration
// metrics, and injects the trace context into [Message.Metadata], when the
// telemetry is enabled. It costs nothing otherwise. Payloads and metadata
// values are never recorded.
//
//		publisher := broadcast.NewPublisher(driver, func(publisher *broadcast.PublisherBuilder) {
//		publisher.Telemetry(app.Telemetry())
//	})
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
func NewPublisher(driver PublisherDriver, configure ...PublisherBuilderFunc) *Publisher {
	return &Publisher{
		driver:      driver,
		config:      newPublisherSettings(configure...),
		instruments: telemetry.NewInstruments(newInstruments),
	}
}

// Driver returns the underlying publisher driver.
// A nil publisher returns nil. Raw calls bypass typed encoding and telemetry.
//
//	publisher := broadcast.NewPublisher(driver)
//	fmt.Println(publisher.Driver() == driver)
func (publisher *Publisher) Driver() PublisherDriver {
	if publisher == nil {
		return nil
	}

	return publisher.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() PublisherDriver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver.
//
//	if driver, ok := publisher.DriverAs[*memorybroadcast.Driver](); ok {
//		fmt.Printf("memory publication: %T\n", driver)
//	}
func (publisher *Publisher) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](publisher.Driver())
}

// Publish JSON-encodes payload and publishes it under the name of topic.
// Invalid names match ErrInvalidName; encoding and driver errors preserve their
// causes. A nil publisher, driver, or context returns an error. Successful
// acceptance does not guarantee delivery, retention, or listener completion.
//
//	topic := broadcast.NewTopic[int]("orders.created")
//	if err := publisher.Publish(ctx, topic, 42); err != nil {
//		return err
//	}
func (publisher *Publisher) Publish[T any](ctx context.Context, topic Topic[T], payload T) error {
	name := topic.name

	if publisher == nil || nilValue(publisher.driver) {
		return errors.New("nil broadcast publisher")
	}

	if ctx == nil {
		return errors.New("nil broadcast context")
	}

	if err := ValidateName(name); err != nil {
		return err
	}

	encoded, err := json.Marshal(payload)

	if err != nil {
		return fmt.Errorf("marshal broadcast payload: %w", err)
	}

	ctx, metadata, op := messaging.Send(ctx, cmp.Or(publisher.config.Telemetry, telemetry.Default()), publisher.instruments, messaging.Target{
		Driver:      publisher.driver,
		System:      publisher.config.System,
		Destination: name,
	})

	err = publisher.driver.Publish(ctx, Message{
		Name:     name,
		Payload:  encoded,
		Metadata: metadata,
	})
	op.End(ctx, err)

	return err
}

// Consumer routes messages from one driver to registered typed listeners.
// Listener registration is concurrent-safe; delivery snapshots run outside the
// registration lock. The caller retains ownership of driver lifecycle.
//
//	consumer := broadcast.NewConsumer(driver)
//	compose.Run(consumer.Run)
type Consumer struct {
	// driver supplies messages; routing and listener registration stay in the facade.
	driver ConsumerDriver

	// config holds the telemetry settings.
	config settings

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[messaging.Instruments]

	// mu protects listener registration and dispatch snapshots.
	mu sync.RWMutex

	// listeners preserves registration order for each dispatch snapshot.
	listeners []consumerListener

	// nextID allocates identities so an unsubscribe removes only its registration.
	nextID uint64
}

// consumerListener retains one registration and its decoding or raw-message adapter.
// Dispatch copies these records before invoking callbacks outside the lock.
type consumerListener struct {
	// id distinguishes registrations, including listeners sharing a pattern.
	id uint64

	// pattern selects messages using the broadcast wildcard rules.
	pattern string

	// invoke adapts the listener and converts decoding failures or panics to errors.
	invoke func(context.Context, Message) error
}

// NewConsumer creates a consumer around driver. Construction policies configure its
// telemetry: every received message emits a consumer span "process {name}",
// parented to the trace context in [Message.Metadata], and the
// messaging.client.consumed.messages and messaging.process.duration metrics
// when the telemetry is enabled. It costs nothing otherwise.
//
//		consumer := broadcast.NewConsumer(driver, func(consumer *broadcast.ConsumerBuilder) {
//		consumer.Telemetry(app.Telemetry())
//	})
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
func NewConsumer(driver ConsumerDriver, configure ...ConsumerBuilderFunc) *Consumer {
	return &Consumer{
		driver:      driver,
		config:      newSettings(configure...),
		instruments: telemetry.NewInstruments(newInstruments),
	}
}

// Driver returns the underlying consumer driver.
// A nil consumer returns nil. Raw consumption bypasses typed listener dispatch.
//
//	consumer := broadcast.NewConsumer(driver)
//	fmt.Println(consumer.Driver() == driver)
func (consumer *Consumer) Driver() ConsumerDriver {
	if consumer == nil {
		return nil
	}

	return consumer.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() ConsumerDriver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver.
//
//	if driver, ok := consumer.DriverAs[*memorybroadcast.Driver](); ok {
//		fmt.Printf("memory subscription: %T\n", driver)
//	}
func (consumer *Consumer) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](consumer.Driver())
}

// Run consumes messages and routes them to matching listeners.
// It delegates cancellation and subscription retention to the driver and returns
// its error unchanged. Nil consumer, driver, or context is rejected. Listener
// failures are delivery outcomes; they do not end Run by themselves.
//
//	compose.Run(consumer.Run)
func (consumer *Consumer) Run(ctx context.Context) error {
	if consumer == nil || nilValue(consumer.driver) {
		return errors.New("nil broadcast consumer")
	}

	if ctx == nil {
		return errors.New("nil broadcast context")
	}

	return consumer.driver.Consume(ctx, consumer.route)
}

// Listen registers a typed listener for messages whose name equals the
// name of topic exactly. A listener may return [Permanent] to tell redelivering
// drivers that retrying cannot help.
// Invalid topic names match ErrInvalidName; a nil listener or consumer returns
// an error without registration. Listeners sharing a name must agree on JSON
// payload semantics. Unsubscribe is idempotent; an in-flight snapshot may still
// invoke a removed listener. One failed listener can redeliver to all listeners.
//
//	topic := broadcast.NewTopic[int]("orders.created")
//	unsubscribe, err := consumer.Listen(topic, func(ctx context.Context, id int) error {
//		return observeOrder(ctx, id)
//	})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
func (consumer *Consumer) Listen[T any](topic Topic[T], listener func(context.Context, T) error) (Unsubscribe, error) {
	if nilValue(listener) {
		return nil, errors.New("nil broadcast listener")
	}

	if err := ValidateName(topic.name); err != nil {
		return nil, err
	}

	return consumer.add(topic.name, typedInvoker(listener))
}

// ListenPattern registers a listener that receives raw messages whose name
// matches pattern. Patterns may use the * and # wildcards; see [Match].
// Invalid patterns match ErrInvalidName. A nil listener or consumer is rejected.
// Each delivery supplies its raw envelope; listeners must not mutate shared
// payload or metadata while other listeners use it. Panics become errors.
//
//	unsubscribe, err := consumer.ListenPattern("orders.#", func(ctx context.Context, message broadcast.Message) error {
//		return observe(ctx, message.Name)
//	})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
func (consumer *Consumer) ListenPattern(pattern string, listener func(context.Context, Message) error) (Unsubscribe, error) {
	if nilValue(listener) {
		return nil, errors.New("nil broadcast listener")
	}

	if err := ValidatePattern(pattern); err != nil {
		return nil, err
	}

	return consumer.add(pattern, rawInvoker(listener))
}

// add registers invoke under pattern and returns its [Unsubscribe] function.
func (consumer *Consumer) add(pattern string, invoke func(context.Context, Message) error) (Unsubscribe, error) {
	if consumer == nil {
		return nil, errors.New("nil broadcast listener")
	}

	consumer.mu.Lock()
	consumer.nextID++
	id := consumer.nextID
	consumer.listeners = append(consumer.listeners, consumerListener{
		id:      id,
		pattern: pattern,
		invoke:  invoke,
	})
	consumer.mu.Unlock()

	return func() {
		consumer.remove(id)
	}, nil
}

// rawInvoker wraps a raw message listener with panic protection.
func rawInvoker(listener func(context.Context, Message) error) func(context.Context, Message) error {
	return func(ctx context.Context, message Message) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("broadcast listener panic for %s: %v\n%s", message.Name, recovered, debug.Stack())
			}
		}()

		if err := listener(ctx, message); err != nil {
			return fmt.Errorf("handle broadcast %s: %w", message.Name, err)
		}

		return nil
	}
}

// typedInvoker decodes each delivery independently for one listener type.
func typedInvoker[T any](listener func(context.Context, T) error) func(context.Context, Message) error {
	return func(ctx context.Context, message Message) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("broadcast listener panic for %s: %v\n%s", message.Name, recovered, debug.Stack())
			}
		}()
		var payload T

		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return permanent{
				fmt.Errorf("decode broadcast %s: %w", message.Name, err),
			}
		}

		if err := listener(ctx, payload); err != nil {
			return fmt.Errorf("handle broadcast %s: %w", message.Name, err)
		}

		return nil
	}
}

// remove deletes the registration identified by id under the listener lock.
// Missing identities are ignored, making repeated unsubscribe calls harmless.
// Clearing the unused slice slot releases captures not retained by another
// registration or an already-taken dispatch snapshot.
func (consumer *Consumer) remove(id uint64) {
	consumer.mu.Lock()
	defer consumer.mu.Unlock()

	for index, listener := range consumer.listeners {
		if listener.id == id {
			consumer.listeners = slices.Delete(consumer.listeners, index, index+1)

			return
		}
	}
}

// route validates a delivery before recording its telemetry and dispatching it.
// Invalid names are permanent failures; listener errors retain their retry meaning.
func (consumer *Consumer) route(ctx context.Context, message Message) error {
	if ctx == nil {
		return errors.New("nil broadcast context")
	}

	if err := ValidateName(message.Name); err != nil {
		return permanent{
			err,
		}
	}

	ctx, op := messaging.Process(ctx, cmp.Or(consumer.config.Telemetry, telemetry.Default()), consumer.instruments, messaging.Target{
		Driver:      consumer.driver,
		System:      consumer.config.System,
		Destination: message.Name,
		ID:          message.ID,
		Metadata:    message.Metadata,
	})

	err := consumer.dispatch(ctx, message)
	op.End(ctx, err)

	return err
}

// dispatch invokes every listener whose pattern matches message and joins
// their failures. The result matches [ErrPermanent] only when every failure
// is permanent.
func (consumer *Consumer) dispatch(ctx context.Context, message Message) error {
	consumer.mu.RLock()

	listeners := append([]consumerListener(nil), consumer.listeners...)
	consumer.mu.RUnlock()
	var errs []error

	for _, listener := range listeners {
		if !Match(listener.pattern, message.Name) {
			continue
		}

		if err := listener.invoke(ctx, message); err != nil {
			errs = append(errs, err)

			consumer.config.Events.Emit(ctx, ListenerFailed, ListenerFailedEvent{
				ID:   message.ID,
				Name: message.Name,
				Err:  err,
			})
		}
	}

	joined := errors.Join(errs...)
	permanentCount := 0

	for _, err := range errs {
		if errors.Is(err, ErrPermanent) {
			permanentCount++
		}
	}

	if permanentCount > 0 && permanentCount < len(errs) {
		return mixed{
			joined,
		}
	}

	return joined
}

// nilValue recognizes both nil interfaces and typed nil values at facade boundaries.
// Non-nilable values are accepted without calling reflect.Value.IsNil on them.
func nilValue(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)

	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// ErrPermanent matches, under [errors.Is], errors that redelivery cannot fix,
// such as an invalid message name, a payload that does not decode, or a
// listener error wrapped with [Permanent]. A delivery error that joins
// several listener failures matches only when every failure is permanent.
//
//	err := broadcast.Permanent(errors.New("invalid order"))
//	fmt.Println(errors.Is(err, broadcast.ErrPermanent)) // true
var ErrPermanent = errors.New("permanent broadcast failure")

// permanent marks its wrapped error as matching [ErrPermanent].
type permanent struct {
	// error supplies the original failure message and remains available to Unwrap.
	error
}

// Is reports whether target is [ErrPermanent].
func (permanent) Is(target error) bool {
	return target == ErrPermanent
}

// Unwrap returns the wrapped cause.
func (permanent permanent) Unwrap() error {
	return permanent.error
}

// mixed joins listener failures of which some, but not all, are permanent.
// It hides [ErrPermanent] from [errors.Is] and does not expose Unwrap, so
// that the permanent parts cannot make the whole delivery look permanent;
// every other target is matched against the joined failures.
type mixed struct {
	// joined holds all the listener failures.
	joined error
}

// Error returns the joined failure messages.
func (mixed mixed) Error() string {
	return mixed.joined.Error()
}

// Is matches every target except [ErrPermanent] against the joined failures.
func (mixed mixed) Is(target error) bool {
	return target != ErrPermanent && errors.Is(mixed.joined, target)
}

// As matches target against the joined failures.
func (mixed mixed) As(target any) bool {
	return errors.As(mixed.joined, target)
}

// Permanent marks err as a failure that redelivery cannot fix. A listener
// returns it to tell drivers that redeliver failed messages to drop or
// dead-letter the message instead of retrying. The result matches
// [ErrPermanent] under [errors.Is] and still unwraps to err; Permanent(nil)
// is nil. Listeners must be idempotent: one failing listener causes
// redelivery to all of them.
//
// Example:
//
//	_, err := consumer.Listen(UserCreated, func(ctx context.Context, user User) error {
//		if user.ID == 0 {
//			return broadcast.Permanent(errors.New("missing user id"))
//		}
//
//		return nil
//	})
func Permanent(err error) error {
	if err == nil {
		return nil
	}

	return permanent{
		err,
	}
}

// Unsubscribe removes a consumer registration. Calling it more than once is
// safe.
// Removal affects future dispatch snapshots, not callbacks already captured.
// It releases the registration's function reference; application captures stay
// shared by any other references the application or an in-flight dispatch owns.
//
//	unsubscribe, err := consumer.Listen(topic, observeOrder)
//	if err != nil {
//		return err
//	}
//	unsubscribe()
type Unsubscribe = func()
