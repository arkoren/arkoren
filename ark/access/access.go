package access

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/core/service/hash"

	"arkoren.dev/ark/access/account"
	"arkoren.dev/ark/access/apitoken"
	"arkoren.dev/ark/access/federation"
	"arkoren.dev/ark/access/internal/storage"
	"arkoren.dev/ark/access/password"
	"arkoren.dev/ark/access/twofactor"
)

// ErrInvalidConfig is wrapped by the errors of [New] for an unusable
// configuration: a missing dependency, a duplicate provider name, an invalid
// instance or a failing package constructor.
//
// Example:
//
//	_, err := access.New(nil, nil)
//	fmt.Println(errors.Is(err, access.ErrInvalidConfig)) // true
var ErrInvalidConfig = errors.New("access: invalid configuration")

// ErrNotMigrated is returned by [Access.Ping] when the tables of an enabled
// feature do not exist. Add [Access.Migrations] to the application's migrator
// and run them.
//
// Example:
//
//	if errors.Is(iam.Ping(ctx), access.ErrNotMigrated) {
//		// run the migrator
//	}
var ErrNotMigrated = storage.ErrNotMigrated

// Kind says which flow a [Message] belongs to.
//
// Example:
//
//	if message.Kind == access.KindReset {
//		// write the reset email
//	}
type Kind string

const (
	// KindVerify is the email verification of a new account.
	//
	// Example:
	//
	//	if message.Kind == access.KindVerify {
	//		// link to the verification page
	//	}
	KindVerify Kind = "verify"

	// KindReset is a password reset.
	//
	// Example:
	//
	//	if message.Kind == access.KindReset {
	//		// link to the reset page
	//	}
	KindReset Kind = "reset"
)

// Message is what the function given to [Builder.Deliver] receives: the secret
// link of a verification or reset email. The token is a credential until it is
// used or expires; never log it. Everything else the application wants to say
// about security (a changed password, disabled two-factor) is an event on
// [Builder.Events], which never carries a token.
//
// Example:
//
//	iam.Deliver(func(ctx context.Context, message access.Message) error {
//		base := "https://app.example.com/verify-email"
//		if message.Kind == access.KindReset {
//			base = "https://app.example.com/reset-password"
//		}
//		return mailer.Send(ctx, message.Identifier, message.Link(base))
//	})
type Message struct {
	// Kind says which flow sent the message.
	Kind Kind

	// Identifier is who to write to: the email stored with the user with the
	// built-in directory, or the address that the application's [Users.Lookup]
	// returned. It is never the text of a request, so a request cannot send a
	// victim's link elsewhere.
	Identifier string

	// Token is the single-use secret to put in the link.
	Token string

	// ExpiresAt is when the token stops working.
	ExpiresAt time.Time
}

// Link returns base with the token as its "token" query parameter, escaped.
//
// Example:
//
//	message := access.Message{Token: "abc"}
//	fmt.Println(message.Link("https://app.example.com/verify")) // https://app.example.com/verify?token=abc
func (message Message) Link(base string) string {
	return base + "?" + url.Values{"token": {message.Token}}.Encode()
}

// Provider is a browser login with an external identity provider, such as the
// *oauthauth.Client of arkoren.dev/ark/oauth, which satisfies it structurally:
// this module never imports ark/oauth.
//
// Example:
//
//	iam.Provider(google, github) // *oauthauth.Client values
type Provider interface {
	// Name is the provider's name in mounted paths: "/auth/<name>" starts the
	// login and "/auth/<name>/callback" finishes it. Names must be unique.
	Name() string

	// LoginHandler starts the login by redirecting to the provider.
	LoginHandler() router.Handler

	// CallbackHandler finishes the login: it verifies the provider's answer,
	// maps it to a subject with resolve and begins the login on guard.
	CallbackHandler(guard *auth.Guard, resolve auth.Resolver) router.Handler
}

