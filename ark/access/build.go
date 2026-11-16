package access

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/database/migrate"

	"arkoren.dev/ark/access/account"
	"arkoren.dev/ark/access/apitoken"
	"arkoren.dev/ark/access/federation"
	"arkoren.dev/ark/access/password"
	"arkoren.dev/ark/access/twofactor"
)

// build constructs the packages and the guard from validated settings, in the
// order their tables depend on each other: users, passwords, two-factor,
// tokens, connections. Hooks that need a package built later read it when they
// run, not here.
func (iam *Access) build(settings *Builder) error {
	users, err := iam.buildUsers(settings)
	if err != nil {
		return err
	}
	iam.provider, iam.verified = users.Provider, users.Verified
	if err := iam.buildPassword(settings, users); err != nil {
		return err
	}
	if err := iam.buildTwoFactor(settings); err != nil {
		return err
	}
	if err := iam.buildTokens(settings); err != nil {
		return err
	}
	if err := iam.buildLinks(settings, users); err != nil {
		return err
	}

	return iam.buildGuard(settings)
}

// add records the migrations and readiness check of an enabled package.
func (iam *Access) add(migrations []migrate.Definition, ping func(context.Context) error) {
	iam.migrations = append(iam.migrations, migrations...)
	iam.pingers = append(iam.pingers, ping)
}

// buildUsers returns the application's [Users], or builds the built-in
// directory and presents it as [Users].
func (iam *Access) buildUsers(settings *Builder) (Users, error) {
	if settings.users != nil {
		return *settings.users, nil
	}
	configure := append([]account.BuilderFunc{func(accounts *account.Builder) {
		accounts.Instance(settings.instance).Events(settings.events)
		if iam.deliver != nil {
			accounts.Deliver(iam.deliverVerification)
		}
	}}, settings.directory...)
	directory, err := account.NewDirectory(iam.db, iam.store, iam.clientIP, configure...)
	if err != nil {
		return Users{}, err
	}
	iam.directory = directory
	iam.add(directory.Migrations(), directory.Ping)

	return directoryUsers(directory), nil
}

// buildPassword builds the password verifier when [Builder.Password] is set.
func (iam *Access) buildPassword(settings *Builder, users Users) error {
	if settings.hasher == nil {
		return nil
	}
	configure := append([]password.BuilderFunc{iam.passwordPolicy(settings)}, settings.password...)
	verifier, err := password.NewVerifier(iam.provider, iam.db, iam.store, iam.clientIP, settings.hasher, iam.passwordUsers(users), configure...)
	if err != nil {
		return err
	}
	iam.verifier = verifier
	iam.add(verifier.Migrations(), verifier.Ping)

	return nil
}

// passwordUsers adapts users to the password package: registration creates
// unverified users, a taken address is [password.ErrIdentifierTaken] (the
// directory's [account.ErrEmailTaken] is mapped), and a reset proves the email.
func (iam *Access) passwordUsers(users Users) password.Users {
	adapted := password.Users{Lookup: users.Lookup, Rotate: users.Rotate, ProveEmail: iam.proveEmail(users.ProveEmail)}
	if users.Create != nil {
		adapted.Create = func(ctx context.Context, identifier string) (string, error) {
			subject, err := users.Create(ctx, identifier, false)
			if errors.Is(err, account.ErrEmailTaken) {
				return "", password.ErrIdentifierTaken
			}

			return subject, err
		}
	}

	return adapted
}

// passwordPolicy is the facade's own password settings, applied before the
// application's.
func (iam *Access) passwordPolicy(settings *Builder) password.BuilderFunc {
	return func(passwords *password.Builder) {
		passwords.Instance(settings.instance).Events(settings.events).AfterChange(iam.revokeTokens)
		if iam.deliver == nil {
			return
		}
		passwords.Deliver(iam.deliverReset)
		if iam.directory != nil {
			passwords.Registered(iam.sendVerification)
		}
	}
}

// buildTwoFactor builds the two-factor store when [Builder.TwoFactor] is set.
func (iam *Access) buildTwoFactor(settings *Builder) error {
	if !settings.twoFactor {
		return nil
	}
	configure := append([]twofactor.BuilderFunc{func(factors *twofactor.Builder) {
		factors.Instance(settings.instance).Events(settings.events)
		factors.Verified(iam.verified).Methods(settings.secondFactor...).OnEnable(iam.revokeTokens)
	}}, settings.twofactor...)
	factors, err := twofactor.NewFactors(iam.provider, iam.db, iam.store, settings.scheme, settings.cipher, configure...)
	if err != nil {
		return err
	}
	iam.factors = factors
	iam.add(factors.Migrations(), factors.Ping)

	return nil
}

// buildTokens builds the token issuer when [Builder.Tokens] is set.
func (iam *Access) buildTokens(settings *Builder) error {
	if !settings.tokens {
		return nil
	}
	configure := append([]apitoken.BuilderFunc{func(tokens *apitoken.Builder) {
		tokens.Instance(settings.instance).Events(settings.events).Verified(iam.verified)
	}}, settings.apitoken...)
	issuer, err := apitoken.NewIssuer(iam.provider, iam.db, iam.store, configure...)
	if err != nil {
		return err
	}
	iam.issuer = issuer
	iam.add(issuer.Migrations(), issuer.Ping)

	return nil
}

