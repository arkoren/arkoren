package federation

import (
	"context"
	"errors"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"

	"arkoren.dev/ark/access/internal/storage"
)

// Builder configures [NewLinks] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	links, err := federation.NewLinks(provider, db, func(links *federation.Builder) {
//		links.SignUp(true).Create(createUser).Fallback(hasPassword)
//	})
type Builder struct {
	instance string
	events   *event.Bus
	signUp   bool
	create   func(context.Context, auth.External) (string, error)
	fallback func(context.Context, string) (bool, error)
	verified func(context.Context, string) (bool, error)
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure federation.BuilderFunc = func(links *federation.Builder) {
//		links.Instance("tenant_a")
//	}
//	links, err := federation.NewLinks(provider, db, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: the table is prefixed "<instance>_" and the
// migration group gains ":<instance>". The name is 1 to 20 bytes of [a-z0-9_]
// starting with a letter; the default is the empty instance.
//
// Example:
//
//	links.Instance("tenant_a")
func (links *Builder) Instance(name string) *Builder {
	links.instance = name

	return links
}

// Events publishes [LinkCreated] and [LinkDeleted] on bus. A nil bus publishes
// nothing.
//
// Example:
//
//	links.Events(events)
func (links *Builder) Events(bus *event.Bus) *Builder {
	links.events = bus

	return links
}

// SignUp lets a first login with an unknown external identity create a user
// ([Builder.Create]) and connect the identity to it; the default is false. With
// false such a login fails with [ErrNoAccount] (403) and creates nothing, so
// only people the application already has can sign in with a provider: they log
// in another way and connect it, or the application adds the user first.
// Logging in with an identity already connected, and a logged-in user
// connecting a provider, never need it. [NewLinks] fails with
// [ErrInvalidConfig] when it is true and no [Builder.Create] is set.
//
// Example:
//
//	links.SignUp(true)
func (links *Builder) SignUp(enabled bool) *Builder {
	links.signUp = enabled

	return links
}

// Create sets the function that makes a local user for an external identity
// that is not connected to anyone, during a logged-out login, when
// [Builder.SignUp] is enabled. It returns the new
// user's subject; its errors pass through to the client (a taken email is
// a 409 with the directory). Use [auth.External.EmailVerified] to decide
// whether the new user's email counts as verified. The package never links by
// email: Create must not return an existing user.
//
// Example:
//
//	links.Create(func(ctx context.Context, external auth.External) (string, error) {
//		if external.EmailVerified {
//			return directory.CreateVerified(ctx, external.Email)
//		}
//		return directory.Create(ctx, external.Email)
//	})
func (links *Builder) Create(create func(ctx context.Context, external auth.External) (string, error)) *Builder {
	links.create = create

	return links
}

// Fallback sets the check that tells whether a user can still log in without
// their connections, for example whether they have a password. Without it the
// last connection of a user can never be removed.
//
// Example:
//
//	links.Fallback(passwords.Has)
func (links *Builder) Fallback(check func(ctx context.Context, subject string) (bool, error)) *Builder {
	links.fallback = check

	return links
}

// Verified sets the check that tells whether a user's email is verified.
// [Links.ConnectHandler] then answers 403 ([account.ErrEmailUnverified]'s
// problem) to an unverified user: whoever registered an address first must not
// attach a provider before its owner proves the address. Without it the handler
// admits everyone, which is only right when the application enforces the rule
// itself; the facade always sets it with the built-in users.
//
// Example:
//
//	links.Verified(directory.Verified)
func (links *Builder) Verified(check func(ctx context.Context, subject string) (bool, error)) *Builder {
	links.verified = check

	return links
}

// validate checks the settings no setter can check alone.
func (links *Builder) validate() error {
	if links.signUp && links.create == nil {
		return errors.New("SignUp needs a Create function")
	}

	return storage.ValidInstance(links.instance)
}
