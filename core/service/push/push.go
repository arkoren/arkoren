package push

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

// operationNames defines instruments with static operation/error-class labels.
// Histogram boundaries are seconds, suited to provider request latency.
var operationNames = clientop.Names{
	Scope:    "arkoren.dev/core/service/push",
	Noun:     "push operations",
	Count:    semconv.PushOperations,
	Duration: semconv.PushOperationDuration,
	Keys:     []string{semconv.PushOperation},
	Buckets:  []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
}

// errorSentinels classify contract errors without recording provider text.
// Context and native errors retain the shared clientop classification rules.
var errorSentinels = []errtype.Sentinel{
	{Err: ErrInvalidMessage, Name: "push.ErrInvalidMessage"},
	{Err: ErrUnsupported, Name: "push.ErrUnsupported"},
	{Err: ErrNotFound, Name: "push.ErrNotFound"},
	{Err: ErrRateLimited, Name: "push.ErrRateLimited"},
	{Err: ErrUnavailable, Name: "push.ErrUnavailable"},
	{Err: ErrOutcomeUnknown, Name: "push.ErrOutcomeUnknown"},
}

// Driver submits notifications to one push provider and is safe for concurrent
// use. Send must call CheckSend before any I/O, reject unsupported recipients
// with ErrUnsupported, and apply no defaults to the resolved SendConfig.
// Drivers must not mutate input or retain caller-owned mutable state. Immutable
// Message values may be retained; Data returns a detached map for encoding.
//
// A successful receipt means acceptance, not device delivery; its ID may be
// empty. Every error returns a zero Receipt. Preserve context cancellation and
// native error causes with wrapping. ErrOutcomeUnknown marks ambiguous
// acceptance; ErrNotFound is reserved for confirmed invalid registrations, not
// authentication failures, mismatched projects, or generic endpoint errors.
// Drivers and Sender never retry automatically. Any lifecycle belongs to the
// concrete driver, not this contract. Endpoint security belongs to the driver.
//
// Example:
//
//	var backend push.Driver = driver
//	receipt, err := backend.Send(ctx, message, push.SendConfig{TTL: time.Hour})
//	if err != nil { return err }
//	_ = receipt.ID
type Driver interface {
	// Send submits once with resolved retention and returns optional acceptance
	// metadata. Call CheckSend before I/O and return a zero receipt on errors.
	//
	// Example:
	//
	//	receipt, err := driver.Send(ctx, message, push.SendConfig{})
	//	if err != nil { return err }
	//	_ = receipt.ID
	Send(ctx context.Context, message Message, config SendConfig) (Receipt, error)
}

// Receipt describes provider acceptance only. A successful receipt may be zero;
// it is neither delivery confirmation nor evidence that a device displayed it.
//
// Example:
//
//	receipt, err := sender.Send(ctx, message)
//	if err != nil { return err }
//	_ = receipt.ID
type Receipt struct {
	// ID is optional opaque provider acceptance metadata. It may be empty and
	// must not enter telemetry or be interpreted as proof of device delivery.
	//
	// Example:
	//
	//	receipt, err := sender.Send(ctx, message)
	//	if err != nil { return err }
	//	_ = receipt.ID
	ID string
}

// Sender validates and instruments a Driver without adding lifecycle,
// delivery state, or automatic retries. Settings are immutable after New and
// calls may run concurrently when the underlying Driver honors its contract.
//
// Example:
//
//	sender := push.New(driver)
//	_, err := sender.Send(ctx, push.NewMessage(push.Token("device-registration"), "Ready"))
//	if err != nil { return err }
type Sender struct {
	// events retains the construction-selected bus; nil disables notifications.
	events *event.Bus

	// driver is shared with the caller, which owns its concrete lifecycle.
	driver Driver

	// defaults is already normalized: zero means explicit no-store.
	defaults SendConfig

	// telemetry is shared, or dynamically resolved per call when nil.
	telemetry *telemetry.Telemetry

	// instruments caches bounded operation instruments per telemetry service.
	instruments *telemetry.Instruments[clientop.Instruments]
}

