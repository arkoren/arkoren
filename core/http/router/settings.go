package router

import "arkoren.dev/core/event"

// recoverSettings configures [Recover].
type recoverSettings struct {
	// Handler converts a recovered panic value into an error. A nil function
	// selects the default conversion; a nil result uses ErrRecoverUnexpected.
	Handler func(value any) error

	// Events receives [PanicRecovered]. Nil, the default, publishes nothing.
	Events *event.Bus
}

// newRecoverSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newRecoverSettings(configure ...RecoverBuilderFunc) recoverSettings {
	recovery := &RecoverBuilder{
		settings: recoverSettings{},
	}

	for _, configure := range configure {
		configure(recovery)
	}

	config := recovery.settings

	return config
}
