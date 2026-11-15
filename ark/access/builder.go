package access

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

	"arkoren.dev/ark/access/apitoken"
	"arkoren.dev/ark/access/password"
	"arkoren.dev/ark/access/twofactor"
)

// Builder configures [New] before publication. Each feature is off until its
// method is called. Callbacks run directly once, in order, on one fresh draft;
// methods return the same draft, and retaining it cannot change a published
// [Access].
//
// Example:
//
//	iam, err := access.New(db, store, func(iam *access.Builder) {
//		iam.Password(hasher)
//		iam.TwoFactor(cipher, "Example")
//		iam.Tokens()
//		iam.Provider(google, github)
//		iam.Deliver(send)
//	})
type Builder struct {
	instance        string
	events          *event.Bus
	users           *Users
	hasher          *hash.Hasher
	password        []password.BuilderFunc
	cipher          *crypto.Encrypter
	issuer          string
	twofactor       []twofactor.BuilderFunc
	tokens          bool
	apitoken        []apitoken.BuilderFunc
	providers       []Provider
	deliver         func(context.Context, Message) error
	secondFactor    []auth.Credential
	stepUp          []auth.Credential
	authenticators  []auth.Authenticator
	guard           []auth.GuardBuilderFunc
	sessionPolicy   []session.BuilderFunc
	sessionLifetime time.Duration
	clientIP        func(*http.Request) string
	revokePersonal  bool
	emailProven     func(context.Context, string) error
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure access.BuilderFunc = func(iam *access.Builder) {
//		iam.Password(hasher)
//	}
//	iam, err := access.New(db, store, configure)
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
// email addresses. A nil bus publishes nothing.
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
// users by email.
//
// Example:
//
//	iam.Users(access.Users{Provider: provider, Lookup: lookup, Create: create, Rotate: rotate})
func (iam *Builder) Users(users Users) *Builder {
	iam.users = &users

	return iam
}

// Password enables registration, login, password confirmation, change, forgot
// and reset, hashing with hasher. configure customizes the password package (the
// policy, the reset lifetime); the facade has already set the instance, the
// events and the client address.
//
// Example:
//
//	iam.Password(hasher)
func (iam *Builder) Password(hasher *hash.Hasher, configure ...password.BuilderFunc) *Builder {
	iam.hasher = hasher
	iam.password = slices.Clone(configure)

	return iam
}

// TwoFactor enables the TOTP authenticator and recovery codes. cipher encrypts
// the secrets (use a key reserved for them) and issuer is the service name
// authenticator apps show. configure customizes the twofactor package.
//
// Example:
//
//	iam.TwoFactor(cipher, "Example")
func (iam *Builder) TwoFactor(cipher *crypto.Encrypter, issuer string, configure ...twofactor.BuilderFunc) *Builder {
	iam.cipher = cipher
	iam.issuer = issuer
	iam.twofactor = slices.Clone(configure)

	return iam
}

// Tokens enables personal access tokens and device tokens. configure sets the
// scope catalog, the prefix and the lifetime cap of the apitoken package.
//
// Example:
//
//	iam.Tokens(func(tokens *apitoken.Builder) {
//		tokens.Scopes("posts:read", "posts:write")
//	})
func (iam *Builder) Tokens(configure ...apitoken.BuilderFunc) *Builder {
	iam.tokens = true
	iam.apitoken = slices.Clone(configure)

	return iam
}

// RevokeTokensOnPasswordChange makes a password change also revoke the user's
// personal access tokens. By default a change ends the user's other sessions and
// their device tokens, but keeps personal tokens, which the user created on
// purpose for scripts and integrations. Turn this on when a changed password
// should cut off everything. It has no effect without [Builder.Tokens] and
// [Builder.Password]; a password reset always revokes every token.
//
// Example:
//
//	iam.RevokeTokensOnPasswordChange()
func (iam *Builder) RevokeTokensOnPasswordChange() *Builder {
	iam.revokePersonal = true

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

// Deliver sets the function that sends the verification and reset links.
// Without it, the forgot-password and email-verification handlers panic at
// composition, because a user who asks for a link must get one.
//
// Example:
//
//	iam.Deliver(func(ctx context.Context, message access.Message) error {
//		return mailer.Send(ctx, message.Identifier, message.Link("https://app.example.com/verify"))
//	})
func (iam *Builder) Deliver(deliver func(ctx context.Context, message Message) error) *Builder {
	iam.deliver = deliver

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
// attempt limit, the login limit). Do not set the credential registries
// (use [Builder.SecondFactor] and [Builder.StepUp]), the session lifetime (use
// [Builder.SessionLifetime]) or a session handle: the provider handlers of
// ark/oauth use the default handle.
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
// (cookie name, domain, renewal). Do not set MaxLifetime (use
// [Builder.SessionLifetime]) or a handle: the provider handlers of ark/oauth
// use the default handle.
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

// ClientIP sets how the client address is found, for the guard's login records
// and the password login throttle. The default is the host of r.RemoteAddr,
// which behind a reverse proxy is the proxy; pass ratelimit.TrustedProxyKey
// with your proxies' prefixes.
//
// Example:
//
//	iam.ClientIP(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
func (iam *Builder) ClientIP(find func(*http.Request) string) *Builder {
	iam.clientIP = find

	return iam
}

// validate checks the combinations of settings that no package can see, in
// order: the providers, the application's users, the session lifetime.
func (iam *Builder) validate() error {
	if err := validateProviders(iam.providers); err != nil {
		return err
	}
	if iam.users != nil {
		if err := iam.users.validate(iam.hasher != nil); err != nil {
			return err
		}
	}
	if iam.sessionLifetime < 0 {
		return errors.New("negative session lifetime")
	}

	return nil
}
