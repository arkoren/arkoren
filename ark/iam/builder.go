package iam

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/iam/account"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/internal/web"
	"arkoren.dev/ark/iam/password"
	"arkoren.dev/ark/iam/twofactor"
)

// Builder configures [New] before publication. Each feature is off until its
// method is called, and so are the two ways new users appear on their own:
// self-service registration ([password.Builder.Registration]) and sign-up with a
// provider ([ProviderBuilder.SignUp]). Until one is enabled the application
// adds users itself, through [IAM.Directory] or its own users. Callbacks run
// directly once, in order, on one fresh draft; methods return the same draft,
// and retaining it cannot change a published [IAM].
//
// Settings that hold hooks and lists add up across calls ([Builder.OnEmailProven],
// [Builder.Directory], [Builder.Provider], [Builder.SecondFactor],
// [Builder.StepUp], [Builder.Authenticator], [Builder.Guard] and
// [Builder.Session]); a nil hook never clears an earlier one. [Builder.Password],
// [Builder.TwoFactor] and [Builder.Tokens] replace their earlier call
// as a whole instead, so the last call is the complete configuration of that
// feature.
//
// Example:
//
//	scheme := totp.New("Acme") // the name authenticator apps show for this account
//	identity, err := iam.New(db, store, func(identity *iam.Builder) {
//		identity.Password(hasher)
//		identity.TwoFactor(scheme, cipher)
//		identity.Tokens()
//		identity.Provider(google)
//		identity.Provider(github, func(login *iam.ProviderBuilder) {
//			login.SignUp(true)
//		})
//	})
type Builder struct {
	clientIP        func(*http.Request) string
	instance        string
	events          *event.Bus
	users           *Users
	directorySet    bool
	directory       []account.BuilderFunc
	hasher          *hash.Hasher
	password        []password.BuilderFunc
	twoFactor       bool
	cipher          *crypto.Encrypter
	scheme          totp.Scheme
	twofactor       []twofactor.BuilderFunc
	tokens          bool
	apitoken        []apitoken.BuilderFunc
	providers       []providerSetup
	secondFactor    []auth.Credential
	stepUp          []auth.Credential
	authenticators  []auth.Authenticator
	guard           []auth.GuardBuilderFunc
	sessionPolicy   []session.BuilderFunc
	sessionLifetime time.Duration
	keepPersonal    bool
	emailProven     func(context.Context, string) error
}

