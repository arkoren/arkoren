package access

import (
	"context"
	"errors"
	"slices"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/access/account"
	"arkoren.dev/ark/access/apitoken"
	"arkoren.dev/ark/access/federation"
	"arkoren.dev/ark/access/password"
	"arkoren.dev/ark/access/twofactor"
)

// Builder configures [New] before publication. Each feature is off until its
// method is called, and so are the two ways new users appear on their own:
// self-service registration ([password.Builder.Registration]) and sign-up with a
// provider ([federation.Builder.SignUp]). Until one is enabled the application
// adds users itself, through [Access.Directory] or its own users. Callbacks run directly once, in order, on one fresh draft;
// methods return the same draft, and retaining it cannot change a published
// [Access].
//
// Example:
//
//	scheme := totp.New("Acme") // the name authenticator apps show for this account
//	iam, err := access.New(db, store, ratelimit.TrustedProxyKey(), func(iam *access.Builder) {
//		iam.Password(hasher, func(passwords *password.Builder) {
//			passwords.Registration(true)
//		})
//		iam.TwoFactor(scheme, cipher)
//		iam.Tokens()
//		iam.Provider(google, github)
//	})
type Builder struct {
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
	providers       []Provider
	federation      []federation.BuilderFunc
	secondFactor    []auth.Credential
	stepUp          []auth.Credential
	authenticators  []auth.Authenticator
	guard           []auth.GuardBuilderFunc
	sessionPolicy   []session.BuilderFunc
	sessionLifetime time.Duration
	keepPersonal    bool
	emailProven     func(context.Context, string) error
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure access.BuilderFunc = func(iam *access.Builder) {
//		iam.Password(hasher)
//	}
//	iam, err := access.New(db, store, clientIP, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance for every package: tables are prefixed
// "<instance>_" and migration groups gain ":<instance>", so two facades can
// share one database. The name is 1 to 20 bytes of [a-z0-9_] starting with a
// letter; the default is the empty instance.
//
// Example:
//
//	iam.Instance("tenant_a")
func (iam *Builder) Instance(name string) *Builder {
	iam.instance = name

	return iam
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
//	iam.Events(events)
func (iam *Builder) Events(bus *event.Bus) *Builder {
	iam.events = bus

	return iam
}

// Users replaces the built-in users table with the application's own; see
// [Users]. Without it the facade uses the built-in directory, which identifies
// users by email. It cannot be combined with [Builder.Directory]: [New] fails
// with [ErrInvalidConfig].
//
// Example:
//
//	iam.Users(access.Users{Provider: provider, Lookup: lookup, Create: create, Rotate: rotate})
func (iam *Builder) Users(users Users) *Builder {
	iam.users = &users

	return iam
}

// Directory configures the built-in users table: email verification
// ([account.Builder.Verification]) and the lifetime of its links
// ([account.Builder.VerifyTTL]). The callbacks run after the facade's own
// settings (the instance and the events), which they should leave alone. It cannot be combined with [Builder.Users], which replaces the
// built-in table: [New] then fails with [ErrInvalidConfig].
//
// Example:
//
//	iam.Directory(func(accounts *account.Builder) {
//		accounts.Verification(signer, dispatch, deliver).VerifyTTL(time.Hour)
//	})
func (iam *Builder) Directory(configure ...account.BuilderFunc) *Builder {
	iam.directorySet = true
	iam.directory = append(iam.directory, configure...)

	return iam
}

// Password enables login, password confirmation and change, hashing with
// hasher. Registration and forgotten-password recovery are not part of it:
// enable them with [password.Builder.Registration] and [password.Builder.Reset]
// in configure (the facade then mounts the forgot and reset routes), or add
// users some other way (see [Access.Directory]). configure customizes the
// password package (the policy, the reset lifetime, registration); the facade has already set the
// instance, the events and the client address. While a user is in a second-factor lockout
// ([auth.Guard.LockedOut]), a correct password of that user is not refunded by
// the login throttle, so starting pending logins that cannot finish keeps
// costing throttle budget. The login asks the guard, so it needs no bus and
// fails closed if the lockout cannot be read.
//
// Example:
//
//	iam.Password(hasher, func(passwords *password.Builder) {
//		passwords.Registration(true).Reset(signer, dispatchReset, deliverReset)
//	})
func (iam *Builder) Password(hasher *hash.Hasher, configure ...password.BuilderFunc) *Builder {
	iam.hasher = hasher
	iam.password = slices.Clone(configure)

	return iam
}

// TwoFactor enables the TOTP authenticator and recovery codes. scheme holds the
// TOTP parameters and the issuer, the service name authenticator apps show (see
// [totp.New]); an invalid scheme, or a nil cipher, makes [New] fail with
// [ErrInvalidConfig]. cipher encrypts the secrets (use a key reserved for them).
// configure customizes the twofactor package.
//
// Example:
//
//	scheme := totp.New("Acme") // the name authenticator apps show for this account
//	iam.TwoFactor(scheme, cipher)
func (iam *Builder) TwoFactor(scheme totp.Scheme, cipher *crypto.Encrypter, configure ...twofactor.BuilderFunc) *Builder {
	iam.twoFactor = true
	iam.scheme = scheme
	iam.cipher = cipher
	iam.twofactor = slices.Clone(configure)

	return iam
}

// Tokens enables personal access tokens and device tokens. configure sets the
// scope catalog and the grant ceiling ([apitoken.Builder.Grants]), the kinds
// ([apitoken.Builder.PersonalTokens], [apitoken.Builder.DeviceTokens]), the
// prefix and the lifetime cap of the apitoken package. Without a catalog tokens
// carry no scopes.
//
// Example:
//
//	iam.Tokens(func(tokens *apitoken.Builder) {
//		tokens.Grants(apitoken.AnyScope, "posts:read", "posts:write")
//	})
func (iam *Builder) Tokens(configure ...apitoken.BuilderFunc) *Builder {
	iam.tokens = true
	iam.apitoken = slices.Clone(configure)

	return iam
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
//	iam.KeepPersonalTokens()
func (iam *Builder) KeepPersonalTokens() *Builder {
	iam.keepPersonal = true

	return iam
}

// OnEmailProven sets a function that runs when a completed password reset
// proved the mailbox of a user whose email was not verified, in the reset's
// transaction and after the built-in purge of connections, authenticator,
// recovery codes and tokens. Use it to delete what the application or a module
// registered through [Builder.SecondFactor] or [Builder.StepUp] (passkeys, say)
// may have attached in the meantime; the facade cannot know of them. An error
// rolls the reset back. It does not run when the email was already verified.
//
// Example:
//
//	iam.OnEmailProven(passkeys.RemoveAll)
func (iam *Builder) OnEmailProven(hook func(ctx context.Context, subject string) error) *Builder {
	iam.emailProven = hook

	return iam
}

// Provider adds external identity providers. Each is mounted under its
// [Provider.Name], and the facade maps their verified identities to users with
// the connection store ([federation.Links.Resolve]), so the application writes
// no resolve function. Names must be unique; [New] rejects duplicates.
//
// Example:
//
//	iam.Provider(google, github)
func (iam *Builder) Provider(providers ...Provider) *Builder {
	iam.providers = append(iam.providers, providers...)

	return iam
}

// Federation configures the connection store that [Builder.Provider] enables,
// most importantly [federation.Builder.SignUp], which lets a first login with an
// unknown identity create a user (off by default). The callbacks run after the
// facade's own settings (the instance, the events, the user creation, the
// fallback check), which they should leave alone. SignUp(true) needs users that
// can be created ([Users.Create], always true for the built-in users); without
// them [New] fails with [ErrInvalidConfig]. It has no effect without providers.
//
// Example:
//
//	iam.Federation(func(links *federation.Builder) {
//		links.SignUp(true)
//	})
func (iam *Builder) Federation(configure ...federation.BuilderFunc) *Builder {
	iam.federation = append(iam.federation, configure...)

	return iam
}

// SecondFactor registers more second-factor credentials after the built-in
// ones, for third-party modules such as a passkey package. Methods are listed
// to users in registration order.
//
// Example:
//
//	iam.SecondFactor(passkeys.Credential())
func (iam *Builder) SecondFactor(credentials ...auth.Credential) *Builder {
	iam.secondFactor = append(iam.secondFactor, credentials...)

	return iam
}

// StepUp registers more step-up credentials after the built-in ones, for
// third-party modules. Federated logins are not a step-up method by default.
//
// Example:
//
//	iam.StepUp(passkeys.Credential())
func (iam *Builder) StepUp(credentials ...auth.Credential) *Builder {
	iam.stepUp = append(iam.stepUp, credentials...)

	return iam
}

// Authenticator adds authenticators after the guard's and the token issuer's,
// for example one that verifies JWTs, to the chain of [Access.Middleware].
//
// Example:
//
//	iam.Authenticator(auth.Bearer(verifyJWT))
func (iam *Builder) Authenticator(authenticators ...auth.Authenticator) *Builder {
	iam.authenticators = append(iam.authenticators, authenticators...)

	return iam
}

// Guard customizes the [auth.Guard] the facade builds (the step-up window, the
// attempt limit, the login limit). The callbacks run first and the facade's own
// settings after them, so the facade wins: the session lifetime
// ([Builder.SessionLifetime]), the session handle (always the default one,
// which the provider handlers of ark/oauth use), the event bus and the
// client address source given to [New] replace whatever the callbacks chose.
// The credential registries only append: register second-factor and step-up
// credentials with [Builder.SecondFactor] and [Builder.StepUp], not here. A
// credential registered here would be listed to users before the built-in ones,
// and a method the facade also registers makes [New] fail with
// [ErrInvalidConfig] as a duplicate.
//
// Example:
//
//	iam.Guard(func(policy *auth.GuardBuilder) {
//		policy.FreshFor(5 * time.Minute)
//	})
func (iam *Builder) Guard(configure ...auth.GuardBuilderFunc) *Builder {
	iam.guard = append(iam.guard, configure...)

	return iam
}

// Session customizes the session middleware that [Access.Middleware] installs
// (cookie name, domain, renewal). The callbacks run first and the facade's
// settings after them, so the maximum lifetime ([Builder.SessionLifetime]) and
// the default handle, which the guard and the provider handlers of ark/oauth
// use, replace whatever the callbacks chose.
//
// Example:
//
//	iam.Session(func(cookies *session.Builder) {
//		cookies.Name("app_session")
//	})
func (iam *Builder) Session(configure ...session.BuilderFunc) *Builder {
	iam.sessionPolicy = append(iam.sessionPolicy, configure...)

	return iam
}

// SessionLifetime sets the absolute lifetime of a login once, for the session
// middleware and the guard together; they must agree or login records would
// outlive or undercut their sessions. The default is 24 hours.
//
// Example:
//
//	iam.SessionLifetime(7 * 24 * time.Hour)
func (iam *Builder) SessionLifetime(lifetime time.Duration) *Builder {
	iam.sessionLifetime = lifetime

	return iam
}

// validate checks the combinations of settings that no package can see, in
// order: the providers, the application's users and directory settings, the
// two-factor cipher, the session lifetime.
func (iam *Builder) validate() error {
	if err := validateProviders(iam.providers); err != nil {
		return err
	}
	if iam.users != nil {
		if iam.directorySet {
			return errors.New("Directory configures the built-in users and cannot be combined with Users")
		}
		if err := iam.users.validate(iam.hasher != nil); err != nil {
			return err
		}
	}
	if iam.twoFactor && iam.cipher == nil {
		return errors.New("TwoFactor needs a cipher")
	}
	if iam.sessionLifetime < 0 {
		return errors.New("negative session lifetime")
	}

	return nil
}
