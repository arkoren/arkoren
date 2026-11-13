package gate

import "arkoren.dev/core/event"

// BuilderFunc edits one Gate draft synchronously. Callbacks run directly once in
// order; nil entries panic and expanded nil/empty slices omit edits. Publication
// detaches the draft's settings while keeping the event bus shared.
//
// Example:
//
//	var policy gate.BuilderFunc = func(access *gate.Builder) {
//		access.Events(events)
//	}
//	permissions := gate.New(policy)
//	r.Use(permissions.Require(isAdmin))
type BuilderFunc = func(*Builder)

// settings configures a [Gate].
type settings struct {
	// Events receives [Denied]. The default, nil, publishes nothing.
	Events *event.Bus
}

// Builder configures New before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	policy := gate.New(func(access *gate.Builder) {
//		access.Events(event.New())
//	})
//	admin := router.New()
//	admin.Use(policy.Require(isAdmin))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	gate := &Builder{}

	for _, configure := range configure {
		configure(gate)
	}

	config := gate.settings

	return config
}

// Events receives [Denied]. The default, nil, publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	policy := gate.New(func(access *gate.Builder) {
//		access.Events(event.New())
//	})
//	admin := router.New()
//	admin.Use(policy.Require(isAdmin))
func (access *Builder) Events(bus *event.Bus) *Builder {
	access.settings.Events = bus

	return access
}