// NativeProvider is the extra capability of a [Provider] that also accepts an ID
// token from a mobile app's own provider SDK (Google and Apple in ark/oauth). A
// provider that implements it also gets "/auth/<name>/nonce" and
// "/auth/<name>/native".
//
// Example:
//
//	var _ access.NativeProvider = (*oauthauth.Client)(nil)
type NativeProvider interface {
	// NonceHandler issues the nonce the app puts in its ID token request.
	NonceHandler() router.Handler

	// NativeHandler verifies the app's ID token, maps it to a subject with resolve
	// and begins the login on guard.
	NativeHandler(guard *auth.Guard, resolve auth.Resolver) router.Handler
}

// Users replaces the built-in users table with the application's own. The
// facade then migrates no users table, never creates or deletes users itself,
// and talks to the application's users only through these functions.
//
// Example:
//
//	iam.Users(access.Users{
//		Provider: provider,
//		Lookup:   func(ctx context.Context, email string) (string, string, error) { return store.SubjectAndEmail(ctx, email) },
//		Create:   func(ctx context.Context, email string, verified bool) (string, error) { return store.Create(ctx, email, verified) },
//		Rotate:   func(ctx context.Context, subject string) error { return store.BumpVersion(ctx, subject) },
//	})
//
// Email verification is the application's with its own users. The built-in
// directory refuses unverified users the actions an earlier holder of the
// address could abuse (connecting a provider, enrolling two-factor, creating
// tokens) and, when a password reset proves the mailbox, removes what such a
// holder attached. With your own users the facade can do the same only through
// [Users.Verified] and [Users.ProveEmail]; without them it does neither, and
// the application must enforce the rule itself.
type Users struct {
	// Provider serves the application's users to the guard. Its driver's
	// UserRecord.Version must change when Rotate runs.
	Provider *auth.UserProvider

	// Lookup returns the subject of the user with identifier, whatever the
	// application uses to log in (an email, a username), and the address to
	// send mail to, as stored with the user and never the typed identifier. An
	// unknown user must return an error wrapping [auth.ErrUserNotFound]. It is
	// required with [Builder.Password].
	Lookup func(ctx context.Context, identifier string) (subject, address string, err error)

	// Create adds a user and returns the subject. It serves registration (with
	// verified false) and the first login with an external identity (the
	// identifier is then the provider's email, possibly empty, and verified is
	// [auth.External.EmailVerified]); its errors pass through to the client.
	// It must fail on an existing identifier and never return the existing
	// user: connecting an identity to someone by email is how accounts get
	// taken over. It may be nil when users are created elsewhere.
	Create func(ctx context.Context, identifier string, verified bool) (subject string, err error)

	// Rotate bumps [auth.UserRecord.Version], which ends every session, pending
	// login and API token of the user. A password reset calls it. It is required
	// with [Builder.Password].
	Rotate func(ctx context.Context, subject string) error

	// Verified is optional. It reports whether the user's email is verified. The
	// facade then answers 403 [account.ErrEmailUnverified] to unverified users on
	// the routes that connect a provider, enroll two-factor or create tokens.
	// Without it those routes are open to unverified users.
	Verified func(ctx context.Context, subject string) (bool, error)

	// ProveEmail is optional. A completed password reset calls it, in the
	// reset's transaction, because the link reached the mailbox. It marks the
	// email verified and reports whether it was unverified before; if so the
	// facade deletes the user's connections, two-factor enrolment, recovery
	// codes and tokens, which may belong to whoever registered the address first.
	// An error rolls the reset back.
	ProveEmail func(ctx context.Context, subject string) (wasUnverified bool, err error)
}

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

// Access is the composed authentication system: the guard, the enabled
// packages and the routes. It is safe for concurrent use. Create it with [New].
//
// Every package of the module assumes the one [database.Database] given to
// [New]: writes that span packages (registering a user and storing their
// password, deleting a user and forgetting their rows) join the transaction
// carried by the context.
//
// Example:
//
//	iam, err := access.New(db, store, func(iam *access.Builder) {
//		iam.Password(hasher)
//	})
//	if err != nil {
//		return err
//	}
//	web := routes.With(iam.Middleware())
//	iam.Mount(web)
type Access struct {
	db         *database.Database
	store      *cache.Cache
	deliver    func(context.Context, Message) error
	provider   *auth.UserProvider
	guard      *auth.Guard
	directory  *account.Directory
	verifier   *password.Verifier
	factors    *twofactor.Factors
	issuer     *apitoken.Issuer
	links      *federation.Links
	providers  []Provider
	middleware router.Middleware
	migrations []migrate.Definition
	pingers    []func(context.Context) error

	// gate is the middleware that admits only users with a verified email, put
	// before the handlers that connect a provider, enroll two-factor or create
	// tokens; nil when nothing can tell (own users without Users.Verified).
	gate router.Middleware

	// extraSecondFactor and extraStepUp record credentials registered through
	// the Builder, so the handlers that need a registry can tell it is not empty.
	extraSecondFactor bool
	extraStepUp       bool
}