// buildLinks builds the connection store when [Builder.Provider] added any
// provider.
func (iam *Access) buildLinks(settings *Builder, users Users) error {
	if len(iam.providers) == 0 {
		return nil
	}
	configure := append([]federation.BuilderFunc{func(links *federation.Builder) {
		links.Instance(settings.instance).Events(settings.events).Verified(iam.verified)
		links.Create(createExternal(users.Create))
		if iam.verifier != nil {
			links.Fallback(iam.verifier.Has)
		}
	}}, settings.federation...)
	links, err := federation.NewLinks(iam.provider, iam.db, configure...)
	if err != nil {
		return err
	}
	iam.links = links
	iam.add(links.Migrations(), links.Ping)

	return nil
}

// buildGuard builds the guard with the credential registries, and the
// middleware chain around it. The application's [Builder.Guard] callbacks run
// first and the facade's own settings last, so what the facade owns (the
// session lifetime, the default session handle, the credential registries and
// the client address) cannot be overridden. Registries only append, so
// credentials a callback registers directly stay in front of the facade's; see
// [Builder.Guard]. The event bus of [Builder.Events] is set before the
// callbacks and is not re-asserted: the facade does not depend on it, so a
// callback may replace it.
func (iam *Access) buildGuard(settings *Builder) error {
	secondFactor, stepUp := iam.credentials(settings)
	iam.secondFactor, iam.stepUp = len(secondFactor) > 0, len(stepUp) > 0
	lifetime := cmp.Or(settings.sessionLifetime, session.DefaultMaxLifetime)
	configure := append(slices.Clone(settings.guard), func(policy *auth.GuardBuilder) {
		policy.Handle(session.Handle{}).SessionLifetime(lifetime)
		policy.SecondFactor(secondFactor...).StepUp(stepUp...).ClientIP(iam.clientIP)
	})
	// The bus goes first: nothing in the facade depends on it, so an application's
	// Guard callback may replace it.
	configure = append([]auth.GuardBuilderFunc{func(policy *auth.GuardBuilder) { policy.Events(settings.events) }}, configure...)
	guard, err := auth.NewGuard(iam.provider, iam.store, configure...)
	if err != nil {
		return err
	}
	iam.guard = guard
	iam.middleware = iam.chain(settings, lifetime)

	return nil
}

// credentials returns the second-factor and step-up registries: the built-in
// methods of the enabled packages (second factor: totp then recovery; step up:
// password then totp), then those of [Builder.SecondFactor] and
// [Builder.StepUp].
func (iam *Access) credentials(settings *Builder) (secondFactor, stepUp []auth.Credential) {
	if iam.verifier != nil {
		stepUp = append(stepUp, iam.verifier.Credential())
	}
	if iam.factors != nil {
		secondFactor = append(secondFactor, iam.factors.Credential(), iam.factors.RecoveryCredential())
		stepUp = append(stepUp, iam.factors.Credential())
	}

	return append(secondFactor, settings.secondFactor...), append(stepUp, settings.stepUp...)
}

// chain returns the session middleware followed by the authenticators: the
// guard's, the token issuer's when enabled, then [Builder.Authenticator]'s. The
// application's [Builder.Session] callbacks run first and the facade's
// lifetime and default handle last, so they cannot be overridden.
func (iam *Access) chain(settings *Builder, lifetime time.Duration) router.Middleware {
	authenticators := []auth.Authenticator{iam.guard.Authenticator()}
	if iam.issuer != nil {
		authenticators = append(authenticators, iam.issuer.Authenticator())
	}
	sessions := session.Middleware(iam.store, append(slices.Clone(settings.sessionPolicy), func(cookies *session.Builder) {
		cookies.MaxLifetime(lifetime).Handle(session.Handle{})
	})...)
	authenticate := auth.Middleware(append(authenticators, settings.authenticators...)...)

	return func(next router.Handler) router.Handler {
		return sessions(authenticate(next))
	}
}

// createExternal returns the function that makes a user for an unknown external
// identity with the provider's email, verified according to
// [auth.External.EmailVerified]. It is nil without [Users.Create], which makes
// [federation.Builder.SignUp] fail construction. Whether it is used at all is
// SignUp's decision.
func createExternal(create func(context.Context, string, bool) (string, error)) func(context.Context, auth.External) (string, error) {
	if create == nil {
		return nil
	}

	return func(ctx context.Context, external auth.External) (string, error) {
		return create(ctx, external.Email, external.EmailVerified)
	}
}

// revokeTokens ends the user's token access after a credential change, a
// password change or enabling two-factor: every personal and device token, or
// only the device tokens with [Builder.KeepPersonalTokens]. Whoever just proved
// the old credential, or the stolen password, may have minted a personal token
// that would otherwise outlive the change. The issuer is built after the
// password and two-factor packages, so it is read when the hook runs.
func (iam *Access) revokeTokens(ctx context.Context, subject string) error {
	if iam.issuer == nil {
		return nil
	}
	if iam.keepPersonal {
		return iam.issuer.RevokeDevices(ctx, subject)
	}

	return iam.issuer.RevokeAll(ctx, subject)
}

// deliverVerification hands a verification email to [Builder.Deliver].
func (iam *Access) deliverVerification(ctx context.Context, message account.Message) error {
	return iam.deliver(ctx, Message{Kind: KindVerify, Identifier: message.Email, Token: message.Token, ExpiresAt: message.ExpiresAt})
}

// deliverReset hands a reset email, or the notice of a registration attempt on
// a taken address, to [Builder.Deliver].
func (iam *Access) deliverReset(ctx context.Context, message password.Message) error {
	kind := KindReset
	if message.Registration {
		kind = KindRegistration
	}

	return iam.deliver(ctx, Message{Kind: kind, Identifier: message.Identifier, Token: message.Token, ExpiresAt: message.ExpiresAt})
}

// sendVerification sends the verification email of a user who just registered.
func (iam *Access) sendVerification(ctx context.Context, subject, _ string) error {
	return iam.directory.SendVerification(ctx, subject)
}
