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

// Defaults of [LoginBuilder].
const (
	// defaultChallengeTTL is how long a pending login waits for its second factor.
	defaultChallengeTTL = 5 * time.Minute

	// defaultMaxAttempts is how many wrong second factors discard a pending login.
	defaultMaxAttempts = 5

	// defaultMaxLogins is how many login records one user keeps.
	defaultMaxLogins = 50
)

// loginSettings configures a [Login].
type loginSettings struct {
	// Handle selects the session bridge; zero selects the default.
	Handle session.Handle

	// ChallengeTTL is how long a pending login is accepted.
	ChallengeTTL time.Duration

	// MaxAttempts is the number of [Login.Fail] calls that discard a pending login.
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

// LoginBuilder configures [NewLogin] before publication. Callbacks run
// directly once, synchronously in order on one fresh draft; nil entries panic.
// Methods return the same draft, and retaining it cannot change a published
// login.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.ChallengeTTL(10 * time.Minute).MaxAttempts(3)
//	})
type LoginBuilder struct {
	// settings holds the private construction values until publication.
	settings loginSettings
}

// LoginBuilderFunc configures a [LoginBuilder].
//
// Example:
//
//	policy := auth.LoginBuilderFunc(func(policy *auth.LoginBuilder) {
//		policy.MaxAttempts(3)
//	})
//	login, err := auth.NewLogin(users, store, policy)
type LoginBuilderFunc = func(*LoginBuilder)

// Handle selects the request session bridge shared with [session.Middleware],
// so one application can run several independent sessions. Zero, the default,
// selects the default bridge. It changes neither cookie nor storage policy. The
// setting replaces the previous value on this draft.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.Handle(adminSession)
//	})
func (policy *LoginBuilder) Handle(handle session.Handle) *LoginBuilder {
	policy.settings.Handle = handle

	return policy
}

// ChallengeTTL sets how long a pending login (see [Login.Begin]) accepts its
// second factor. The default is five minutes. [NewLogin] rejects a value of
// zero or less. The setting replaces the previous value on this draft.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.ChallengeTTL(10 * time.Minute)
//	})
func (policy *LoginBuilder) ChallengeTTL(ttl time.Duration) *LoginBuilder {
	policy.settings.ChallengeTTL = ttl

	return policy
}

// MaxAttempts sets how many calls to [Login.Fail] discard a pending login. The
// default is five. [NewLogin] rejects a value of zero or less. The setting
// replaces the previous value on this draft.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.MaxAttempts(3)
//	})
func (policy *LoginBuilder) MaxAttempts(attempts int) *LoginBuilder {
	policy.settings.MaxAttempts = attempts

	return policy
}

// SecondFactor registers a check that reports whether subject must present a
// second factor before [Login.Begin] logs them in, for example an
// "is two-factor enrolled" lookup of an ark. Core receives plain functions, so it
// never imports the package that owns the answer. It may be called several
// times: one yes from any check is enough to require a second factor, and the
// first error stops the login and is returned by Begin. A nil check makes
// [NewLogin] fail.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.SecondFactor(factors.Enrolled)
//	})
func (policy *LoginBuilder) SecondFactor(check func(ctx context.Context, subject string) (bool, error)) *LoginBuilder {
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
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.Devices(false)
//	})
func (policy *LoginBuilder) Devices(enabled bool) *LoginBuilder {
	policy.settings.Devices = enabled

	return policy
}

// ClientIP sets how the address of a login record is found. The default is the
// host of r.RemoteAddr, which behind a reverse proxy is the proxy. Behind a
// proxy, pass a function that trusts forwarded headers only from your proxies:
// ratelimit.TrustedProxyKey (package core/http/middleware/ratelimit) returns
// such a function. It reports IPv4 addresses as they are but reduces IPv6
// addresses to their /64 prefix, which is what the record then stores. The
// result is cut to 255 bytes. [NewLogin] rejects nil. The setting replaces the
// previous value on this draft.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.ClientIP(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
//	})
func (policy *LoginBuilder) ClientIP(find func(*http.Request) string) *LoginBuilder {
	policy.settings.ClientIP = find

	return policy
}

// MaxLogins sets how many login records one user keeps. The default is 50. A new
// login beyond it drops the oldest record, which logs that device out.
// [NewLogin] rejects a value of zero or less. The setting replaces the previous
// value on this draft.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.MaxLogins(10)
//	})
func (policy *LoginBuilder) MaxLogins(limit int) *LoginBuilder {
	policy.settings.MaxLogins = limit

	return policy
}

// SessionLifetime tells the login the maximum session lifetime configured on
// [session.Middleware] with [session.Builder.MaxLifetime], which the session
// package does not expose to other packages. A user's list of login records
// lives that long after its last write, and a record older than it is never
// listed and is removed by the next write, so no record outlives its sessions. The
// default is [session.DefaultMaxLifetime]; set it when the middleware uses
// another value. [NewLogin] rejects a value of zero or less. The setting
// replaces the previous value on this draft.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.SessionLifetime(7 * 24 * time.Hour)
//	})
func (policy *LoginBuilder) SessionLifetime(lifetime time.Duration) *LoginBuilder {
	policy.settings.SessionLifetime = lifetime

	return policy
}

// newLoginSettings applies the callbacks once on a draft holding the defaults
// and returns the completed settings. The check slice is owned by the result.
func newLoginSettings(configure ...LoginBuilderFunc) loginSettings {
	policy := &LoginBuilder{settings: loginSettings{
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