// ClientIP selects the address used by login records and all facade throttles.
// By default, IAM uses the connection host and ignores forwarding headers.
// Calls replace earlier selections; a final nil makes New return
// ErrInvalidConfig. The function and its captures remain shared after
// construction. Return an address without its port; throttles apply IPv6 /64
// grouping independently. Trust forwarded headers only from explicitly trusted
// proxies, for example with arkoren.dev/core/http/clientip.
//
// Example:
//
//	identity, err := iam.New(db, store, func(identity *iam.Builder) {
//		identity.ClientIP(clientip.Cloudflare())
//		identity.Password(hasher)
//	})
func (identity *Builder) ClientIP(clientIP func(*http.Request) string) *Builder {
	identity.clientIP = clientIP

	return identity
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure iam.BuilderFunc = func(identity *iam.Builder) {
//		identity.Password(hasher)
//	}
//	identity, err := iam.New(db, store, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance for every package: tables are prefixed
// "<instance>_" and migration groups gain ":<instance>", so two facades can
// share one database. The name is 1 to 20 bytes of [a-z0-9_] starting with a
// letter; the default is the empty instance.
//
// Example:
//
//	identity.Instance("tenant_a")
func (identity *Builder) Instance(name string) *Builder {
	identity.instance = name

	return identity
}

// Events publishes the events of every package on bus: account, password,
// two-factor, token and connection changes, and the core
// [auth.CredentialEnrolled]. Events carry subjects only, never tokens, codes or
// email addresses. A nil bus publishes nothing. The guard publishes
// [auth.SecondFactorLocked] on it too, for your own listeners; the facade never
// depends on events, because a failing listener must not change a decision.
//
// Example:
//
//	identity.Events(events)
func (identity *Builder) Events(bus *event.Bus) *Builder {
	identity.events = bus

	return identity
}

// Users replaces the built-in users table with the application's own; see
// [Users]. Without it the facade uses the built-in directory, which identifies
// users by email. It cannot be combined with [Builder.Directory]: [New] fails
// with [ErrInvalidConfig].
//
// Example:
//
//	identity.Users(iam.Users{Provider: provider, Lookup: lookup, Create: create, Rotate: rotate})
func (identity *Builder) Users(users Users) *Builder {
	identity.users = &users

	return identity
}

// Directory configures the built-in users table: email verification
// ([account.Builder.Verification]) and the lifetime of its links
// ([account.Builder.VerifyTTL]). The callbacks run after the facade's own
// settings (the instance and the events), which they should leave alone. It cannot be combined with [Builder.Users], which replaces the
// built-in table: [New] then fails with [ErrInvalidConfig].
//
// Example:
//
//	identity.Directory(func(accounts *account.Builder) {
//		accounts.Verification(signer, dispatch, deliver).VerifyTTL(time.Hour)
//	})
func (identity *Builder) Directory(configure ...account.BuilderFunc) *Builder {
	identity.directorySet = true
	identity.directory = append(identity.directory, configure...)

	return identity
}

// Password enables login, password confirmation and change, hashing with
// hasher. Registration and forgotten-password recovery are not part of it:
// enable them with [password.Builder.Registration] and [password.Builder.Reset]
// in configure (the facade then mounts the forgot and reset routes), or add
// users some other way (see [IAM.Directory]). Later calls replace the earlier
// one: the last hasher and the last configure callbacks are all that count, so
// compose the settings into one call. configure customizes the
// password package (the policy, the reset lifetime, registration); the facade has already set the
// instance, the events and the client address. While a user is in a second-factor lockout
// ([auth.Guard.LockedOut]), a correct password of that user is not refunded by
// the login throttle, so starting pending logins that cannot finish keeps
// costing throttle budget. The login asks the guard, so it needs no bus and
// fails closed if the lockout cannot be read.
//
// Example:
//
//	identity.Password(hasher, func(passwords *password.Builder) {
//		passwords.Registration(true).Reset(signer, dispatchReset, deliverReset)
//	})
func (identity *Builder) Password(hasher *hash.Hasher, configure ...password.BuilderFunc) *Builder {
	identity.hasher = hasher
	identity.password = slices.Clone(configure)

	return identity
}

// TwoFactor enables the TOTP authenticator and recovery codes. scheme holds the
// TOTP parameters and the issuer, the service name authenticator apps show (see
// [totp.New]); an invalid scheme, or a nil cipher, makes [New] fail with
// [ErrInvalidConfig]. cipher encrypts the secrets (use a key reserved for them).
// configure customizes the twofactor package. Later calls replace the earlier
// one: the last scheme, cipher and configure callbacks are all that count.
//
// Example:
//
//	scheme := totp.New("Acme") // the name authenticator apps show for this account
//	identity.TwoFactor(scheme, cipher)
func (identity *Builder) TwoFactor(scheme totp.Scheme, cipher *crypto.Encrypter, configure ...twofactor.BuilderFunc) *Builder {
	identity.twoFactor = true
	identity.scheme = scheme
	identity.cipher = cipher
	identity.twofactor = slices.Clone(configure)

	return identity
}

// Tokens enables personal access tokens and device tokens. configure sets the
// scope catalog and the grant ceiling ([apitoken.Builder.Grants]), the kinds
// ([apitoken.Builder.PersonalTokens], [apitoken.Builder.DeviceTokens]), the
// prefix and the lifetime cap of the apitoken package. Without a catalog tokens
// carry no scopes. Later calls replace the earlier one: only the configure
// callbacks of the last call count.
//
// Example:
//
//	identity.Tokens(func(tokens *apitoken.Builder) {
//		tokens.Grants(apitoken.AnyScope, "posts:read", "posts:write")
//	})
func (identity *Builder) Tokens(configure ...apitoken.BuilderFunc) *Builder {
	identity.tokens = true
	identity.apitoken = slices.Clone(configure)

	return identity
}

// KeepPersonalTokens limits what a password change and enabling two-factor
// revoke to the user's device tokens. By default both revoke every token,
// personal ones included: someone who stole the password can log in and create a
// personal token, and a user who then changes the password, or protects the
// account with two-factor, would otherwise leave that token working. Keep
// personal tokens only when your users rely on long-lived tokens for scripts and
// integrations and you accept that trade-off; they can still revoke them one by
// one. It has no effect without [Builder.Tokens]. A password reset always
// revokes every token, whatever this says.
//
// Example:
//
//	identity.KeepPersonalTokens()
func (identity *Builder) KeepPersonalTokens() *Builder {
	identity.keepPersonal = true

	return identity
}

// OnEmailProven adds a function that runs when a completed password reset, or a
// provider login that verified the email of an unverified user and took the
// account over, proved the mailbox of a user whose email was not verified. It
// runs in that transaction and after the built-in purge of connections,
// authenticator, recovery codes and tokens. Use it to delete what the application or a module
// registered through [Builder.SecondFactor] or [Builder.StepUp] (passkeys, say)
// may have attached in the meantime; the facade cannot know of them. An error
// rolls the reset or the login back. It does not run when the email was already verified.
// Calls add up: the functions run in the order added, all of them even when one
// fails (the errors are joined), and a nil hook changes nothing.
//
// Example:
//
//	identity.OnEmailProven(passkeys.RemoveAll)
func (identity *Builder) OnEmailProven(hook func(ctx context.Context, subject string) error) *Builder {
	identity.emailProven = web.Then(identity.emailProven, hook)

	return identity
}

// Provider adds an external identity provider and its settings. It is mounted
// under its [Provider.Name], and the facade maps its verified identities to
// users with the one connection store the providers share
// (`federation.Links`), so the application writes no resolve function. Configure
// each provider in its own call, with callbacks that run directly once, in order,
// on one fresh [ProviderBuilder]; a name used by two calls makes [New] fail with
// [ErrInvalidConfig]. By default a provider only logs in people the application
// already has: see [ProviderBuilder.SignUp].
//
// Example:
//
//	identity.Provider(google)
//	identity.Provider(entra, func(login *iam.ProviderBuilder) {
//		login.SignUp(true)
//	})
func (identity *Builder) Provider(provider Provider, configure ...ProviderBuilderFunc) *Builder {
	settings := ProviderBuilder{}
	for _, apply := range configure {
		apply(&settings)
	}
	identity.providers = append(identity.providers, providerSetup{provider: provider, settings: settings})

	return identity
}

// SecondFactor registers more second-factor credentials after the built-in
// ones, for third-party modules such as a passkey package. Methods are listed
// to users in registration order.
//
// Example:
//
//	identity.SecondFactor(passkeys.Credential())
func (identity *Builder) SecondFactor(credentials ...auth.Credential) *Builder {
	identity.secondFactor = append(identity.secondFactor, credentials...)

	return identity
}

// StepUp registers more step-up credentials after the built-in ones, for
// third-party modules. Federated logins are not a step-up method by default.
//
// Example:
//
//	identity.StepUp(passkeys.Credential())
func (identity *Builder) StepUp(credentials ...auth.Credential) *Builder {
	identity.stepUp = append(identity.stepUp, credentials...)

	return identity
}

// Authenticator adds authenticators after the guard's and the token issuer's,
// for example one that verifies JWTs, to the chain of [IAM.Middleware].
//
// Example:
//
//	identity.Authenticator(auth.Bearer(verifyJWT))
func (identity *Builder) Authenticator(authenticators ...auth.Authenticator) *Builder {
	identity.authenticators = append(identity.authenticators, authenticators...)

	return identity
}

// Guard customizes the [auth.Guard] the facade builds (the step-up window, the
// attempt limit, the login limit). The callbacks run first and the facade's own
// settings after them, so the facade wins: the session lifetime
// ([Builder.SessionLifetime]), the session handle (always the default one,
// which the provider handlers of ark/oauth use) and the client address source
// set by [Builder.ClientIP] replace whatever the callbacks chose. The event bus of
// [Builder.Events] is set before the callbacks and not asserted again, because
// nothing in the facade depends on it: a callback may replace it, for example to
// send the guard's events elsewhere.
// The credential registries only append: register second-factor and step-up
// credentials with [Builder.SecondFactor] and [Builder.StepUp], not here. A
// credential registered here would be listed to users before the built-in ones,
// and a method the facade also registers makes [New] fail with
// [ErrInvalidConfig] as a duplicate.
//
// Example:
//
//	identity.Guard(func(policy *auth.GuardBuilder) {
//		policy.FreshFor(5 * time.Minute)
//	})
func (identity *Builder) Guard(configure ...auth.GuardBuilderFunc) *Builder {
	identity.guard = append(identity.guard, configure...)

	return identity
}

// Session customizes the session middleware that [IAM.Middleware] installs
// (cookie name, domain, renewal). The callbacks run first and the facade's
// settings after them, so the maximum lifetime ([Builder.SessionLifetime]) and
// the default handle, which the guard and the provider handlers of ark/oauth
// use, replace whatever the callbacks chose.
//
// Example:
//
//	identity.Session(func(cookies *session.Builder) {
//		cookies.Name("app_session")
//	})
func (identity *Builder) Session(configure ...session.BuilderFunc) *Builder {
	identity.sessionPolicy = append(identity.sessionPolicy, configure...)

	return identity
}

// SessionLifetime sets the absolute lifetime of a login once, for the session
// middleware and the guard together; they must agree or login records would
// outlive or undercut their sessions. The default is 24 hours.
//
// Example:
//
//	identity.SessionLifetime(7 * 24 * time.Hour)
func (identity *Builder) SessionLifetime(lifetime time.Duration) *Builder {
	identity.sessionLifetime = lifetime

	return identity
}

// validate checks the combinations of settings that no package can see, in
// order: the providers, the application's users and directory settings, the
// two-factor cipher, the session lifetime.
func (identity *Builder) validate() error {
	if identity.clientIP == nil {
		return errors.New("client address source is required")
	}
	if err := validateProviders(identity.providers, identity.users == nil || identity.users.Create != nil); err != nil {
		return err
	}
	if identity.users != nil {
		if identity.directorySet {
			return errors.New("Directory configures the built-in users and cannot be combined with Users")
		}
		if err := identity.users.validate(identity.hasher != nil); err != nil {
			return err
		}
	}
	if identity.twoFactor && identity.cipher == nil {
		return errors.New("TwoFactor needs a cipher")
	}
	if identity.sessionLifetime < 0 {
		return errors.New("negative session lifetime")
	}

	return nil
}
