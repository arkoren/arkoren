package iam

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

	"arkoren.dev/ark/iam/account"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/federation"
	"arkoren.dev/ark/iam/password"
	"arkoren.dev/ark/iam/permission"
	"arkoren.dev/ark/iam/twofactor"
)

// build constructs the packages and the guard from validated settings: users,
// passwords, two-factor, permissions, tokens, connections. The permission store
// comes before the tokens because their ceiling reads it. Migrations are added
// in the order their tables depend on each other, with the permissions last.
// Hooks that need a package built later read it when they run, not here.
func (identity *IAM) build(settings *Builder) error {
	users, err := identity.buildUsers(settings)
	if err != nil {
		return err
	}
	identity.provider, identity.verified = users.Provider, users.Verified
	if err := identity.buildPassword(settings, users); err != nil {
		return err
	}
	if err := identity.buildTwoFactor(settings); err != nil {
		return err
	}
	if err := identity.buildPermissions(settings); err != nil {
		return err
	}
	if err := identity.buildTokens(settings); err != nil {
		return err
	}
	if err := identity.buildLinks(settings, users); err != nil {
		return err
	}
	if identity.permissions != nil {
		identity.add(identity.permissions.Migrations(), identity.permissions.Ping)
	}

	return identity.buildGuard(settings)
}

// add records the migrations and readiness check of an enabled package.
func (identity *IAM) add(migrations []migrate.Definition, ping func(context.Context) error) {
	identity.migrations = append(identity.migrations, migrations...)
	identity.pingers = append(identity.pingers, ping)
}

// buildUsers returns the application's [Users], or builds the built-in
// directory and presents it as [Users].
func (identity *IAM) buildUsers(settings *Builder) (Users, error) {
	if settings.users != nil {
		return *settings.users, nil
	}
	configure := append([]account.BuilderFunc{func(accounts *account.Builder) {
		accounts.Instance(settings.instance).Events(settings.events)
	}}, settings.directory...)
	directory, err := account.NewDirectory(identity.db, identity.store, identity.clientIP, configure...)
	if err != nil {
		return Users{}, err
	}
	identity.directory = directory
	identity.add(directory.Migrations(), directory.Ping)

	return directoryUsers(directory), nil
}

// buildPassword builds the password verifier when [Builder.Password] is enabled.
func (identity *IAM) buildPassword(settings *Builder, users Users) error {
	if !settings.passwordOn {
		return nil
	}
	configure := append([]password.BuilderFunc{identity.passwordPolicy(settings)}, settings.password...)
	verifier, err := password.NewVerifier(identity.provider, identity.db, identity.store, identity.clientIP, settings.hasher, identity.passwordUsers(users), configure...)
	if err != nil {
		return err
	}
	if identity.directory != nil && verifier.Registration() && !verifier.ResetEnabled() {
		return errors.New("registration with the built-in users needs password.Builder.Reset: it is how the owner of an address that someone else registered learns of it and takes the account back")
	}
	identity.verifier = verifier
	identity.add(verifier.Migrations(), verifier.Ping)

	return nil
}

