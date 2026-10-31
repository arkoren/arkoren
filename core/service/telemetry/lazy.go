package telemetry

import "arkoren.dev/core/service/telemetry/internal/lazy"

// Instruments caches the instruments a component builds for one [Telemetry],
// so a component that follows [Default] at use time neither rebuilds them on
// every call nor leaks one set per call. Create one per component with
// [NewInstruments] and keep it for the component's lifetime. It is safe for
// concurrent use and must not be copied after first use.
//
//	instruments := telemetry.NewInstruments(func(service *telemetry.Telemetry) *telemetry.Tracer {
//		tracer := service.Tracer("example.com/cache")
//		return &tracer
//	})
//	tracer := instruments.For(telemetry.Default())
//	_ = tracer
type Instruments[T any] struct {
	// cache holds the most recently built value.
	cache lazy.Cache[*Telemetry, T]
}

// NewInstruments returns an Instruments whose build function creates the
// tracer, meters, and instruments of a component for the telemetry it is
// given. Concurrent misses are serialized: build runs once for the current
// telemetry, then again only when For resolves a different telemetry. Only the
// latest value is retained; switching back rebuilds it. build must not call For
// on the Instruments being built, and must synchronize any shared captured state
// accessed outside the callback. Callback panics propagate without publication.
//
//	instruments := telemetry.NewInstruments(func(service *telemetry.Telemetry) *telemetry.Tracer {
//		tracer := service.Tracer("example.com/cache")
//		return &tracer
//	})
//	tracer := instruments.For(telemetry.Default())
//	_, span := tracer.Start(ctx, "get")
//	span.End()
func NewInstruments[T any](build func(*Telemetry) *T) *Instruments[T] {
	return &Instruments[T]{cache: lazy.NewCache(build)}
}

// For returns the value built for t, building it on the first call and when t
// differs from the previous call. It is safe for concurrent use.
//
//	metrics := instruments.For(telemetry.Default())
func (instruments *Instruments[T]) For(t *Telemetry) *T {
	return instruments.cache.Get(t)
}
