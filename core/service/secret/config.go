package secret

import (
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// BuilderFunc configures one fresh construction draft directly, synchronously
// and once in order. An expanded nil or empty slice leaves defaults unchanged;
// a supplied nil entry panics. New copies the completed settings, not shared
// service objects. Construction callbacks themselves are not retained.
//
// Example:
//
//	var observations secret.BuilderFunc = func(secrets *secret.Builder) {
//		secrets.Events(bus)
//	}
//	secrets := secret.New(driver, observations)
//	if _, err := secrets.Put(ctx, "api-token", []byte(token)); err != nil {
//		return err
//	}
type BuilderFunc = func(*Builder)

// settings holds shared observability services. Copying it at publication
// detaches the mutable draft, not the services themselves.
type settings struct {
	// telemetry is nil when the store follows telemetry.Default per call.
	telemetry *telemetry.Telemetry

	// events is nil when mutation notifications are disabled.
	events *event.Bus
}

// Builder configures observability before a Store is published. Callbacks run
// once, synchronously in order on one draft; nil entries panic. Retaining a
// draft cannot change a published store. Methods return the same draft.
//
// Example:
//
//	secrets := secret.New(driver, func(secrets *secret.Builder) {
//		secrets.Events(bus)
//	})
type Builder struct {
	// settings contains the construction-time service references.
	settings settings
}

// Telemetry replaces the service receiving operation spans and metrics. Nil
// follows telemetry.Default on each call, including changes after construction.
// The service is shared, not copied or started by the store.
//
// Example:
//
//	secret.New(driver, func(secrets *secret.Builder) {
//		secrets.Telemetry(nil)
//	})
func (secrets *Builder) Telemetry(service *telemetry.Telemetry) *Builder {
	secrets.settings.telemetry = service

	return secrets
}

// Events replaces the bus receiving MutationSucceeded and MutationFailed.
// Nil disables notifications. Publication is synchronous and best effort;
// listener failures cannot change the operation's result. The bus is shared.
//
// Example:
//
//	secret.New(driver, func(secrets *secret.Builder) {
//		secrets.Events(bus)
//	})
func (secrets *Builder) Events(bus *event.Bus) *Builder {
	secrets.settings.events = bus

	return secrets
}
