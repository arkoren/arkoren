package access

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"

	"arkoren.dev/ark/access/account"
	"arkoren.dev/ark/access/apitoken"
	"arkoren.dev/ark/access/federation"
	"arkoren.dev/ark/access/internal/storage"
	"arkoren.dev/ark/access/password"
	"arkoren.dev/ark/access/twofactor"
)

// ErrInvalidConfig is wrapped by the errors of [New] for an unusable
// configuration: a missing dependency (database, store or client address
// source), a duplicate provider name, an invalid instance or scheme, a nil
// two-factor cipher, [Builder.Directory] combined with [Builder.Users],
// registration with the built-in users without [password.Builder.Reset], or a
// failing package constructor (registration or sign-up without users that can
// be created, tokens with no kind enabled, a scope catalog without a grant
// function).
//
// Example:
//
//	_, err := access.New(nil, nil, nil)
//	fmt.Println(errors.Is(err, access.ErrInvalidConfig)) // true
var ErrInvalidConfig = errors.New("access: invalid configuration")

// ErrIdentifierTaken is what [Users.Create] returns, or wraps, for an identifier
// that already belongs to a user. Registration then answers 202 like for a new
// user and notifies the owner. It is
// [password.ErrIdentifierTaken].
//
// Example:
//
//	create := func(ctx context.Context, identifier string, verified bool) (string, error) {
//		if exists(identifier) {
//			return "", access.ErrIdentifierTaken
//		}
//		return insert(ctx, identifier, verified)
//	}
var ErrIdentifierTaken = password.ErrIdentifierTaken

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
//	iam, err := access.New(db, store, ratelimit.TrustedProxyKey(), func(iam *access.Builder) {
//		iam.Password(hasher, func(passwords *password.Builder) {
//			passwords.Registration(true)
//		})
//	})
//	if err != nil {
//		return err
//	}
//	web := routes.With(iam.Middleware())
//	iam.Mount(web)
type Access struct {
	db         *database.Database
	store      *cache.Cache
	clientIP   func(*http.Request) string
	provider   *auth.UserProvider
	guard      *auth.Guard
	directory  *account.Directory
	verifier   *password.Verifier
	factors    *twofactor.Factors
	issuer     *apitoken.Issuer
	links      *federation.Links
	providers  []mountedProvider
	middleware router.Middleware
	migrations []migrate.Definition
	pingers    []func(context.Context) error

	// verified tells whether a user's email is verified: the directory's, or
	// Users.Verified. It is the Verified setting of the packages whose handlers
	// connect a provider, enroll two-factor or create tokens, and is nil only
	// with the application's own users and no Users.Verified, validated to
	// come together with Users.ProveEmail.
	verified func(context.Context, string) (bool, error)

	// emailProven and keepPersonal are [Builder.OnEmailProven] and
	// [Builder.KeepPersonalTokens], read by the hooks when they run.
	emailProven  func(context.Context, string) error
	keepPersonal bool

	// secondFactor and stepUp report whether the guard's registries are not
	// empty, which the handlers that need one check.
	secondFactor bool
	stepUp       bool
}

