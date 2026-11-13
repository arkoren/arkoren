package auth

import (
	"context"
	"errors"
	"slices"
	"sync"

	"arkoren.dev/core/event"
)

var (
	// ErrUserNotFound reports that no user has the subject. Only this error
	// proves a user is deleted: code that removes data of deleted users must act
	// on it and on nothing else, so a disabled user's data is never removed.
	// A subject that cannot be parsed is [ErrInvalidSubject], never this error.
	//
	// Example:
	//
	//	if _, err := users.Find(ctx, subject); errors.Is(err, auth.ErrUserNotFound) {
	//		return users.Forget(ctx, subject)
	//	}
	ErrUserNotFound = errors.New("auth: user not found")

	// ErrUserDisabled reports that the user exists but may not act, for example
	// a suspended account. The user is not deleted: keep its data.
	//
	// Example:
	//
	//	if _, err := users.Find(ctx, subject); errors.Is(err, auth.ErrUserDisabled) {
	//		return nil // Skip the user, keep the data.
	//	}
	ErrUserDisabled = errors.New("auth: user disabled")
)

// UserRecord is what a [UserDriver] knows about one user.
//
// Example:
//
//	record, err := users.Find(ctx, "42")
//	if err != nil {
//		return err
//	}
//	fmt.Println(record.Subject, record.Version, record.Claims["role"])
type UserRecord struct {
	// Subject is the user's subject as passed to Find.
	Subject string

	// Version is an opaque string that changes whenever the user changes in a
	// way that should invalidate what was derived from it, such as a password
	// version. Empty means the driver has no version.
	Version string

	// Claims are application-defined attributes of the user. Treat the map as
	// read only: drivers may share it.
	Claims map[string]any

	// Value is the driver's own user value, such as the application's user
	// struct. Its type is whatever the driver documents; nil if none.
	Value any
}

// UserDriver finds users by subject. Implementations must return an error
// wrapping [ErrInvalidSubject] for a subject that fails [ValidateSubject],
// [ErrUserNotFound] for a missing user and [ErrUserDisabled] for a disabled
// one, with the record's Subject set to the argument. They must be safe for
// concurrent use. The conformance suite is [arkoren.dev/core/auth/authtest].
//
// Example:
//
//	type directory struct{}
//
//	func (directory) Find(ctx context.Context, subject string) (auth.UserRecord, error) {
//		return auth.UserRecord{}, auth.ErrUserNotFound
//	}
type UserDriver interface {
	// Find returns the record of subject.
	//
	// Example:
	//
	//	record, err := driver.Find(ctx, "42")
	Find(ctx context.Context, subject string) (UserRecord, error)
}

// UserProvider looks users up through a [UserDriver] and coordinates cleanup
// when a user is forgotten. It is safe for concurrent use.
//
// Example:
//
//	users, err := auth.NewUserProvider(driver)
//	if err != nil {
//		return err
//	}
//	record, err := users.Find(ctx, "42")
type UserProvider struct {
	// driver finds users.
	driver UserDriver

	// events receives [UserForgotten]; nil emits nothing.
	events *event.Bus

	// mu protects hooks, so registration may race with Forget.
	mu sync.Mutex

	// hooks are the forget hooks in registration order.
	hooks []func(context.Context, string) error
}

// NewUserProvider returns a provider over driver. It returns an error when
// driver is nil. Construction callbacks configure [UserProviderBuilder].
//
// Example:
//
//	users, err := auth.NewUserProvider(driver, func(users *auth.UserProviderBuilder) {
//		users.Events(app.Events())
//	})
func NewUserProvider(driver UserDriver, configure ...UserProviderBuilderFunc) (*UserProvider, error) {
	if driver == nil {
		return nil, errors.New("auth: nil user driver")
	}

	return &UserProvider{
		driver: driver,
		events: newUserProviderSettings(configure...).Events,
	}, nil
}

// Find returns the record of subject from the driver. Errors are the driver's:
// see [UserDriver] for [ErrInvalidSubject], [ErrUserNotFound] and
// [ErrUserDisabled].
//
// Example:
//
//	record, err := users.Find(ctx, subject)
//	if errors.Is(err, auth.ErrUserNotFound) {
//		return nil // The user was deleted.
//	}
func (provider *UserProvider) Find(ctx context.Context, subject string) (UserRecord, error) {
	return provider.driver.Find(ctx, subject)
}

// OnForget registers hook to delete the data a package keeps about a user. Hooks
// run in registration order by [UserProvider.Forget]. Deletes should be
// idempotent, so there are no names and no duplicate check. It is safe to call
// concurrently with Forget; a Forget already running uses the hooks registered
// before it started. It panics when hook is nil.
//
// Example:
//
//	users.OnForget(func(ctx context.Context, subject string) error {
//		return subscriptions.DeleteBySubject(ctx, subject)
//	})
func (provider *UserProvider) OnForget(hook func(ctx context.Context, subject string) error) {
	if hook == nil {
		panic("auth: nil forget hook")
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()

	provider.hooks = append(provider.hooks, hook)
}

// Forget removes the data of a user by running every hook registered with
// [UserProvider.OnForget] synchronously in ctx, in registration order, so the
// hooks join a transaction carried by ctx. The first hook error stops the run
// and is returned unchanged; hook panics propagate. It returns an error
// wrapping [ErrInvalidSubject] without running hooks for an invalid subject.
// After every hook succeeded it emits [UserForgotten] on the configured bus,
// and never after a failure. Forget does not look the user up.
//
// Example:
//
//	err := db.WithTransaction(ctx, func(ctx context.Context) error {
//		if err := removeAccount(ctx, id); err != nil {
//			return err
//		}
//		return users.Forget(ctx, subject)
//	})
func (provider *UserProvider) Forget(ctx context.Context, subject string) error {
	if err := ValidateSubject(subject); err != nil {
		return err
	}

	provider.mu.Lock()
	hooks := slices.Clone(provider.hooks)
	provider.mu.Unlock()

	for _, hook := range hooks {
		if err := hook(ctx, subject); err != nil {
			return err
		}
	}

	provider.events.Emit(ctx, UserForgotten, UserForgottenEvent{Subject: subject})

	return nil
}
