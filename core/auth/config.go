package auth

import (
	"arkoren.dev/core/event"
	"arkoren.dev/foundation/problem"
)

// userProviderSettings configures a [UserProvider].
type userProviderSettings struct {
	// Events receives [UserForgotten] after a successful [UserProvider.Forget].
	// The default, nil, emits nothing.
	Events *event.Bus
}

// UserProviderBuilder configures [NewUserProvider] before publication.
// Callbacks run directly once, synchronously in order on one fresh draft; nil
// entries panic and expanded nil or empty slices supply no edits. Methods
// return the same draft, and retaining it cannot change a published provider.
//
// Example:
//
//	users, err := auth.NewUserProvider(driver, func(users *auth.UserProviderBuilder) {
//		users.Events(app.Events())
//	})
type UserProviderBuilder struct {
	// settings holds the private construction values until publication.
	settings userProviderSettings
}

// Events sets the bus that receives [UserForgotten]. Nil, the default, emits
// nothing. The bus is a shared service, not copied.
//
// This setting replaces the previous value on the construction draft.
//
// Example:
//
//	users, err := auth.NewUserProvider(driver, func(users *auth.UserProviderBuilder) {
//		users.Events(event.New())
//	})
func (builder *UserProviderBuilder) Events(events *event.Bus) *UserProviderBuilder {
	builder.settings.Events = events

	return builder
}

// UserProviderBuilderFunc configures a [UserProviderBuilder].
//
// Example:
//
//	var policy auth.UserProviderBuilderFunc = func(users *auth.UserProviderBuilder) {
//		users.Events(events)
//	}
//	users, err := auth.NewUserProvider(driver, policy)
type UserProviderBuilderFunc = func(*UserProviderBuilder)

// newUserProviderSettings applies the callbacks once and returns the completed
// settings.
func newUserProviderSettings(configure ...UserProviderBuilderFunc) userProviderSettings {
	builder := &UserProviderBuilder{}

	for _, configure := range configure {
		configure(builder)
	}

	return builder.settings
}

// MiddlewareBuilderFunc edits one [Require] draft synchronously. Callbacks run
// directly once in order; nil entries panic and expanded nil/empty slices omit
// edits. The completed settings detach from the draft, not event services or
// captures.
//
// Example:
//
//	var policy auth.MiddlewareBuilderFunc = func(authenticator *auth.MiddlewareBuilder) {
//		authenticator.Events(events)
//	}
//	r.Use(auth.Require(policy))
type MiddlewareBuilderFunc = func(*MiddlewareBuilder)

// middlewareSettings configures [Require].
type middlewareSettings struct {
	// ErrorResponse is the problem returned for anonymous requests. Zero
	// uses [ErrUnauthenticated] with a "WWW-Authenticate: Bearer" challenge.
	ErrorResponse problem.Details

	// Events receives [Rejected] when [Require] rejects a request. The
	// default, nil, publishes nothing.
	Events *event.Bus
}

// newMiddlewareSettings applies construction callbacks and publishes the
// completed settings, independent of the draft. Normalization stays after the
// final callback.
func newMiddlewareSettings(configure ...MiddlewareBuilderFunc) middlewareSettings {
	authenticator := &MiddlewareBuilder{}

	for _, configure := range configure {
		configure(authenticator)
	}

	return authenticator.settings
}
