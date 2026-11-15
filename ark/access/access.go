package access

import (
	"context"
	"errors"
	"fmt"
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
	if err := settings.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	iam := &Access{
		db:           db,
		store:        store,
		deliver:      settings.deliver,
		providers:    slices.Clone(settings.providers),
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