// New composes the facade over db, the application's single database, and
// store, the cache that holds sessions and counters. It does no I/O: the
// application runs [Access.Migrations] with its migrator, and [Access.Ping]
// verifies the tables. Errors wrap [ErrInvalidConfig].
//
// Build order is: the user provider (built-in directory or [Users]), the
// enabled packages, then the guard with their credentials registered. The
// default registries are second factor: totp then recovery; step up: password
// then totp. A federated login is not a step-up method.
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
//	if err != nil {
//		return err
//	}
//	if err := migrator.Add(iam.Migrations()...); err != nil {
//		return err
//	}
func New(db *database.Database, store *cache.Cache, configure ...BuilderFunc) (*Access, error) {
	if db == nil || store == nil {
		return nil, fmt.Errorf("%w: database and store are required", ErrInvalidConfig)
	}
	settings := Builder{}
	for _, apply := range configure {
		apply(&settings)
	}
	iam := &Access{db: db, store: store, deliver: settings.deliver, providers: slices.Clone(settings.providers)}
	if err := iam.validate(settings); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if err := iam.build(settings); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	return iam, nil
}

// validate checks the combinations of settings that no package can see.
func (iam *Access) validate(settings Builder) error {
	names := map[string]bool{}
	for _, provider := range settings.providers {
		if provider == nil || provider.Name() == "" {
			return errors.New("a provider is nil or has an empty name")
		}
		if names[provider.Name()] {
			return fmt.Errorf("duplicate provider name %q: set a distinct name on one of them", provider.Name())
		}
		names[provider.Name()] = true
	}
	if users := settings.users; users != nil {
		if users.Provider == nil {
			return errors.New("Users.Provider is required")
		}
		if settings.hasher != nil && (users.Lookup == nil || users.Rotate == nil) {
			return errors.New("Users.Lookup and Users.Rotate are required with Password")
		}
	}
	if settings.sessionLifetime < 0 {
		return errors.New("negative session lifetime")
	}

	return nil
}

