package payment

import (
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// Builder is a construction-only draft for Client observation settings.
// New copies it before publication; retaining a draft cannot reconfigure a client.
//
// Example:
//
//	client := payment.New(driver, func(checkout *payment.Builder) {
//		checkout.Telemetry(nil)
//	})
//	_ = client.Driver()
type Builder struct {
	// telemetry is a shared service, resolved dynamically from Default when nil.
	// Publication copies the pointer rather than the underlying service.
	telemetry *telemetry.Telemetry

	// events is the shared in-process bus for observed operation results.
	// Nil disables notifications; publication retains only the selected pointer.
	events *event.Bus
}

// BuilderFunc is a synchronous construction callback accepted by New.
// Callbacks share one fresh draft; supplied nil entries panic by design.
//
// Example:
//
//	policy := payment.BuilderFunc(func(checkout *payment.Builder) {
//		checkout.Telemetry(nil)
//	})
//	_ = payment.New(driver, policy)
type BuilderFunc = func(*Builder)

// Telemetry selects the shared observability service without recording payloads.
// Nil follows telemetry.Default on every operation, including later changes.
// Later calls replace earlier settings and return the same construction draft.
//
// Example:
//
//	client := payment.New(driver, func(checkout *payment.Builder) {
//		checkout.Telemetry(nil)
//	})
//	_ = client.Driver()
func (checkout *Builder) Telemetry(instrumentation *telemetry.Telemetry) *Builder {
	checkout.telemetry = instrumentation

	return checkout
}

// Events selects the shared bus for best-effort operation-result notifications.
// Nil disables notifications. Later calls replace earlier settings and return
// the same draft; changing a retained draft cannot reconfigure a published Client.
// Listeners observe API-call results, not settlement or authoritative provider
// state. Listener errors and panics cannot change the returned result or error.
//
// Example:
//
//	bus := event.New()
//	client := payment.New(driver, func(checkout *payment.Builder) {
//		checkout.Events(bus)
//	})
//	_ = client.Driver()
func (checkout *Builder) Events(events *event.Bus) *Builder {
	checkout.events = events

	return checkout
}