// New composes the facade over db, the application's single database, store,
// the cache that holds sessions and counters, and clientIP, which finds the
// client address for the guard's login records and the throttles of every
// package. clientIP is required, because the right source depends on the
// deployment and a wrong default silently breaks the throttles: behind a
// reverse proxy r.RemoteAddr is the proxy, so every user shares one counter,
// and a header read without trusting the proxy that set it lets an attacker
// pick their own counter. Pass ratelimit.TrustedProxyKey with your proxies'
// prefixes, or with none when no proxy sits in front of the application (it
// then returns the host of r.RemoteAddr, an IPv6 address reduced to its /64).
// It does no I/O: the
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
//	clientIP := ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8"))
//	scheme := totp.New("Acme") // the name authenticator apps show for this account
//	iam, err := access.New(db, store, clientIP, func(iam *access.Builder) {
//		iam.Password(hasher, func(passwords *password.Builder) {
//			passwords.Registration(true)
//		})
//		iam.TwoFactor(scheme, cipher)
//		iam.Tokens()
//		iam.Provider(google)
//		iam.Provider(github)
//	})
//	if err != nil {
//		return err
//	}
//	if err := migrator.Add(iam.Migrations()...); err != nil {
//		return err
//	}
func New(db *database.Database, store *cache.Cache, clientIP func(*http.Request) string, configure ...BuilderFunc) (*Access, error) {
	if db == nil || store == nil || clientIP == nil {
		return nil, fmt.Errorf("%w: database, store and client address source are required", ErrInvalidConfig)
	}
	settings := Builder{}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := settings.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	iam := &Access{
		db:           db,
		store:        store,
		clientIP:     clientIP,
		emailProven:  settings.emailProven,
		keepPersonal: settings.keepPersonal,
	}
	if err := iam.build(&settings); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	return iam, nil
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
// brought its own with [Builder.Users]. With registration and sign-up both off
// this is how the application adds users: [account.Directory.Create] and
// [account.Directory.CreateVerified].
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
// returns [federation.ErrNotLinked]. Use it to mount a provider by hand. It
// never signs users up, because sign-up is a setting of each provider given to
// [Builder.Provider]: a first login with an unknown identity is
// [federation.ErrNoAccount] (the takeover of an unverified user still works).
// For a hand-mounted provider that signs up, make its resolver with
// [federation.Links.Resolver] on [Access.Links], which creates users with the
// facade's own creation.
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

// SendReset is the worker side of a password reset; see
// [password.Verifier.SendReset]. It has the shape func(context.Context,
// password.ResetRequest) error, so a queue consumer or a workflow step can use
// it directly: the dispatch function of [password.Builder.Reset] stores a
// [password.ResetRequest] and the worker passes it here. An unknown user sends
// nothing and returns nil; an error of the deliver function is returned so the
// queue retries, which means one request may deliver more than once. Without
// [Builder.Password] it returns an error wrapping [ErrInvalidConfig], and without
// [password.Builder.Reset] one wrapping [password.ErrInvalidConfig].
//
// Example:
//
//	consumer.Handle(resetJob, iam.SendReset)
func (iam *Access) SendReset(ctx context.Context, request password.ResetRequest) error {
	if iam.verifier == nil {
		return fmt.Errorf("%w: SendReset requires Builder.Password", ErrInvalidConfig)
	}

	return iam.verifier.SendReset(ctx, request)
}

// SendVerification is the worker side of an email verification; see
// [account.Directory.SendVerification]. It has the shape func(context.Context,
// account.VerifyRequest) error, so a queue consumer or a workflow step can use
// it directly. A deleted or already verified user sends nothing and returns nil;
// an error of the deliver function is returned so the queue retries, which means
// one request may deliver more than once. It returns an error wrapping
// [ErrInvalidConfig] with the application's own [Builder.Users], or without
// [account.Builder.Verification].
//
// Example:
//
//	consumer.Handle(verifyJob, iam.SendVerification)
func (iam *Access) SendVerification(ctx context.Context, request account.VerifyRequest) error {
	if iam.directory == nil {
		return fmt.Errorf("%w: SendVerification requires the built-in users", ErrInvalidConfig)
	}

	return iam.directory.SendVerification(ctx, request)
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

// Prune deletes the tokens that can no longer authenticate: expired tokens,
// tokens a version change of their user ended, device tokens unused for longer than [apitoken.Builder.DeviceIdleTTL] and
// device tokens that were never used and are older than that; see
// [apitoken.Issuer.Prune]. Without [Builder.Tokens] there is nothing to prune
// and it returns nil, so one scheduled entry works for every composition. It
// changes no behavior (the bearer check already rejects these tokens), only
// keeps the table small, and it is safe to run concurrently and repeatedly.
//
// It has the shape func(context.Context) error, so it can be a scheduled entry
// run daily.
//
// Example:
//
//	err := scheduler.Add(schedule.NewEntry("access-prune", iam.Prune, func(entry *schedule.EntryBuilder) {
//		entry.When(schedule.UTC().Hour(3))
//	}))
func (iam *Access) Prune(ctx context.Context) error {
	if iam.issuer == nil {
		return nil
	}

	return iam.issuer.Prune(ctx)
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
