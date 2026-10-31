package push

import (
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// DefaultTTL is the Sender's initial retention period, applied only at the
// facade boundary. Raw drivers must not substitute it for SendConfig.TTL zero.
//
// Example:
//
//	sender := push.New(driver, func(delivery *push.Builder) {
//		delivery.TTL(push.DefaultTTL)
//	})
//	_ = sender
const DefaultTTL = 24 * time.Hour

// Builder is a construction-only Sender draft. New copies completed settings;
// retaining it cannot reconfigure a published sender. Live telemetry is shared.
//
// Example:
//
//	sender := push.New(driver, func(delivery *push.Builder) {
//		delivery.TTL(time.Hour).Telemetry(nil)
//	})
//	_ = sender.Driver()
type Builder struct {
	// events is shared; nil disables operational notifications.
	events *event.Bus

	// telemetry follows the dynamic default when nil, never captured at New.
	telemetry *telemetry.Telemetry

	// ttl stores the construction policy before normalization at publication.
	ttl time.Duration
}

// Events selects best-effort, in-process submission notifications and returns
// the same draft. Nil disables them. Listeners cannot change Send's result;
// notifications never establish delivery or display on a device.
//
// Example:
//
//	sender := push.New(driver, func(delivery *push.Builder) {
//		delivery.Events(event.New())
//	})
//	_ = sender
func (delivery *Builder) Events(events *event.Bus) *Builder {
	delivery.events = events

	return delivery
}

// BuilderFunc aliases the synchronous construction callback type. Callbacks
// share one fresh draft; nil entries panic and empty expansions omit edits.
//
// Example:
//
//	policies := []push.BuilderFunc{
//		func(delivery *push.Builder) {
//			delivery.TTL(time.Hour)
//		},
//	}
//	sender := push.New(driver, policies...)
//	_ = sender.Driver()
type BuilderFunc = func(*Builder)

// Telemetry selects a shared observability service and returns the same draft.
// Nil follows telemetry.Default on every call, including later default changes.
//
// Example:
//
//	sender := push.New(driver, func(delivery *push.Builder) {
//		delivery.Telemetry(nil)
//	})
//	_ = sender
func (delivery *Builder) Telemetry(instrumentation *telemetry.Telemetry) *Builder {
	delivery.telemetry = instrumentation

	return delivery
}

// TTL replaces the default retention policy and returns the same draft.
// Zero resets to DefaultTTL, positive durations set retention, and negative
// durations disable provider storage. Drivers may reject unsupported policies.
//
// Example:
//
//	sender := push.New(driver, func(delivery *push.Builder) {
//		delivery.TTL(-1)
//	})
//	_ = sender
func (delivery *Builder) TTL(ttl time.Duration) *Builder {
	delivery.ttl = ttl

	return delivery
}

// SendConfig is the fully resolved raw-driver configuration. Its zero value
// means explicit no-store; defaults are exclusively a Sender responsibility.
//
// Example:
//
//	_, err := driver.Send(ctx, message, push.SendConfig{TTL: time.Hour})
//	if err != nil { return err }
type SendConfig struct {
	// TTL is requested retention as a nonnegative duration. Zero requests no
	// storage; a negative raw value is invalid. Drivers never apply defaults
	// and must not silently increase retention. Provider bounds and precision
	// constraints belong in the driver, not in shared message validation.
	//
	// Example:
	//
	//	_, err := driver.Send(ctx, message, push.SendConfig{TTL: 0})
	//	if err != nil { return err }
	TTL time.Duration
}

// sendOptions holds a per-call policy before inheritance is resolved.
// Zero inherits the Sender default, unlike zero in raw SendConfig.
type sendOptions struct {
	// ttl is positive retention, zero inheritance, or negative no-store.
	ttl time.Duration
}

// SendOption edits a private per-call draft. Use TTL to select retention;
// supplied nil options panic. Options never reconfigure the Sender.
//
// Example:
//
//	options := []push.SendOption{push.TTL(time.Hour)}
//	_, err := sender.Send(ctx, message, options...)
//	if err != nil { return err }
type SendOption func(*sendOptions)

// TTL selects retention for one Send. Zero or omission inherits the Sender's
// configured default, positive overrides it, and negative requests no-store.
// Later options replace earlier values; no global maximum or precision limit
// is imposed here. A driver may reject a policy it cannot represent safely.
//
// Example:
//
//	_, err := sender.Send(ctx, message, push.TTL(-1))
//	if err != nil { return err }
func TTL(ttl time.Duration) SendOption {
	return func(policy *sendOptions) {
		policy.ttl = ttl
	}
}

// resolveTTL converts construction or per-call policy to raw retention.
// A zero policy inherits fallback; negative values become explicit no-store.
func resolveTTL(ttl, fallback time.Duration) time.Duration {
	if ttl == 0 {
		return fallback
	}

	if ttl < 0 {
		return 0
	}

	return ttl
}
