package auth

import (
	"context"
	"net/http"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/session"
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

// Defaults of [GuardBuilder].
const (
	// defaultChallengeTTL is how long a pending login waits for its second factor.
	defaultChallengeTTL = 5 * time.Minute

	// defaultMaxAttempts is how many wrong second factors discard a pending login.
	defaultMaxAttempts = 5

	// defaultMaxLogins is how many login records one user keeps.
	defaultMaxLogins = 50
)

// guardSettings configures a [Guard].
type guardSettings struct {
	// Handle selects the session bridge; zero selects the default.
	Handle session.Handle

	// ChallengeTTL is how long a pending login is accepted.
	ChallengeTTL time.Duration

	// MaxAttempts is the number of [Guard.Fail] calls per user, within one
	// [GuardBuilder.ChallengeTTL] window, that lock the user out of the second factor.
	MaxAttempts int

	// SecondFactors are the checks that decide whether a user needs a second factor.
	SecondFactors []func(ctx context.Context, subject string) (bool, error)

	// Devices selects whether login records keep the user agent and address.
	Devices bool

	// ClientIP returns the address stored in a login record.
	ClientIP func(*http.Request) string

	// MaxLogins is the number of login records kept per user.
	MaxLogins int

	// SessionLifetime is the lifetime of the login-record list.
	SessionLifetime time.Duration
}

// GuardBuilder configures [NewGuard] before publication. Callbacks run
// directly once, synchronously in order on one fresh draft; nil entries panic.
// Methods return the same draft, and retaining it cannot change a published
// guard.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.ChallengeTTL(10 * time.Minute).MaxAttempts(3)
//	})
type GuardBuilder struct {
	// settings holds the private construction values until publication.
	settings guardSettings
}

// GuardBuilderFunc configures a [GuardBuilder].
//
// Example:
//
//	policy := auth.GuardBuilderFunc(func(policy *auth.GuardBuilder) {
//		policy.MaxAttempts(3)
//	})
//	guard, err := auth.NewGuard(users, store, policy)
type GuardBuilderFunc = func(*GuardBuilder)

// Handle selects the request session bridge shared with [session.Middleware],
// so one application can run several independent sessions. Zero, the default,
// selects the default bridge. It changes neither cookie nor storage policy. The
// setting replaces the previous value on this draft.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.Handle(adminSession)
//	})
func (policy *GuardBuilder) Handle(handle session.Handle) *GuardBuilder {
	policy.settings.Handle = handle

	return policy
}

// ChallengeTTL sets how long a pending login (see [Guard.Begin]) accepts its
// second factor. The default is five minutes. [NewGuard] rejects a value of
// zero or less. The setting replaces the previous value on this draft.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.ChallengeTTL(10 * time.Minute)
//	})
func (policy *GuardBuilder) ChallengeTTL(ttl time.Duration) *GuardBuilder {
	policy.settings.ChallengeTTL = ttl

	return policy
}

// MaxAttempts sets how many second-factor code checks, each reserved by a
// successful [Guard.Pending], one user gets within a [GuardBuilder.ChallengeTTL]
// window and across all sessions, before the pending login is discarded and the
// user is locked out of the second factor until the window ends. The
// default is five. [NewGuard] rejects a value of zero or less. The setting
// replaces the previous value on this draft.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.MaxAttempts(3)
//	})
func (policy *GuardBuilder) MaxAttempts(attempts int) *GuardBuilder {
	policy.settings.MaxAttempts = attempts

	return policy
}

// SecondFactor registers a check that reports whether subject must present a
// second factor before [Guard.Begin] logs them in, for example an
// "is two-factor enrolled" lookup of an ark. Core receives plain functions, so it
// never imports the package that owns the answer. It may be called several
// times: one yes from any check is enough to require a second factor, and the
// first error stops the login and is returned by Begin. A nil check makes
// [NewGuard] fail.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.SecondFactor(factors.Enrolled)
//	})
func (policy *GuardBuilder) SecondFactor(check func(ctx context.Context, subject string) (bool, error)) *GuardBuilder {
	policy.settings.SecondFactors = append(policy.settings.SecondFactors, check)

	return policy
}

// Devices selects whether login records keep the User-Agent and client address
// of each login, which is what makes the device list readable. The default is
// true. With false the records hold only IDs and times. Both values are personal
// data; they are never put in telemetry or events. The setting replaces the
// previous value on this draft.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.Devices(false)
//	})
func (policy *GuardBuilder) Devices(enabled bool) *GuardBuilder {
	policy.settings.Devices = enabled

	return policy
}

// ClientIP sets how the address of a login record is found. The default is the
// host of r.RemoteAddr, which behind a reverse proxy is the proxy. Behind a
// proxy, pass a function that trusts forwarded headers only from your proxies:
// ratelimit.TrustedProxyKey (package core/http/middleware/ratelimit) returns
// such a function. It reports IPv4 addresses as they are but reduces IPv6
// addresses to their /64 prefix, which is what the record then stores. The
// result is cut to 255 bytes. [NewGuard] rejects nil. The setting replaces the
// previous value on this draft.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.ClientIP(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
//	})
func (policy *GuardBuilder) ClientIP(find func(*http.Request) string) *GuardBuilder {
	policy.settings.ClientIP = find

	return policy
}

// MaxLogins sets how many login records one user keeps. The default is 50. A new
// login beyond it drops the oldest record, which logs that device out.
// [NewGuard] rejects a value of zero or less. The setting replaces the previous
// value on this draft.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.MaxLogins(10)
//	})
func (policy *GuardBuilder) MaxLogins(limit int) *GuardBuilder {
	policy.settings.MaxLogins = limit

	return policy
}

// SessionLifetime tells the guard the maximum session lifetime configured on
// [session.Middleware] with [session.Builder.MaxLifetime], which the session
// package does not expose to other packages. A user's list of login records
// lives that long after its last write, and a record older than it is never
// listed and is removed by the next write, so no record outlives its sessions. The
// default is [session.DefaultMaxLifetime]; set it when the middleware uses
// another value. [NewGuard] rejects a value of zero or less. The setting
// replaces the previous value on this draft.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.SessionLifetime(7 * 24 * time.Hour)
//	})
func (policy *GuardBuilder) SessionLifetime(lifetime time.Duration) *GuardBuilder {
	policy.settings.SessionLifetime = lifetime

	return policy
}

// newGuardSettings applies the callbacks once on a draft holding the defaults
// and returns the completed settings. The check slice is owned by the result.
func newGuardSettings(configure ...GuardBuilderFunc) guardSettings {
	policy := &GuardBuilder{settings: guardSettings{
		ChallengeTTL: defaultChallengeTTL,
		MaxAttempts:  defaultMaxAttempts,

		Devices:         true,
		ClientIP:        remoteHost,
		MaxLogins:       defaultMaxLogins,
		SessionLifetime: session.DefaultMaxLifetime,
	}}

	for _, configure := range configure {
		configure(policy)
	}

	return policy.settings
}