// passwordUsers adapts users to the password package: registration creates
// unverified users, a taken address is [password.ErrIdentifierTaken] (the
// directory's [account.ErrEmailTaken] is mapped), and a reset proves the email.
func (identity *IAM) passwordUsers(users Users) password.Users {
	adapted := password.Users{Lookup: users.Lookup, Rotate: users.Rotate, ProveEmail: identity.proveEmail(users.ProveEmail)}
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
// application's, whose hooks add to them instead of replacing them. With the built-in users, registration asks for an email
// through two hooks that read the directory and the verifier when they run: a
// new user gets a verification, and the owner of a taken address a notice
// ([IAM.tellOwner]).
func (identity *IAM) passwordPolicy(settings *Builder) password.BuilderFunc {
	return func(passwords *password.Builder) {
		passwords.Instance(settings.instance).Events(settings.events).AfterChange(identity.revokeTokens)
		if identity.directory != nil {
			passwords.Registered(identity.verifyNew).Taken(identity.tellOwner)
		}
	}
}

// buildTwoFactor builds the two-factor store when [Builder.TwoFactor] is enabled.
func (identity *IAM) buildTwoFactor(settings *Builder) error {
	if !settings.twoFactor {
		return nil
	}
	configure := append([]twofactor.BuilderFunc{func(factors *twofactor.Builder) {
		factors.Instance(settings.instance).Events(settings.events)
		factors.Verified(identity.verified).Methods(settings.secondFactor...).OnEnable(identity.revokeTokens)
	}}, settings.twofactor...)
	factors, err := twofactor.NewFactors(identity.provider, identity.db, identity.store, settings.scheme, settings.cipher, configure...)
	if err != nil {
		return err
	}
	identity.factors = factors
	identity.add(factors.Migrations(), factors.Ping)

	return nil
}

// buildPermissions builds the permission store when [Builder.Permissions] is
// enabled. Its migrations are added by build, after the connections'.
func (identity *IAM) buildPermissions(settings *Builder) error {
	if !settings.permissions {
		return nil
	}
	configure := append([]permission.BuilderFunc{func(policy *permission.Builder) {
		policy.Instance(settings.instance).Events(settings.events)
	}}, settings.permission...)
	store, err := permission.NewStore(identity.provider, identity.db, identity.store, configure...)
	if err != nil {
		return err
	}
	identity.permissions = store

	return nil
}

// buildTokens builds the token issuer when [Builder.Tokens] is enabled. With the
// permission store, the ceiling is the user's permissions, set before the
// application's callbacks so an explicit Grants replaces it.
func (identity *IAM) buildTokens(settings *Builder) error {
	if !settings.tokens {
		return nil
	}
	configure := []apitoken.BuilderFunc{func(tokens *apitoken.Builder) {
		tokens.Instance(settings.instance).Events(settings.events).Verified(identity.verified)
	}}
	if store := identity.permissions; store != nil {
		configure = append(configure, func(tokens *apitoken.Builder) {
			tokens.Grants(store.Permissions, store.Catalog()...)
		})
	}
	configure = append(configure, settings.apitoken...)
	issuer, err := apitoken.NewIssuer(identity.provider, identity.db, identity.store, configure...)
	if err != nil {
		return err
	}
	identity.issuer = issuer
	identity.add(issuer.Migrations(), issuer.Ping)

	return nil
}

// buildLinks builds the connection store when [Builder.Provider] added any
// provider, and one resolver per provider over it, each with the provider's own
// sign-up settings. The hooks of the packages add up (see
// [federation.Builder.TakeOver] and the other setters): the facade's settings
// are the store's, and a provider's [ProviderBuilder.Create] runs before the
// facade's own creation, so an application function can refuse or handle an
// identity first.
func (identity *IAM) buildLinks(settings *Builder, users Users) error {
	if len(settings.providers) == 0 {
		return nil
	}
	links, err := federation.NewLinks(identity.provider, identity.db, func(links *federation.Builder) {
		links.Instance(settings.instance).Events(settings.events).Verified(identity.verified)
		if identity.verifier != nil {
			links.Fallback(identity.verifier.Has)
		}
		if identity.directory != nil {
			links.TakeOver(identity.takeOver(users))
		}
		links.Create(createExternal(users.Create))
	})
	if err != nil {
		return err
	}
	for _, setup := range settings.providers {
		resolve, err := links.Resolver(func(resolver *federation.ResolverBuilder) {
			resolver.SignUp(setup.settings.signUp).Create(setup.settings.create)
		})
		if err != nil {
			return err
		}
		identity.providers = append(identity.providers, mountedProvider{provider: setup.provider, resolve: resolve})
	}
	identity.links = links
	identity.add(links.Migrations(), links.Ping)

	return nil
}

// buildGuard builds the guard with the credential registries, and the
// middleware chain around it. The application's [Builder.Guard] callbacks run
// first and the facade's own settings last, so what the facade owns (the
// session lifetime, the default session handle, the credential registries, the
// client address and, with [Builder.Permissions], the grants loader) cannot be
// overridden. Registries only append, so
// credentials a callback registers directly stay in front of the facade's; see
// [Builder.Guard]. The event bus of [Builder.Events] is set before the
// callbacks and is not re-asserted: the facade does not depend on it, so a
// callback may replace it.
func (identity *IAM) buildGuard(settings *Builder) error {
	secondFactor, stepUp := identity.credentials(settings)
	identity.secondFactor, identity.stepUp = len(secondFactor) > 0, len(stepUp) > 0
	lifetime := cmp.Or(settings.sessionLifetime, session.DefaultMaxLifetime)
	configure := append(slices.Clone(settings.guard), func(policy *auth.GuardBuilder) {
		policy.Handle(session.Handle{}).SessionLifetime(lifetime)
		policy.SecondFactor(secondFactor...).StepUp(stepUp...).ClientIP(identity.clientIP)
		if identity.permissions != nil {
			policy.Grants(identity.permissions.Load)
		}
	})
	// The bus goes first: nothing in the facade depends on it, so an application's
	// Guard callback may replace it.
	configure = append([]auth.GuardBuilderFunc{func(policy *auth.GuardBuilder) { policy.Events(settings.events) }}, configure...)
	guard, err := auth.NewGuard(identity.provider, identity.store, configure...)
	if err != nil {
		return err
	}
	identity.guard = guard
	identity.middleware = identity.chain(settings, lifetime)

	return nil
}

// credentials returns the second-factor and step-up registries: the built-in
// methods of the enabled packages (second factor: totp then recovery; step up:
// password then totp), then those of [Builder.SecondFactor] and
// [Builder.StepUp].
func (identity *IAM) credentials(settings *Builder) (secondFactor, stepUp []auth.Credential) {
	if identity.verifier != nil {
		stepUp = append(stepUp, identity.verifier.Credential())
	}
	if identity.factors != nil {
		secondFactor = append(secondFactor, identity.factors.Credential(), identity.factors.RecoveryCredential())
		stepUp = append(stepUp, identity.factors.Credential())
	}

	return append(secondFactor, settings.secondFactor...), append(stepUp, settings.stepUp...)
}

// chain returns the session middleware followed by the authenticators: the
// guard's, the token issuer's when enabled, then [Builder.Authenticator]'s. The
// application's [Builder.Session] callbacks run first and the facade's
// lifetime and default handle last, so they cannot be overridden.
func (identity *IAM) chain(settings *Builder, lifetime time.Duration) router.Middleware {
	authenticators := []auth.Authenticator{identity.guard.Authenticator()}
	if identity.issuer != nil {
		authenticators = append(authenticators, identity.issuer.Authenticator())
	}
	sessions := session.Middleware(identity.store, append(slices.Clone(settings.sessionPolicy), func(cookies *session.Builder) {
		cookies.MaxLifetime(lifetime).Handle(session.Handle{})
	})...)
	authenticate := auth.Middleware(append(authenticators, settings.authenticators...)...)

	return func(next router.Handler) router.Handler {
		return sessions(authenticate(next))
	}
}

// createExternal returns the function that makes a user for an unknown external
// identity with the provider's email. Sign-up refuses an identity without a
// proven email before it gets here, so the user is created verified; the flag
// still follows [auth.External.EmailVerified], because the application's own
// [Users.Create] receives it. It is nil without [Users.Create], which makes
// [ProviderBuilder.SignUp] fail construction. Whether it is used at all is
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
func (identity *IAM) revokeTokens(ctx context.Context, subject string) error {
	if identity.issuer == nil {
		return nil
	}
	if identity.keepPersonal {
		return identity.issuer.RevokeDevices(ctx, subject)
	}

	return identity.issuer.RevokeAll(ctx, subject)
}

// verifyNew asks for the verification email of a user who just registered. It
// does nothing without [account.Builder.Verification].
func (identity *IAM) verifyNew(ctx context.Context, subject, _ string) error {
	if !identity.directory.VerificationEnabled() {
		return nil
	}

	return identity.directory.RequestVerification(ctx, subject)
}

// tellOwner is the work of a registration that meets a taken address, chosen so
// that it costs what the work for a new address costs ([IAM.verifyNew]):
// without [account.Builder.Verification] nothing is dispatched on either path.
// With it, the owner gets a reset notice ([password.Builder.Reset], which
// registration with the built-in users requires), whose link also proves the
// mailbox. An unknown identifier is no owner.
func (identity *IAM) tellOwner(ctx context.Context, identifier string) error {
	if !identity.directory.VerificationEnabled() {
		return nil
	}

	return identity.verifier.RequestReset(ctx, password.ResetRequest{Identifier: identifier, Notice: true})
}
