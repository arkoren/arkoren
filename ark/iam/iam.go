package iam

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/core/service/queue"

	"arkoren.dev/ark/iam/account"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/federation"
	"arkoren.dev/ark/iam/internal/migration"
	"arkoren.dev/ark/iam/internal/storage"
	"arkoren.dev/ark/iam/password"
	"arkoren.dev/ark/iam/permission"
	"arkoren.dev/ark/iam/twofactor"
)

// ErrInvalidConfig is wrapped by the errors of [New] for an unusable
// configuration: a missing dependency (database, store or client address
// source), a duplicate provider name, an invalid instance or scheme, an enabled
// feature without its required components (a nil hasher or two-factor cipher,
// or [password.Builder.Reset] or [account.Builder.Verification] without a signer,
// a mailer, a page address or [Builder.Queue]),
// [Builder.Directory] combined with [Builder.Users], or a failing package
// constructor (tokens with no kind enabled, a scope catalog without a grant
// function, an invalid or conflicting permission definition). A setting whose
// dependency is off is not an error: registration without users that can be
// created or, with the built-in users, without [password.Builder.Reset], and
// sign-up without users that can be created, are ignored with one warning
// logged through slog.Default naming the setting.
//
// Example:
//
//	_, err := iam.New(nil, nil)
//	fmt.Println(errors.Is(err, iam.ErrInvalidConfig)) // true
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
//			return "", iam.ErrIdentifierTaken
//		}
//		return insert(ctx, identifier, verified)
//	}
var ErrIdentifierTaken = password.ErrIdentifierTaken

// ErrNotMigrated is returned by [IAM.Ping] when the tables of an enabled
// feature do not exist. Add [IAM.Migrations] to the application's migrator
// and run them.
//
// Example:
//
//	if errors.Is(identity.Ping(ctx), iam.ErrNotMigrated) {
//		// run the migrator
//	}
var ErrNotMigrated = storage.ErrNotMigrated

// IAM is the composed authentication system: the guard, the enabled
// packages and the routes. It is safe for concurrent use. Create it with [New].
//
// Every package of the module assumes the one [database.Database] given to
// [New]: writes that span packages (registering a user and storing their
// password, deleting a user and forgetting their rows) join the transaction
// carried by the context.
//
// Example:
//
//	identity, err := iam.New(db, store, func(identity *iam.Builder) {
//		identity.Password(hasher)
//	})
//	if err != nil {
//		return err
//	}
//	web := routes.With(identity.Middleware())
//	web.Group("/", identity.Register)
type IAM struct {
	db          *database.Database
	store       *cache.Cache
	clientIP    func(*http.Request) string
	provider    *auth.UserProvider
	guard       *auth.Guard
	directory   *account.Directory
	verifier    *password.Verifier
	factors     *twofactor.Factors
	issuer      *apitoken.Issuer
	permissions *permission.Store
	links       *federation.Links
	providers   []mountedProvider
	account     *AccountBuilder
	middleware  router.Middleware
	instance    string
	pingers     []func(context.Context) error

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

	// registration reports whether POST /register is on: the password package
	// has Registration, and with the built-in users also Reset. [New] ignores
	// the setting otherwise.
	registration bool

	// secondFactor and stepUp report whether the guard's registries are not
	// empty, which the handlers that need one check.
	secondFactor bool
	stepUp       bool
}

// New composes the facade over db, the application's single database, store,
// the cache that holds sessions and counters. The default client address is the
// host of RemoteAddr, without trusting forwarding headers. Behind a
// reverse proxy r.RemoteAddr is the proxy, so every user shares one counter,
// and a header read without trusting the proxy that set it lets an attacker
// pick their own counter. Set [Builder.ClientIP] to a resolver from
// arkoren.dev/core/http/clientip when trusted infrastructure forwards requests.
// IAM does no I/O at construction: the application runs [IAM.Migrations] with
// its migrator, and [IAM.Ping]
// verifies the tables. Errors wrap [ErrInvalidConfig]; a setting that depends
// on a feature that is off is ignored with a warning instead.
//
// Build order is: the user provider (built-in directory or [Users]), the
// enabled packages, then the guard with their credentials registered. The
// default registries are second factor: totp then recovery; step up: password
// then totp. A federated login is not a step-up method.
//
// Example:
//
//	identity, err := iam.New(db, store, func(identity *iam.Builder) {
//		identity.Password(hasher)
//	})
//	if err != nil {
//		return err
//	}
//	if err := migrator.Add(identity.Migrations()...); err != nil {
//		return err
//	}
func New(db *database.Database, store *cache.Cache, configure ...BuilderFunc) (*IAM, error) {
	if db == nil || store == nil {
		return nil, fmt.Errorf("%w: database and store are required", ErrInvalidConfig)
	}
	settings := Builder{clientIP: remoteClientIP}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := settings.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	identity := &IAM{
		db:           db,
		store:        store,
		clientIP:     settings.clientIP,
		instance:     settings.instance,
		emailProven:  settings.emailProven,
		keepPersonal: settings.keepPersonal,
	}
	if err := identity.build(&settings); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	return identity, nil
}

