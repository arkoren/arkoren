package auth

import (
	"arkoren.dev/core/event"
	"arkoren.dev/foundation/problem"
)

// BuilderFunc edits one Require draft synchronously. Callbacks run directly once
// in order; nil entries panic and expanded nil/empty slices omit edits. The
// completed settings detach from the draft, not event services or captures.
//
// Example:
//
//	var policy auth.BuilderFunc = func(authenticator *auth.Builder) {
//		authenticator.Events(events)
//	}
//	r.Use(auth.Require[User](policy))
type BuilderFunc = func(*Builder)

// settings configures [Require].
type settings struct {
	// ErrorResponse is the problem returned for anonymous requests. Zero
	// uses [ErrUnauthenticated] with a "WWW-Authenticate: Bearer" challenge.
	ErrorResponse problem.Details

	// Events receives [Rejected] when [Require] rejects a request. The
	// default, nil, publishes nothing.
	Events *event.Bus
}

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	authenticator := &Builder{
		settings: settings{},
	}

	for _, configure := range configure {
		configure(authenticator)
	}

	config := authenticator.settings

	return config
}
