package hash

import "arkoren.dev/core/service/telemetry"

// Builder configures aggregate metrics at construction. New publishes an
// independent settings snapshot; the telemetry service itself remains shared.
//
// Example:
//
//	hasher := hash.New(driver, func(hasher *hash.Builder) {
//		hasher.Telemetry(metrics)
//	})
//	_ = hasher
type Builder struct {
	// telemetry selects metrics; nil follows telemetry.Default on each call.
	telemetry *telemetry.Telemetry
}

// BuilderFunc configures one fresh draft directly, synchronously and once in
// order. Nil entries panic; expanded nil or empty slices supply no edits. New
// copies the service selection, not the service object, and does not retain
// construction callbacks or clone the state referenced by the driver.
//
// Example:
//
//	var policy hash.BuilderFunc = func(hasher *hash.Builder) {
//		hasher.Telemetry(metrics)
//	}
//	hasher := hash.New(driver, policy)
//	_ = hasher
type BuilderFunc = func(*Builder)

// Telemetry selects aggregate metrics without traces or events. Nil resolves
// telemetry.Default per call; telemetry.New(nil) explicitly disables metrics.
// The service is shared, not copied. This method returns the same draft.
//
// Example:
//
//	hasher := hash.New(driver, func(hasher *hash.Builder) {
//		hasher.Telemetry(telemetry.New(nil))
//	})
//	_ = hasher
func (hasher *Builder) Telemetry(service *telemetry.Telemetry) *Builder {
	hasher.telemetry = service

	return hasher
}
