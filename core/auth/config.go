package auth

import (
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

	// defaultFresh is how recently a factor must have been verified to count as
	// fresh.
	defaultFresh = 15 * time.Minute
)

// guardSettings configures a [Guard].
type guardSettings struct {
	// Handle selects the session bridge; zero selects the default.
	Handle session.Handle

	// ChallengeTTL is how long a pending login is accepted.
	ChallengeTTL time.Duration

	// MaxAttempts is the number of credential checks per user, within one
	// [GuardBuilder.ChallengeTTL] window, that lock the user out of the second
	// factor and of step-up.
	MaxAttempts int

	// SecondFactors are the credentials that may finish a parked login, in
	// registration order.
	SecondFactors []Credential

	// StepUps are the credentials that may re-prove a logged-in user, in
	// registration order.
	StepUps []Credential

	// Fresh is how recent a factor must be to count as fresh.
	Fresh time.Duration

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

// MaxAttempts sets how many credential checks, each reserved by
// [Guard.ChallengeHandler] or [Guard.ConfirmHandler] before the credential is
// verified, one user gets within a [GuardBuilder.ChallengeTTL] window, across
// all methods and sessions, before a pending login is discarded and the user is
// locked out of the second factor and of step-up until the window ends. A check
// that fails with an infrastructure error is refunded. The default is five. [NewGuard] rejects a value of zero or less. The setting
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

// SecondFactor registers credentials that may finish a parked login. A user who
// is enrolled in any of them, and whose login did not already prove two kinds of
// factor, must present one of their enrolled methods before [Guard.Begin] logs
// them in; [Guard.ChallengeHandler] verifies it. Core never imports the module
// that owns the credential: it receives plain functions in a [Credential]. The
// call may be repeated; credentials keep registration order, which is the order
// of [Outcome.Methods]. [NewGuard] rejects a duplicate method within this
// registry, an empty method and a nil [Credential.Enrolled]. The same credential
// may also be registered with [GuardBuilder.StepUp].
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.SecondFactor(totp.Credential())
//	})
func (policy *GuardBuilder) SecondFactor(credentials ...Credential) *GuardBuilder {
	policy.settings.SecondFactors = append(policy.settings.SecondFactors, credentials...)

	return policy
}

// StepUp registers credentials that may re-prove a logged-in user:
// [Guard.ConfirmHandler] verifies one and records the factor on the session, and
// [Guard.Fresh] counts only these methods as fresh when the user has one
// enrolled. Register the methods a user can use to prove themselves again, such
// as the password and an authenticator, not a federated login. The call may be
// repeated; credentials keep registration order, which is the order of the
// methods in [ErrStepUpRequired]. [NewGuard] applies the same checks as for
// [GuardBuilder.SecondFactor].
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.StepUp(passwords.Credential(), totp.Credential())
//	})
func (policy *GuardBuilder) StepUp(credentials ...Credential) *GuardBuilder {
	policy.settings.StepUps = append(policy.settings.StepUps, credentials...)

	return policy
}

// FreshFor sets how recent a factor must be for [Guard.Fresh] to accept it. The
// default is 15 minutes. [Guard.RequireFresh] takes its own window per route.
// [NewGuard] rejects a value of zero or less. The setting replaces the previous
// value on this draft.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.FreshFor(5 * time.Minute)
//	})
func (policy *GuardBuilder) FreshFor(window time.Duration) *GuardBuilder {
	policy.settings.Fresh = window

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
// and returns the completed settings. The credential slices are owned by the
// result only after [NewGuard] clones them.
func newGuardSettings(configure ...GuardBuilderFunc) guardSettings {
	policy := &GuardBuilder{settings: guardSettings{
		ChallengeTTL: defaultChallengeTTL,
		MaxAttempts:  defaultMaxAttempts,
		Fresh:        defaultFresh,

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