// remoteClientIP uses the peer host without trusting forwarding headers. If
// RemoteAddr is not host:port, it returns the original value, matching auth.
func remoteClientIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}

	return host
}

// Guard returns the guard the facade built, for code that logs users in itself
// (a custom provider, an SSO callback) or protects routes with
// [auth.Guard.RequireFresh].
//
// Example:
//
//	api.With(identity.Guard().RequireFresh(5 * time.Minute)).Delete("/billing", cancel)
func (identity *IAM) Guard() *auth.Guard {
	return identity.guard
}

// UserProvider returns the [auth.UserProvider] every package shares: the
// built-in directory's, or the one given in [Users].
//
// Example:
//
//	record, err := identity.UserProvider().Find(ctx, subject)
func (identity *IAM) UserProvider() *auth.UserProvider {
	return identity.provider
}

// Directory returns the built-in users table, or nil when the application
// brought its own with [Builder.Users]. With registration and sign-up both off
// this is how the application adds users: [account.Directory.Create] and
// [account.Directory.CreateVerified].
//
// Example:
//
//	if directory := identity.Directory(); directory != nil {
//		_ = directory.Disable(ctx, subject)
//	}
func (identity *IAM) Directory() *account.Directory {
	return identity.directory
}

// Verifier returns the password package's verifier, or nil without
// [Builder.Password].
//
// Example:
//
//	if verifier := identity.Verifier(); verifier != nil {
//		_ = verifier.Set(ctx, subject, "a new long password")
//	}
func (identity *IAM) Verifier() *password.Verifier {
	return identity.verifier
}

// Factors returns the two-factor store, or nil without [Builder.TwoFactor].
//
// Example:
//
//	if factors := identity.Factors(); factors != nil {
//		status, _ := factors.Status(ctx, subject)
//		_ = status
//	}
func (identity *IAM) Factors() *twofactor.Factors {
	return identity.factors
}

// Issuer returns the token issuer, or nil without [Builder.Tokens].
//
// Example:
//
//	if issuer := identity.Issuer(); issuer != nil {
//		_ = issuer.RevokeAll(ctx, subject)
//	}
func (identity *IAM) Issuer() *apitoken.Issuer {
	return identity.issuer
}

// Links returns the connection store, or nil without [Builder.Provider].
//
// Example:
//
//	if links := identity.Links(); links != nil {
//		connections, _ := links.List(ctx, subject)
//		_ = connections
//	}
func (identity *IAM) Links() *federation.Links {
	return identity.links
}

// Resolve is the [auth.Resolver] the facade gives to its providers: it maps a
// verified external identity to a local subject through the connection store.
// It is nil-safe to call only with [Builder.Provider]; without providers it
// returns [federation.ErrNotLinked]. Use it to mount a provider by hand. It
// never signs users up, because sign-up is a setting of each provider given to
// [Builder.Provider]: a first login with an unknown identity is
// [federation.ErrNoAccount] (the takeover of an unverified user still works).
// For a hand-mounted provider that signs up, make its resolver with
// [federation.Links.Resolver] on [IAM.Links], which creates users with the
// facade's own creation.
//
// Example:
//
//	web.Get("/sso/callback", provider.CallbackHandler(identity.Guard(), identity.Resolve))
func (identity *IAM) Resolve(ctx context.Context, external auth.External) (string, error) {
	if identity.links == nil {
		return "", federation.ErrNotLinked
	}

	return identity.links.Resolve(ctx, external)
}

// MustAccept panics at composition unless every method is registered in the
// guard's second-factor or step-up registry, so a typo in a method name or a
// module that was never registered fails at startup instead of on the first
// request.
//
// Example:
//
//	identity.MustAccept(auth.MethodPassword, auth.MethodOTP)
func (identity *IAM) MustAccept(methods ...auth.Method) {
	for _, method := range methods {
		identity.guard.MustAccept(method)
	}
}