// build constructs the packages and the guard from validated settings.
func (iam *Access) build(settings Builder) error {
	var (
		err    error
		lookup func(context.Context, string) (string, string, error)
		create func(context.Context, string, bool) (string, error)
		rotate func(context.Context, string) error
	)
	if settings.users != nil {
		iam.provider, lookup, create, rotate = settings.users.Provider, settings.users.Lookup, settings.users.Create, settings.users.Rotate
		if verified := settings.users.Verified; verified != nil {
			iam.gate = func(next router.Handler) router.Handler {
				return func(w http.ResponseWriter, r *http.Request) error {
					identity, err := request.Identity(r)
					if err != nil {
						return err
					}
					ok, err := verified(r.Context(), identity.Subject())
					if err != nil {
						return err
					}
					if !ok {
						return account.ErrEmailUnverified
					}

					return next(w, r)
				}
			}
		}
	} else {
		iam.directory, err = account.NewDirectory(iam.db, func(accounts *account.Builder) {
			accounts.Instance(settings.instance).Events(settings.events).Throttle(iam.store, settings.clientIP)
			if settings.deliver != nil {
				accounts.Deliver(func(ctx context.Context, message account.Message) error {
					return settings.deliver(ctx, Message{Kind: KindVerify, Identifier: message.Email, Token: message.Token, ExpiresAt: message.ExpiresAt})
				})
			}
		})
		if err != nil {
			return err
		}
		iam.provider, lookup, rotate = iam.directory.Provider(), iam.directory.Lookup, iam.directory.Rotate
		create = func(ctx context.Context, email string, verified bool) (string, error) {
			if verified {
				return iam.directory.CreateVerified(ctx, email)
			}

			return iam.directory.Create(ctx, email)
		}
		iam.gate = account.RequireVerified()
		iam.add(iam.directory.Migrations(), iam.directory.Ping)
	}
	if settings.hasher != nil {
		users := password.Users{Lookup: lookup, Rotate: rotate, ProveEmail: iam.proveEmail(settings.users)}
		if create != nil {
			users.Create = func(ctx context.Context, identifier string) (string, error) {
				return create(ctx, identifier, false)
			}
		}
		iam.verifier, err = password.NewVerifier(iam.provider, iam.db, iam.store, settings.hasher, users,
			append([]password.BuilderFunc{func(passwords *password.Builder) {
				passwords.AfterChange(func(ctx context.Context, subject string) error {
					switch {
					case iam.issuer == nil:
						return nil
					case settings.revokePersonal:
						return iam.issuer.RevokeAll(ctx, subject)
					default:
						return iam.issuer.RevokeDevices(ctx, subject)
					}
				})
				passwords.Instance(settings.instance).Events(settings.events)
				if settings.clientIP != nil {
					passwords.ClientIP(settings.clientIP)
				}
				if settings.deliver != nil {
					passwords.Deliver(func(ctx context.Context, message password.Message) error {
						return settings.deliver(ctx, Message{Kind: KindReset, Identifier: message.Identifier, Token: message.Token, ExpiresAt: message.ExpiresAt})
					})
				}
				if iam.directory != nil && settings.deliver != nil {
					passwords.Registered(func(ctx context.Context, subject, _ string) error {
						return iam.directory.SendVerification(ctx, subject)
					})
				}
			}}, settings.password...)...)
		if err != nil {
			return err
		}
		iam.add(iam.verifier.Migrations(), iam.verifier.Ping)
	}
	if settings.cipher != nil {
		iam.factors, err = twofactor.NewFactors(iam.provider, iam.db, iam.store, settings.cipher, settings.issuer,
			append([]twofactor.BuilderFunc{func(factors *twofactor.Builder) {
				factors.Instance(settings.instance).Events(settings.events)
			}}, settings.twofactor...)...)
		if err != nil {
			return err
		}
		iam.add(iam.factors.Migrations(), iam.factors.Ping)
	}
	if settings.tokens {
		iam.issuer, err = apitoken.NewIssuer(iam.provider, iam.db,
			append([]apitoken.BuilderFunc{func(tokens *apitoken.Builder) {
				tokens.Instance(settings.instance).Events(settings.events)
			}}, settings.apitoken...)...)
		if err != nil {
			return err
		}
		iam.add(iam.issuer.Migrations(), iam.issuer.Ping)
	}
	if len(settings.providers) > 0 {
		iam.links, err = federation.NewLinks(iam.provider, iam.db, func(links *federation.Builder) {
			links.Instance(settings.instance).Events(settings.events)
			links.Create(iam.createExternal(create))
			if iam.verifier != nil {
				links.Fallback(iam.verifier.Has)
			}
		})
		if err != nil {
			return err
		}
		iam.add(iam.links.Migrations(), iam.links.Ping)
	}

	return iam.buildGuard(settings)
}

// add records the migrations and readiness check of an enabled package.
func (iam *Access) add(migrations []migrate.Definition, ping func(context.Context) error) {
	iam.migrations = append(iam.migrations, migrations...)
	iam.pingers = append(iam.pingers, ping)
}

// createExternal returns the function that makes a user for an unknown external
// identity: [Users.Create] (or the built-in directory) with the provider's
// email, verified according to [auth.External.EmailVerified]. It is nil when
// there is no way to create users, so unknown identities are refused.
func (iam *Access) createExternal(create func(context.Context, string, bool) (string, error)) func(context.Context, auth.External) (string, error) {
	if create == nil {
		return nil
	}

	return func(ctx context.Context, external auth.External) (string, error) {
		return create(ctx, external.Email, external.EmailVerified)
	}
}