// New constructs a Sender without I/O. Callbacks run directly once,
// synchronously, in order on a fresh draft. Nil entries and callback panics
// propagate; expanded nil or empty slices omit edits. Publication detaches the
// draft, not the driver, telemetry service, or event bus. Nil drivers fail on Send.
//
// Example:
//
//	sender := push.New(driver, func(delivery *push.Builder) {
//		delivery.TTL(time.Hour)
//	})
//	_ = sender.Driver()
func New(driver Driver, configure ...BuilderFunc) *Sender {
	delivery := &Builder{}

	for _, configure := range configure {
		configure(delivery)
	}

	return &Sender{
		events:      delivery.events,
		driver:      driver,
		defaults:    SendConfig{TTL: resolveTTL(delivery.ttl, DefaultTTL)},
		telemetry:   delivery.telemetry,
		instruments: telemetry.NewInstruments(clientop.Build(operationNames)),
	}
}

// Driver returns the originally supplied backend, including wrapping drivers.
// Direct use bypasses default resolution, telemetry, and events, not validation.
//
// Example:
//
//	backend := push.New(driver).Driver()
//	_ = backend
func (sender *Sender) Driver() Driver {
	return sender.driver
}

// DriverAs finds the first backend of type T through Unwrap() Driver wrappers.
// It returns T's zero value and false when no matching backend is present.
//
// Example:
//
//	backend, ok := push.New(driver).DriverAs[push.Driver]()
//	if ok { _ = backend }
func (sender *Sender) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](sender.driver)
}

// Send resolves per-call retention, validates portable input, and submits once.
// Errors preserve native and context identity and always return a zero receipt.
// A nil context or driver fails with ErrInvalidMessage before instrumentation.
// Only bounded operation/error classes enter telemetry, never error text.
// ErrOutcomeUnknown outranks joined context or provider failures in telemetry;
// the returned error still preserves every original cause for errors.Is.
// When Events is configured, one Accepted or Failed notification observes the
// driver call; preflight failures emit none. Listener failures do not change
// the returned result, and unknown acceptance never proves failed delivery.
// Driver panics propagate without an outcome notification. A nil event bus
// performs no event-related clock reads.
//
// Example:
//
//	receipt, err := sender.Send(ctx, message, push.TTL(time.Hour))
//	if err != nil { return err }
//	_ = receipt.ID
func (sender *Sender) Send(ctx context.Context, message Message, opts ...SendOption) (receipt Receipt, err error) {
	policy := &sendOptions{}

	for _, option := range opts {
		option(policy)
	}

	config := SendConfig{TTL: resolveTTL(policy.ttl, sender.defaults.TTL)}

	if err := CheckSend(ctx, message, config); err != nil {
		return Receipt{}, err
	}

	if sender.driver == nil {
		return Receipt{}, fmt.Errorf("%w: driver cannot be nil", ErrInvalidMessage)
	}

	ctx, operation := clientop.Begin(ctx, sender.telemetry, sender.instruments, telemetry.Client, "push", "send",
		telemetry.String(semconv.PushOperation, "send"))
	// startedAt measures driver work only when event observations are enabled.
	var startedAt time.Time

	if sender.events != nil {
		startedAt = time.Now()
	}

	// returned excludes provider panics from the returned-outcome event stream.
	returned := false
	defer func() {
		// duration excludes telemetry completion and event listener execution.
		var duration time.Duration

		if returned && sender.events != nil {
			duration = time.Since(startedAt)
		}

		// Normalize telemetry alone: cancellation after dispatch cannot establish
		// non-acceptance. The API error and event retain their own semantics.
		failure := err

		if errors.Is(err, ErrOutcomeUnknown) {
			failure = ErrOutcomeUnknown
		}

		operation.End(ctx, failure, errorSentinels...)

		if returned {
			sender.emit(ctx, duration, err)
		}
	}()

	receipt, err = sender.driver.Send(ctx, message, config)
	returned = true

	if err != nil {
		return Receipt{}, fmt.Errorf("send push: %w", err)
	}

	return receipt, nil
}

// CheckSend is shared direct-driver preflight without I/O. Nil contexts fail
// with ErrInvalidMessage; done contexts preserve ctx.Err before declaration
// errors. Raw negative TTL is invalid, while zero explicitly requests no-store.
// This imposes no provider-specific bounds or whole-second requirement.
//
// Example:
//
//	if err := push.CheckSend(ctx, message, push.SendConfig{TTL: time.Hour}); err != nil {
//		return push.Receipt{}, err
//	}
func CheckSend(ctx context.Context, message Message, config SendConfig) error {
	if ctx == nil {
		return fmt.Errorf("%w: context cannot be nil", ErrInvalidMessage)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("send push: %w", err)
	}

	if err := message.Validate(); err != nil {
		return err
	}

	if config.TTL < 0 {
		return fmt.Errorf("%w: raw retention must not be negative", ErrInvalidMessage)
	}

	return nil
}
