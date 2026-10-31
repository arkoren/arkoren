package crypto

import "arkoren.dev/core/service/telemetry"

// Builder configures decryption fallbacks and aggregate metrics for New.
// Methods return this draft; telemetry services remain shared dependencies.
// New copies the fallback interface slice, not the live drivers it contains.
// Retaining the draft cannot change an Encrypter's fallback ordering.
//
// Example:
//
//	encrypter, err := crypto.New(current, func(encrypter *crypto.Builder) {
//		encrypter.Previous(previous)
//	})
//	if err != nil {
//		return err
//	}
//	_ = encrypter
type Builder struct {
	// telemetry is copied at publication, but the selected service stays shared.
	telemetry *telemetry.Telemetry

	// previous holds fallbacks in construction order, owned by the draft.
	previous []Driver
}

// BuilderFunc edits one fresh construction draft directly, once and
// synchronously in order. A nil current driver is rejected before callbacks;
// nil callback entries panic and expanded nil or empty slices omit edits.
// Publication detaches the fallback interface slice, not the live drivers or
// their referenced state. Construction callbacks themselves are not retained.
//
// Example:
//
//	var rotation crypto.BuilderFunc = func(encrypter *crypto.Builder) {
//		encrypter.Previous(previous)
//	}
//	encrypter, err := crypto.New(current, rotation)
//	if err != nil {
//		return err
//	}
//	_ = encrypter
type BuilderFunc = func(*Builder)

// Previous appends drivers in decryption fallback order, including across calls.
// New validates nil fallback drivers after all policies. The supplied interface
// slice is copied immediately; driver objects remain shared dependencies.
//
// Example:
//
//	encrypter, err := crypto.New(current, func(encrypter *crypto.Builder) {
//		encrypter.Previous(previous)
//	})
//	if err != nil {
//		return err
//	}
//	_ = encrypter
func (encrypter *Builder) Previous(drivers ...Driver) *Builder {
	encrypter.previous = append(encrypter.previous, drivers...)

	return encrypter
}

// Telemetry selects aggregate metrics without traces or events. Nil resolves
// telemetry.Default per call; telemetry.New(nil) explicitly disables metrics.
// The service stays shared, but retaining this draft cannot replace a published
// Encrypter's selection. It returns the same draft.
//
// Example:
//
//	encrypter, err := crypto.New(driver, func(encrypter *crypto.Builder) {
//		encrypter.Telemetry(telemetry.New(nil))
//	})
//	if err != nil {
//		return err
//	}
//	_ = encrypter
func (encrypter *Builder) Telemetry(service *telemetry.Telemetry) *Builder {
	encrypter.telemetry = service

	return encrypter
}