// QueueName is the name of the queue every job of the module runs on. A consumer
// that restricts its queues with queue.ConsumerBuilder.Queues must include it, or
// the emails of [password.Builder.Reset] and [account.Builder.Verification] are
// stored and never sent.
//
// Example:
//
//	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
//		consumer.Queues("emails", iam.QueueName)
//	})
const QueueName = storage.Queue

// RegisterJobs registers the handlers of the module's background jobs on
// consumer, so the process that runs the consumer sends the emails of
// [password.Builder.Reset] and [account.Builder.Verification]. The application
// does not name the jobs and nothing else needs wiring: new jobs of a feature
// that is enabled register here too. Features that are off register nothing, so
// a process that builds the same [IAM] as the one that dispatches registers
// exactly the jobs it can receive. Job names carry the [Builder.Instance], so
// several instances can share one consumer.
//
// The jobs run on the queue [QueueName]. RegisterJobs cannot see a queue
// restriction of the consumer: a consumer built with queue.ConsumerBuilder.Queues
// must list [QueueName], or stored jobs wait unconsumed. It returns the first
// error of the registrations, for example queue.ErrDuplicateName when the
// consumer already handles the module's jobs. Run it at composition and let the
// consumer's own runner consume.
//
// Example:
//
//	consumer := queue.NewConsumer(driver)
//	if err := identity.RegisterJobs(consumer); err != nil {
//		return err
//	}
//	compose.Run(consumer.Run)
func (identity *IAM) RegisterJobs(consumer *queue.Consumer) error {
	if identity.verifier != nil {
		if err := identity.verifier.RegisterJobs(consumer); err != nil {
			return err
		}
	}
	if identity.directory != nil {
		return identity.directory.RegisterJobs(consumer)
	}

	return nil
}

// Migrations returns the migrations of every feature of the module for this
// instance, in the order their tables depend on each other: users, passwords,
// two-factor, tokens, connections, permissions. It does not depend on which
// features are enabled, so enabling a feature later never needs a new migrator
// call: its table already exists and its readiness check already passes. The
// only structural choice that removes a group is [Builder.Users]: with the
// application's own users, the built-in users group is left out.
//
// Each package keeps its own migration group (see the Migrations method of each
// package, which returns the same definitions), so they may also be added
// separately. Groups are append-only: a release that changes a table adds a
// migration at the end of its group, never rewrites a shipped one. The
// application adds the definitions to its migrator; the facade never runs DDL.
// [IAM.Ping] still checks the tables of the enabled features only.
//
// Example:
//
//	if err := migrator.Add(identity.Migrations()...); err != nil {
//		return err
//	}
func (identity *IAM) Migrations() []migrate.Definition {
	var definitions []migrate.Definition
	if identity.directory != nil {
		definitions = append(definitions, migration.Account(identity.instance)...)
	}
	definitions = append(definitions, migration.Password(identity.instance)...)
	definitions = append(definitions, migration.TwoFactor(identity.instance)...)
	definitions = append(definitions, migration.Tokens(identity.instance)...)
	definitions = append(definitions, migration.Links(identity.instance)...)

	return append(definitions, migration.Permissions(identity.instance)...)
}

// Ping verifies, read-only, that the tables of every enabled feature exist and
// returns the first failure, which wraps [ErrNotMigrated]. Use it as a
// readiness check.
//
// Example:
//
//	if err := identity.Ping(ctx); err != nil {
//		return err
//	}
func (identity *IAM) Ping(ctx context.Context) error {
	for _, ping := range identity.pingers {
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
//	err := scheduler.Add(schedule.NewEntry("access-prune", identity.Prune, func(entry *schedule.EntryBuilder) {
//		entry.When(schedule.UTC().Hour(3))
//	}))
func (identity *IAM) Prune(ctx context.Context) error {
	if identity.issuer == nil {
		return nil
	}

	return identity.issuer.Prune(ctx)
}

// Middleware returns the middleware to install on every route that identifies
// users: the session middleware, then the authenticators in order (the guard's
// browser session, the token issuer's bearer tokens when enabled, and those
// added with [Builder.Authenticator]). Apply it once per router and protect
// routes with auth.Require.
//
// Example:
//
//	web := routes.With(identity.Middleware())
//	api := routes.With(identity.Middleware(), auth.Require())
func (identity *IAM) Middleware() router.Middleware {
	return identity.middleware
}