// proveEmail returns the hook a password reset runs in its transaction to turn
// the proof of mailbox ownership into a verified email. When the email was not
// verified before, whoever registered the address first may have attached
// things to the account (a connected provider, an authenticator, recovery
// codes, tokens), so all of them are deleted; the reset itself rotates the user
// and the handler ends the logins. It reads the packages when it runs, because
// they are built after it. It is nil when there is nothing to prove with: own
// users without [Users.ProveEmail].
func (iam *Access) proveEmail(users *Users) func(context.Context, string) error {
	prove := func(ctx context.Context, subject string) (bool, error) {
		return iam.directory.ProveEmail(ctx, subject)
	}
	if users != nil {
		if users.ProveEmail == nil {
			return nil
		}
		prove = users.ProveEmail
	}

	return func(ctx context.Context, subject string) error {
		wasUnverified, err := prove(ctx, subject)
		if err != nil || !wasUnverified {
			return err
		}
		if iam.links != nil {
			if err := iam.links.UnlinkAll(ctx, subject); err != nil {
				return err
			}
		}
		if iam.factors != nil {
			if err := iam.factors.Remove(ctx, subject); err != nil {
				return err
			}
		}
		if iam.issuer != nil {
			return iam.issuer.RevokeAll(ctx, subject)
		}

		return nil
	}
}

// buildGuard builds the guard with the default credential registries and the
// middleware chain.
func (iam *Access) buildGuard(settings Builder) error {
	var secondFactor, stepUp []auth.Credential
	if iam.verifier != nil {
		stepUp = append(stepUp, iam.verifier.Credential())
	}
	if iam.factors != nil {
		secondFactor = append(secondFactor, iam.factors.Credential(), iam.factors.RecoveryCredential())
		stepUp = append(stepUp, iam.factors.Credential())
	}
	secondFactor = append(secondFactor, settings.secondFactor...)
	stepUp = append(stepUp, settings.stepUp...)
	iam.extraSecondFactor, iam.extraStepUp = len(settings.secondFactor) > 0, len(settings.stepUp) > 0
	lifetime := settings.sessionLifetime
	if lifetime == 0 {
		lifetime = session.DefaultMaxLifetime
	}
	var err error
	iam.guard, err = auth.NewGuard(iam.provider, iam.store, append([]auth.GuardBuilderFunc{func(policy *auth.GuardBuilder) {
		policy.SessionLifetime(lifetime)
		if settings.clientIP != nil {
			policy.ClientIP(settings.clientIP)
		}
		if len(secondFactor) > 0 {
			policy.SecondFactor(secondFactor...)
		}
		if len(stepUp) > 0 {
			policy.StepUp(stepUp...)
		}
	}}, settings.guard...)...)
	if err != nil {
		return err
	}
	authenticators := []auth.Authenticator{iam.guard.Authenticator()}
	if iam.issuer != nil {
		authenticators = append(authenticators, iam.issuer.Authenticator())
	}
	authenticators = append(authenticators, settings.authenticators...)
	middlewares := []router.Middleware{
		session.Middleware(iam.store, append([]session.BuilderFunc{func(cookies *session.Builder) {
			cookies.MaxLifetime(lifetime)
		}}, settings.sessionPolicy...)...),
		auth.Middleware(authenticators...),
	}
	iam.middleware = func(next router.Handler) router.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			next = middlewares[i](next)
		}

		return next
	}

	return nil
}

// Guard returns the guard the facade built, for code that logs users in itself
// (a custom provider, an SSO callback) or protects routes with
// [auth.Guard.RequireFresh].
//
// Example:
//
//	api.With(iam.Guard().RequireFresh(5 * time.Minute)).Delete("/billing", cancel)
func (iam *Access) Guard() *auth.Guard {
	return iam.guard
}

// UserProvider returns the [auth.UserProvider] every package shares: the
// built-in directory's, or the one given in [Users].
//
// Example:
//
//	record, err := iam.UserProvider().Find(ctx, subject)
func (iam *Access) UserProvider() *auth.UserProvider {
	return iam.provider
}

// Directory returns the built-in users table, or nil when the application
// brought its own with [Builder.Users].
//
// Example:
//
//	if directory := iam.Directory(); directory != nil {
//		_ = directory.Disable(ctx, subject)
//	}
func (iam *Access) Directory() *account.Directory {
	return iam.directory
}

// Verifier returns the password package's verifier, or nil without
// [Builder.Password].
//
// Example:
//
//	if verifier := iam.Verifier(); verifier != nil {
//		_ = verifier.Set(ctx, subject, "a new long password")
//	}
func (iam *Access) Verifier() *password.Verifier {
	return iam.verifier
}

// Factors returns the two-factor store, or nil without [Builder.TwoFactor].
//
// Example:
//
//	if factors := iam.Factors(); factors != nil {
//		status, _ := factors.Status(ctx, subject)
//		_ = status
//	}
func (iam *Access) Factors() *twofactor.Factors {
	return iam.factors
}

// Issuer returns the token issuer, or nil without [Builder.Tokens].
//
// Example:
//
//	if issuer := iam.Issuer(); issuer != nil {
//		_ = issuer.RevokeAll(ctx, subject)
//	}
func (iam *Access) Issuer() *apitoken.Issuer {
	return iam.issuer
}

// Links returns the connection store, or nil without [Builder.Provider].
//
// Example:
//
//	if links := iam.Links(); links != nil {
//		connections, _ := links.List(ctx, subject)
//		_ = connections
//	}
func (iam *Access) Links() *federation.Links {
	return iam.links
}

// Resolve is the [auth.Resolver] the facade gives to its providers: it maps a
// verified external identity to a local subject through the connection store.
// It is nil-safe to call only with [Builder.Provider]; without providers it
// returns [federation.ErrNotLinked]. Use it to mount a provider by hand.
//
// Example:
//
//	web.Get("/sso/callback", provider.CallbackHandler(iam.Guard(), iam.Resolve))
func (iam *Access) Resolve(ctx context.Context, external auth.External) (string, error) {
	if iam.links == nil {
		return "", federation.ErrNotLinked
	}

	return iam.links.Resolve(ctx, external)
}

// MustAccept panics at composition unless every method is registered in the
// guard's second-factor or step-up registry, so a typo in a method name or a
// module that was never registered fails at startup instead of on the first
// request.
//
// Example:
//
//	iam.MustAccept(auth.MethodPassword, auth.MethodOTP)
func (iam *Access) MustAccept(methods ...auth.Method) {
	for _, method := range methods {
		iam.guard.MustAccept(method)
	}
}

// Wait blocks until the emails that the forgot-password and resend handlers sent
// in the background have been handed to [Builder.Deliver], or returns the error
// of ctx when it ends first. Those handlers answer 202 at once and deliver
// afterwards, so call Wait during shutdown, after the server stopped accepting
// requests; tests call it before asserting on what was delivered.
//
// Example:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	_ = iam.Wait(ctx)
func (iam *Access) Wait(ctx context.Context) error {
	if iam.verifier != nil {
		if err := iam.verifier.Wait(ctx); err != nil {
			return err
		}
	}
	if iam.directory != nil {
		return iam.directory.Wait(ctx)
	}

	return nil
}

// Migrations returns the migrations of the enabled features only, in the order
// the tables depend on each other: users, passwords, two-factor, tokens,
// connections. Each package keeps its own migration group, so they may also be
// added separately. The application adds them to its migrator; the facade never
// runs DDL.
//
// Example:
//
//	if err := migrator.Add(iam.Migrations()...); err != nil {
//		return err
//	}
func (iam *Access) Migrations() []migrate.Definition {
	return slices.Clone(iam.migrations)
}

// Ping verifies, read-only, that the tables of every enabled feature exist and
// returns the first failure, which wraps [ErrNotMigrated]. Use it as a
// readiness check.
//
// Example:
//
//	if err := iam.Ping(ctx); err != nil {
//		return err
//	}
func (iam *Access) Ping(ctx context.Context) error {
	for _, ping := range iam.pingers {
		if err := ping(ctx); err != nil {
			return err
		}
	}

	return nil
}

// Middleware returns the middleware to install on every route that identifies
// users: the session middleware, then the authenticators in order (the guard's
// browser session, the token issuer's bearer tokens when enabled, and those
// added with [Builder.Authenticator]). Apply it once per router and protect
// routes with auth.Require.
//
// Example:
//
//	web := routes.With(iam.Middleware())
//	api := routes.With(iam.Middleware(), auth.Require())
func (iam *Access) Middleware() router.Middleware {
	return iam.middleware
}
