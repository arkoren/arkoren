# Examples

A small shop service shaped by the arkoren-structure rules: a `server` with HTTP routes and an event listener, a `worker` consuming jobs, and a `migrate` one-shot. Every file compiles and passes `go vet` and `arkoren check` against `arkoren.dev/core` v0.19.0 (the identity files against `arkoren.dev/ark/iam` v0.5.0), and the tests at the end pass. Drivers are SQLite and the memory queue; swap them inside the bootstrap providers without touching features. Generated files keep their long doc comments in a real project; they are shortened here.

## Contents

- Processes: thin `main`, the `App` provider and the `Compose` of each process
- Bootstrap providers: database (two singletons, one constructor), queue, migrator, router
- Feature wiring: the providers in `internal/bootstrap/<feature>.go` that build each feature and make its surfaces live
- Identity (`ark/iam`): the `IAM` provider, fragment providers that return construction callbacks, the router, the email jobs, the shared providers they use
- Migrations: schema-builder operations extracted into exported variables
- Feature roots: plain Go with dependencies as constructor arguments, a store with exported query functions, a service with an extracted transaction body and an after-commit event, a cross-feature interface
- Web surface: handlers using the request and response helpers, a validation schema, problems
- Other surfaces: job handlers, an event listener in another feature
- Tests: a handler test with no composer, the whole process started with `application.Start`, and the identity flow with a test root provider that supplies the mailer

## Processes

`main` only runs `application.Run` with the process root's `App` and `Compose`. `App` is a provider holding the application's settings, so the log handler comes from `bootstrap.LogHandler` like any other shared value. `Compose` is the explicit list of what the process runs: each `compose.Use` builds a provider, and each provider makes its component live (mounts routes, subscribes listeners, registers lifecycle). `arkoren add process worker` generates the `worker` and `migrate` roots with the same `App` shape and the service names `shop-worker` and `shop-migrate`.

`cmd/server/main.go`

```go
package main

import (
	"context"
	"os"

	_ "github.com/joho/godotenv/autoload"

	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap/server"
)

// main maps run's error to the exit status after cleanup finishes.
func main() {
	if run() != nil {
		os.Exit(1)
	}
}

// run builds the application from server.App and runs server.Compose.
// application.Run owns construction, composition, startup and shutdown, and
// reports lifecycle failures itself, so run returns its error unchanged.
func run() error {
	ctx, stop := application.NewSignalContext(context.Background())
	defer stop()

	return application.Run(ctx, server.App, server.Compose)
}
```

`internal/bootstrap/server/app.go`

```go
package server

import (
	"os"

	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap"
)

// App is the server process's application: its settings, built by the first
// application.Run or application.Start that receives it. Debug is enabled only
// when APP_DEBUG is exactly "true".
var App = application.Provide(func(compose *application.Composer) *application.Application {
	return application.New(func(app *application.Builder) {
		app.Name("shop")
		app.Debug(os.Getenv("APP_DEBUG") == "true")
		app.LogHandler(compose.Use(bootstrap.LogHandler))
	})
})
```

`internal/bootstrap/server/compose.go`

```go
package server

import (
	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap"
)

// Compose is the explicit list of what the server process runs: feature
// providers first (the migrations before the routes, the identity router before
// the routes that need the user, the identity jobs before the consumer that runs
// them), diagnostics only in debug, and the HTTP server last.
func Compose(compose *application.Composer) {
	compose.Use(bootstrap.Migrate)
	compose.Use(bootstrap.IAMRouter)
	compose.Use(bootstrap.OrdersRoutes)
	compose.Use(bootstrap.NotificationsListeners)
	compose.Use(bootstrap.IAMJobs)
	compose.Use(bootstrap.Work)

	if compose.App().Debug() {
		compose.Use(bootstrap.MCP)
	}

	compose.Use(bootstrap.Server)
}
```

`internal/bootstrap/worker/compose.go`

```go
package worker

import (
	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap"
)

// Compose is the explicit list of what the worker process runs: the job
// handlers, then the consumer that runs them. A worker has no HTTP server.
func Compose(compose *application.Composer) {
	compose.Use(bootstrap.OrdersJobs)
	compose.Use(bootstrap.Work)
}
```

`internal/bootstrap/migrate/compose.go`

```go
package migrate

import (
	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap"
)

// Compose runs pending migrations once, as the process's only runner: the
// process ends when Up returns. The server applies them at startup through
// bootstrap.Migrate; this process runs them to completion before a deploy.
func Compose(compose *application.Composer) {
	compose.Run(compose.Use(bootstrap.Migrator).Up)
}
```

## Bootstrap providers

Every exported declaration of `internal/bootstrap` is a provider variable that returns the component it makes live. A provider `Use`s its dependencies first, then constructs and registers lifecycle. `NewDatabase` is the one allowed exception: a value-returning constructor that opens a new pool on every call, so only providers call it. `Work` registers a runner and `Migrate` a startup callback, and each returns the component, so `Compose` stays a list of `Use` calls. The generated `Router` also mounts the welcome page; it is trimmed here.

`internal/bootstrap/database.go`

```go
package bootstrap

import (
	"os"

	"arkoren.dev/core/application"
	"arkoren.dev/core/service/database"
	"arkoren.dev/driver/sqlite/sqlitedatabase"
)

// Database is the process's primary database, opened from DATABASE_URL.
var Database = application.Provide(func(compose *application.Composer) *database.Database {
	return NewDatabase(compose, "DATABASE_URL")
})

// ReportingDatabase is a second singleton of the same kind, opened from
// REPORTING_DATABASE_URL and sharing NewDatabase.
var ReportingDatabase = application.Provide(func(compose *application.Composer) *database.Database {
	return NewDatabase(compose, "REPORTING_DATABASE_URL")
})

// NewDatabase opens a new pool from the environment variable envKey and
// registers its lifecycle: the pool starts with the application and stops when
// it shuts down. Each call opens a separate pool, so only providers call it.
func NewDatabase(compose *application.Composer, envKey string) *database.Database {
	driver := compose.Must(sqlitedatabase.New(os.Getenv(envKey)))
	compose.Cleanup(driver.Stop)
	compose.Startup(driver.Start)

	return database.New(driver)
}
```

`internal/bootstrap/queue.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue"
)

// Queue is the process's queue driver. The memory driver keeps jobs in this
// process only; swap in a networked driver here without touching features.
var Queue = application.Provide(func(compose *application.Composer) *memoryqueue.Driver {
	driver := memoryqueue.New()
	compose.Cleanup(driver.Stop)
	compose.Startup(driver.Start)

	return driver
})

// Dispatcher sends jobs through Queue.
var Dispatcher = application.Provide(func(compose *application.Composer) *queue.Dispatcher {
	return queue.NewDispatcher(compose.Use(Queue))
})

// Consumer receives jobs from Queue. Job providers such as OrdersJobs register
// their handlers on it.
var Consumer = application.Provide(func(compose *application.Composer) *queue.Consumer {
	return queue.NewConsumer(compose.Use(Queue))
})

// Work runs the consumer as a supervised runner and returns it. List it last in
// a worker's Compose, after every provider that registers handlers.
var Work = application.Provide(func(compose *application.Composer) *queue.Consumer {
	consumer := compose.Use(Consumer)

	compose.Run(consumer.Run)

	return consumer
})
```

`internal/bootstrap/migrator.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/database/migrate"

	"example.com/shop/internal/migrations"
)

// Migrator holds the application's own migrations and each ark's. IAM.Migrations
// returns every feature's groups whichever are enabled, so building IAM here
// means a process that uses the migrator needs IAM's settings (keys, URLs) too.
var Migrator = application.Provide(func(compose *application.Composer) *migrate.Migrator {
	migrator := migrate.New(compose.Use(Database))
	compose.Require(migrator.Add(migrations.All()...))
	compose.Require(migrator.Add(compose.Use(IAM).Migrations()...))

	return migrator
})

// Migrate applies the pending migrations during startup, before the process
// serves requests or reports ready, and returns the migrator. Up holds the
// migration lock, so replicas starting together are safe: one applies the
// migrations while the others wait. For production, take Migrate out of the
// server's Compose and run the migrations in a dedicated process created with
// `arkoren add process migrate`, whose Compose registers
// compose.Run(compose.Use(bootstrap.Migrator).Up): the process ends when Up
// returns, so a deploy runs it to completion before starting the new servers.
var Migrate = application.Provide(func(compose *application.Composer) *migrate.Migrator {
	migrator := compose.Use(Migrator)

	compose.Startup(migrator.Up)

	return migrator
})
```

`internal/bootstrap/router.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/http/middleware/csrf"
	"arkoren.dev/core/http/middleware/secureheaders"
	"arkoren.dev/core/http/router"
)

// Router is the process's one shared HTTP router, built by the first
// compose.Use(Router) of a composition. It holds the shared middleware and the
// health probes; feature providers such as OrdersRoutes add their routes to it
// and Server serves it.
var Router = application.Provide(func(compose *application.Composer) *router.Router {
	telemetry := compose.App().Telemetry()
	app := compose.App()

	routes := router.New()

	routes.Use(secureheaders.Middleware())
	routes.Use(telemetry.Middleware())
	routes.Use(router.Recover())
	routes.Use(csrf.Middleware())

	routes.Group("/", app.RegisterProbes)

	return routes
})
```

## Feature wiring

Each feature has one file in `internal/bootstrap` with a store provider, a service provider and one provider per surface. Bootstrap imports features; features never import bootstrap. A surface provider builds the surface from its dependencies and registers it on its target: the web provider mounts handlers on the router under the feature's prefix, the jobs provider registers handlers on the consumer, and the events provider subscribes listeners and defers the unsubscribe. A cross-feature dependency is passed here: `OrdersService` hands the catalog service to the interface `orders` defines.

`internal/bootstrap/orders.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"

	"example.com/shop/internal/orders"
	ordersjobs "example.com/shop/internal/orders/jobs"
	ordersweb "example.com/shop/internal/orders/web"
)

// OrdersStore is the process's one orders Store over the primary database.
var OrdersStore = application.Provide(func(compose *application.Composer) *orders.Store {
	return orders.NewStore(compose.Use(Database))
})

// OrdersService is the process's one orders Service. It checks stock through
// the catalog service and emits orders.Placed on the application's event bus.
var OrdersService = application.Provide(func(compose *application.Composer) *orders.Service {
	events := compose.App().Events()

	return orders.NewService(compose.Use(Database), compose.Use(OrdersStore), compose.Use(CatalogService), events)
})

// OrdersRoutes mounts the orders routes under /orders on the shared router and
// returns the handlers. Use it from the Compose of every process that serves HTTP.
var OrdersRoutes = application.Provide(func(compose *application.Composer) *ordersweb.Handlers {
	routes := compose.Use(Router)

	handlers := ordersweb.NewHandlers(compose.Use(OrdersService))
	handlers.Register(routes.Prefix("/orders"))

	return handlers
})

// OrdersJobs registers the orders job handlers on the shared consumer and
// returns them. Use it from the Compose of every process that consumes jobs.
var OrdersJobs = application.Provide(func(compose *application.Composer) *ordersjobs.Handlers {
	logger := compose.App().Logger()

	handlers := ordersjobs.NewHandlers(compose.Use(OrdersService), logger)
	compose.Require(handlers.Register(compose.Use(Consumer)))

	return handlers
})
```

`internal/bootstrap/catalog.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"

	"example.com/shop/internal/catalog"
)

// CatalogService is the process's one catalog Service. Other features' providers
// obtain it with compose.Use(CatalogService) and pass it where they need the
// small interface they define.
var CatalogService = application.Provide(func(*application.Composer) *catalog.Service {
	return catalog.NewService()
})
```

`internal/bootstrap/notifications.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"

	"example.com/shop/internal/notifications"
	notificationsevents "example.com/shop/internal/notifications/events"
)

// NotificationsService is the process's one notifications Service. It hands
// receipts to the queue through the shared dispatcher.
var NotificationsService = application.Provide(func(compose *application.Composer) *notifications.Service {
	return notifications.NewService(compose.Use(Dispatcher))
})

// NotificationsListeners subscribes the notifications listeners on the
// application's event bus and returns them. Unsubscribing is registered for
// shutdown. Use it from the Compose of the process that emits the events it
// listens to: the bus is in-process.
var NotificationsListeners = application.Provide(func(compose *application.Composer) *notificationsevents.Listeners {
	events := compose.App().Events()

	listeners := notificationsevents.NewListeners(compose.Use(NotificationsService))
	compose.MustDefer(listeners.Register(events))

	return listeners
})
```

## Identity

`arkoren add ark oauth github` writes the `GitHub` client and its `GitHubSignUp` fragment, then `arkoren add ark iam --with reset,verification,two-factor,tokens,permissions,account` writes these files, wires the client it finds, inserts `compose.Use(bootstrap.Migrate)`, `IAMRouter`, `IAMJobs` and `Work` into the server's `Compose`, and appends `DATABASE_URL`, `PASSWORD_RESET_URL`, `EMAIL_VERIFY_URL` and `GITHUB_REDIRECT_URL` to `.env` with local defaults (never a secret or a key path). Run `arkoren add ark oauth google` (or `microsoft`, `apple`, `oidc`) after the module to add another sign-in client; it inserts the `identity.Provider` call itself. The routes are mounted under `/auth`, so login is `POST /auth/login` and the GitHub callback is `GET /auth/github/callback`. The module's migrations are listed in the `Migrator` shown under Bootstrap providers, and `Migrate` applies them when the server starts. The tests in this reference compare the files below with that command's output.

The `Migrator` and `Migrate` providers in `internal/bootstrap/migrator.go`, and an empty `All` in `internal/migrations/migrations.go`, are written only when the project has no `Migrator`; with one, the command inserts `compose.Require(migrator.Add(compose.Use(IAM).Migrations()...))` into it. `Up` holds the migration lock, so replicas starting together are safe. For production, take `Migrate` out of the server's `Compose` and run the `migrate` process before the deploy.

The `IAM` provider calls `iam.New` over `Database` and `Cache`, publishes the module's events on the application's bus and registers its readiness check. Each optional feature takes the settings of one fragment provider: `PasswordReset` returns the `password.BuilderFunc` that turns on registration and reset, `EmailVerification` the `account.BuilderFunc` for verification, and so on. A fragment provider returns a construction callback instead of a component, and `IAM` resolves it with `compose.Use` inside the facade's own callback, which runs once, synchronously, during composition. Plain settings stay `os.Getenv` inside their provider; only a real need turns one into a provider of its own.

`IAMRouter` returns the shared router wrapped in the identity middleware after mounting the module's routes on it, so routes that need the signed-in user mount on it. `IAMJobs` registers the module's email jobs on the consumer: the module stores them on the `Dispatcher` from the request and sends them through the `Mailer`, and the process that lists `IAMJobs` and `Work` in its `Compose` sends the emails. With the memory queue that is the server. When the `Queue` driver becomes a networked one, move `IAMJobs` and `Work` to the worker's `Compose` and deploy the worker before enabling a new email feature. A consumer restricted with `Queues(...)` must list `iam.QueueName` (`"iam"`).

`internal/bootstrap/iam.go`

```go
package bootstrap

import (
	"arkoren.dev/ark/iam"
	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/foundation/totp"
)

// IAM is the process's one identity module: password login, the sessions and
// guard behind it, and every feature this provider enables. It reads the
// database and cache through providers and publishes its events on the
// application's bus. Each optional feature is configured by the fragment
// provider that its callback receives, so a feature's settings live in one
// small file. The module does no I/O when built; the Migrator provider adds
// IAM.Migrations and the Migrate provider applies them at startup, and the
// readiness check registered here fails until they have run.
//
// Sessions and counters live in the Cache provider. The default memory cache is
// private to this process; use a shared cache driver before running more than
// one process or replica. Behind a reverse proxy, set the client address source
// with identity.ClientIP, and keep CSRF protection on every route a session can
// reach.
var IAM = application.Provide(func(compose *application.Composer) *iam.IAM {
	events := compose.App().Events()

	identity := compose.Must(iam.New(compose.Use(Database), compose.Use(Cache), func(identity *iam.Builder) {
		identity.Events(events)
		identity.Queue(compose.Use(Dispatcher))
		identity.Password(compose.Use(Hasher), compose.Use(PasswordReset))
		identity.Directory(compose.Use(EmailVerification))
		identity.TwoFactor(totp.New("shop"), compose.Use(TwoFactorKey))
		identity.Tokens(compose.Use(TokenScopes))
		identity.Permissions(compose.Use(Roles))
		identity.Provider(compose.Use(GitHub), compose.Use(GitHubSignUp))
		identity.Account(compose.Use(AccountView))
	}))

	compose.Check("iam", identity.Ping)

	return identity
})

// IAMRouter mounts the identity routes under /auth on the shared router (so
// POST /auth/login, GET /auth/sessions, GET /auth/github/callback and so on) and
// returns the router that identifies users: it runs the session and
// authenticator middleware before its routes. Routes that need the signed-in
// user mount on this router instead of Router, and routes that must be signed in
// add auth.Require to it. List it in the Compose of every process that serves HTTP,
// before the providers whose routes need the user.
var IAMRouter = application.Provide(func(compose *application.Composer) *router.Router {
	identity := compose.Use(IAM)
	routes := compose.Use(Router)

	web := routes.With(identity.Middleware())
	web.Group("/auth", identity.Register)

	return web
})

// IAMJobs registers the identity module's background jobs on the shared
// consumer and returns it. The jobs send the module's emails (password reset
// and email verification); the module stores them on the Dispatcher provider
// from the request. List it, then Work, in the Compose of the process that sends
// the emails: the server for a single process, a worker once the memory queue
// is replaced by a networked driver. Deploy that process before enabling a new
// email feature elsewhere, and list the queue iam.QueueName in any queue
// restriction of the consumer.
var IAMJobs = application.Provide(func(compose *application.Composer) *queue.Consumer {
	identity := compose.Use(IAM)
	consumer := compose.Use(Consumer)

	compose.Require(identity.RegisterJobs(consumer))

	return consumer
})
```

`internal/bootstrap/password_reset.go`

```go
package bootstrap

import (
	"os"

	"arkoren.dev/ark/iam/password"
	"arkoren.dev/core/application"
)

// PasswordReset is the identity module's password settings: self-service
// registration and forgotten-password recovery. Reset emails go through the
// Mailer provider and carry a link signed by LinkSigner to the page at
// PASSWORD_RESET_URL, an absolute URL without a fragment, such as
// https://app.example.com/reset-password. That page reads the signed value from
// the fragment and posts it to POST /auth/password/reset. Registration with the
// built-in users requires reset, because the reset notice is how the owner of an
// address someone else registered learns of it; remove the Registration call
// when users are added another way.
var PasswordReset = application.Provide(func(compose *application.Composer) password.BuilderFunc {
	signer := compose.Use(LinkSigner)
	mailer := compose.Use(Mailer)
	resetURL := os.Getenv("PASSWORD_RESET_URL")

	return func(passwords *password.Builder) {
		passwords.Registration(true)
		passwords.Reset(signer, mailer, resetURL)
	}
})
```

`internal/bootstrap/email_verification.go`

```go
package bootstrap

import (
	"os"

	"arkoren.dev/ark/iam/account"
	"arkoren.dev/core/application"
)

// EmailVerification is the identity module's directory settings: email
// verification of the built-in users. Verification emails go through the Mailer
// provider and carry a link signed by LinkSigner to the page at
// EMAIL_VERIFY_URL, an absolute URL without a fragment, such as
// https://app.example.com/verify-email. That page reads the signed value from
// the fragment and posts it to POST /auth/email/verify. Verification never blocks
// login; protect routes that need a verified email with IAM.RequireVerified.
var EmailVerification = application.Provide(func(compose *application.Composer) account.BuilderFunc {
	signer := compose.Use(LinkSigner)
	mailer := compose.Use(Mailer)
	verifyURL := os.Getenv("EMAIL_VERIFY_URL")

	return func(accounts *account.Builder) {
		accounts.Verification(signer, mailer, verifyURL)
	}
})
```

`internal/bootstrap/token_scopes.go`

```go
package bootstrap

import (
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/core/application"
)

// TokenScopes is the identity module's token settings: the scope catalog that
// personal and device tokens may carry, and the ceiling on what a user may grant.
// AnyScope lets every user grant every listed scope; replace it with a function
// of the user (or the permission store) to cap tokens at what their owner holds.
// The scopes below are placeholders: name the scopes your routes check.
var TokenScopes = application.Provide(func(*application.Composer) apitoken.BuilderFunc {
	return func(tokens *apitoken.Builder) {
		tokens.Grants(apitoken.AnyScope, "read", "write")
	}
})
```

`internal/bootstrap/roles.go`

```go
package bootstrap

import (
	"arkoren.dev/ark/iam/permission"
	"arkoren.dev/core/application"
	"arkoren.dev/core/auth/gate"
)

// Roles is the identity module's permission settings: the roles and permissions
// that can be assigned to users. It defines one administrator role. Holders of
// the users:manage permission may grant and revoke roles through the management
// routes, never beyond what they hold, and the last administrator cannot lose
// the role. Users read their own permissions through GET /auth/me/permissions.
// Replace the placeholder with the roles and permissions of the application and
// check them in handlers with gate.HasPermission and gate.HasRole.
var Roles = application.Provide(func(*application.Composer) permission.BuilderFunc {
	manage := gate.NewPermission("users:manage")
	admin := gate.NewRole("admin", func(role *gate.RoleBuilder) {
		role.Grants(manage)
	})

	return func(policy *permission.Builder) {
		policy.Roles(admin)
		policy.KeepOne(admin)
		policy.Management(manage)
		policy.SelfService(true)
	}
})
```

`internal/bootstrap/account_view.go`

```go
package bootstrap

import (
	"arkoren.dev/ark/iam"
	"arkoren.dev/core/application"
)

// AccountView is the identity module's account settings: what GET /auth/account tells
// the signed-in user beyond the subject and email facts, which is how the request
// logged in, their permissions and their two-factor state.
// The route never encodes application data; compose a GET /me from IAM.Account
// when the profile belongs in the answer.
var AccountView = application.Provide(func(*application.Composer) iam.AccountBuilderFunc {
	return func(view *iam.AccountBuilder) {
		view.Permissions(true)
		view.TwoFactor(true)
		view.Login(true)
	}
})
```

`internal/bootstrap/github.go`

```go
package bootstrap

import (
	"os"

	"arkoren.dev/ark/oauth/oauthauth"
	"arkoren.dev/core/application"
)

// GitHub is the process's one GitHub sign-in client. It reads GITHUB_CLIENT_ID,
// GITHUB_CLIENT_SECRET and GITHUB_REDIRECT_URL (the absolute address of the
// callback route, such as https://app.example.com/auth/github/callback) from the
// environment, and seals its login cookies with the OAuthKey provider. Pass the
// client to the identity module with identity.Provider(compose.Use(GitHub)),
// which mounts its login and callback routes.
var GitHub = application.Provide(func(compose *application.Composer) *oauthauth.Client {
	cipher := compose.Use(OAuthKey)

	client := compose.Must(oauthauth.New(oauthauth.GitHub(), cipher, os.Getenv("GITHUB_CLIENT_ID"), func(client *oauthauth.ClientBuilder) {
		client.ClientSecret(os.Getenv("GITHUB_CLIENT_SECRET"))
		client.RedirectURL(os.Getenv("GITHUB_REDIRECT_URL"))
	}))
	compose.Startup(client.Start)

	return client
})
```

`internal/bootstrap/github_sign_up.go`

```go
package bootstrap

import (
	"arkoren.dev/ark/iam"
	"arkoren.dev/core/application"
)

// GitHubSignUp is the identity module's settings for the GitHub provider: it
// lets a first GitHub login create a user, when GitHub proved an email address.
// Without it the provider only logs in people the application already has.
// Remove it from the IAM provider to turn sign-up off.
var GitHubSignUp = application.Provide(func(*application.Composer) iam.ProviderBuilderFunc {
	return func(login *iam.ProviderBuilder) {
		login.SignUp(true)
	}
})
```

The shared providers the module uses, written with local defaults when `internal/bootstrap` lacks them: a SQLite `Database` and the memory `Queue`, `Dispatcher`, `Consumer` and `Work` shown above, plus these. Keys come from files that `arkoren key generate shared --out FILE` creates, one per purpose. `Mailer` logs email instead of sending it and logs bodies only in debug, which is how a developer reads a link locally.

`internal/bootstrap/hasher.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/bcrypt/bcrypthash"
)

// Hasher hashes and verifies passwords with bcrypt at its default cost. Raise
// the cost, or swap in another driver such as argon2, here without touching
// features.
var Hasher = application.Provide(func(*application.Composer) *hash.Hasher {
	return hash.New(bcrypthash.New())
})
```

`internal/bootstrap/cache.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// Cache is the process's shared cache, which holds sessions and rate-limit
// counters. The memory driver keeps entries in this process only: sessions end
// with a restart and are not shared between processes or replicas. Swap in a
// networked driver here before running more than one.
var Cache = application.Provide(func(compose *application.Composer) *cache.Cache {
	driver := memorycache.New()
	compose.Cleanup(driver.Stop)
	compose.Startup(driver.Start)

	return cache.New(driver)
})
```

`internal/bootstrap/link_signer.go`

```go
package bootstrap

import (
	"os"

	"arkoren.dev/core/application"
	"arkoren.dev/core/http/link"
	"arkoren.dev/foundation/key"
)

// LinkSigner signs the links the application emails, such as password reset and
// email verification. It loads a 32-byte shared key from the file named by
// LINK_KEY_FILE; create one with `arkoren key generate shared --out FILE`.
// Reserve the key for links: it is not an encryption key, and rotating it ends
// every link already sent. Composition fails when the file is missing or invalid,
// and the application never generates a key to replace a missing one.
var LinkSigner = application.Provide(func(compose *application.Composer) *link.Signer {
	secret := compose.Must(key.LoadShared(os.Getenv("LINK_KEY_FILE")))

	return compose.Must(link.New(secret))
})
```

`internal/bootstrap/two_factor_key.go`

```go
package bootstrap

import (
	"os"

	"arkoren.dev/core/application"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/driver/aes/aescrypto"
	"arkoren.dev/foundation/key"
)

// TwoFactorKey encrypts the stored two-factor secrets with AES. It loads a
// 32-byte shared key from the file named by TWO_FACTOR_KEY_FILE; create one with
// `arkoren key generate shared --out FILE`. Reserve the key for these secrets:
// losing or replacing it makes every enrolled authenticator unusable. Composition
// fails when the file is missing or invalid, and the application never generates
// a key to replace a missing one.
var TwoFactorKey = application.Provide(func(compose *application.Composer) *crypto.Encrypter {
	secret := compose.Must(key.LoadShared(os.Getenv("TWO_FACTOR_KEY_FILE")))
	driver := compose.Must(aescrypto.New(secret))
	compose.Cleanup(driver.Stop)

	return compose.Must(crypto.New(driver))
})
```

`internal/bootstrap/oauth_key.go`

```go
package bootstrap

import (
	"os"

	"arkoren.dev/core/application"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/driver/aes/aescrypto"
	"arkoren.dev/foundation/key"
)

// OAuthKey seals the login cookies of the OAuth sign-in clients with AES. It
// loads a 32-byte shared key from the file named by OAUTH_KEY_FILE; create one
// with `arkoren key generate shared --out FILE`. Every process and replica that
// serves the callback must load the same key, or a login started on one fails on
// another. Composition fails when the file is missing or invalid, and the
// application never generates a key to replace a missing one.
var OAuthKey = application.Provide(func(compose *application.Composer) *crypto.Encrypter {
	secret := compose.Must(key.LoadShared(os.Getenv("OAUTH_KEY_FILE")))
	driver := compose.Must(aescrypto.New(secret))
	compose.Cleanup(driver.Stop)

	return compose.Must(crypto.New(driver))
})
```

`internal/bootstrap/mailer.go`

```go
package bootstrap

import (
	"cmp"
	"os"

	"arkoren.dev/core/application"
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/slogmail"
)

// Mailer sends the application's email. This starter writes each email to the
// application log instead of delivering it: summaries only, plus the body when
// application debug is on, which is how a developer reads a reset or
// verification link locally. Bodies can carry credentials, so debug must stay
// off in production, where this driver must be replaced by a delivering one such
// as SMTP. The sender is MAIL_FROM, defaulting to a placeholder address.
var Mailer = application.Provide(func(compose *application.Composer) *mail.Mailer {
	debug := compose.App().Debug()

	driver := slogmail.New(func(driver *slogmail.Builder) {
		if debug {
			driver.Detail(slogmail.DetailBody)
		}
	})

	return mail.New(driver, func(mailer *mail.Builder) {
		mailer.From(mail.Address{Email: cmp.Or(os.Getenv("MAIL_FROM"), "noreply@example.com")})
	})
})
```

## Migrations

Each migration has its own numbered file (`arkoren add migration`). Schema changes are schema-builder operations in exported variables, never dialect SQL, and the migration only references them.

`internal/migrations/migrations.go`

```go
package migrations

import "arkoren.dev/core/service/database/migrate"

// All returns the application's own migrations in the order they run. A
// bootstrap Migrator provider adds All() together with each ark's Migrations().
// Append new migrations at the end; reordering or removing a shipped migration
// breaks databases that already applied it.
func All() []migrate.Definition {
	return []migrate.Definition{
		CreateOrders,
	}
}
```

`internal/migrations/0001_create_orders.go`

```go
package migrations

import (
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query/schema"
)

// CreateOrdersUp creates the orders table.
var CreateOrdersUp = schema.Create("orders", func(table *schema.Table) {
	table.ID()
	table.String("sku", 64)
	table.BigInteger("total")
	table.Timestamps()
})

// CreateOrdersDown reverses CreateOrdersUp and destroys the table's data.
var CreateOrdersDown = schema.Drop("orders")

// CreateOrders is the "create-orders" migration. All lists it in the order
// migrations run; the application's migrator is the only code that executes it.
var CreateOrders = migrate.NewMigration("create-orders", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Schema(CreateOrdersUp))
	migration.Down(migrate.Schema(CreateOrdersDown))
})
```

## Feature roots

The root is plain Go: it imports neither `application` nor `internal/bootstrap`, and its constructors take what they use. It declares what others listen to or dispatch, because the root cannot import its own children. Statements are built by exported query functions that `Store` methods only run; the function passed to `WithTransaction` is an exported method too. `catalog` is a second feature whose service satisfies the interface `orders` defines.

`internal/orders/orders.go`

```go
package orders

import (
	"errors"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/queue"
)

// Placement describes an order to place.
type Placement struct {
	SKU   string `json:"sku"`
	Total int64  `json:"total"`
}

// ErrOutOfStock reports an unavailable SKU. It is a plain domain error: each
// surface maps it to its own wire form.
var ErrOutOfStock = errors.New("orders: out of stock")

// Placed is emitted after an order commits. It is declared in the root so any
// package may listen to it, and the root cannot import its own children.
var Placed = event.NewTopic[Placement]("orders.placed")

// Receipt is the payload of SendReceipt.
type Receipt struct {
	SKU string `json:"sku"`
}

// SendReceipt is the job that sends a receipt. It is declared in the root so
// other features can dispatch it; the jobs child only handles it.
var SendReceipt = queue.NewJob[Receipt]("orders-send-receipt")
```

`internal/orders/store.go`

```go
package orders

import (
	"context"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// Store persists orders over the database it is given.
type Store struct {
	db *database.Database
}

// NewStore builds the store over db. It does no I/O.
func NewStore(db *database.Database) *Store {
	return &Store{db: db}
}

// InsertOrder is the statement that stores placement.
func InsertOrder(placement Placement) query.Query {
	return query.Table("orders", func(statement *query.Builder) {
		statement.Insert(map[string]any{"sku": placement.SKU, "total": placement.Total})
	})
}

// Create stores placement. It joins a transaction carried by ctx.
func (store *Store) Create(ctx context.Context, placement Placement) error {
	_, err := store.db.Exec(ctx, InsertOrder(placement))

	return err
}
```

`internal/orders/service.go`

```go
package orders

import (
	"context"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/database"
)

// Availability is what orders needs from another feature. orders defines it;
// the catalog service satisfies it and a bootstrap provider passes it in.
type Availability interface {
	Available(ctx context.Context, sku string) (bool, error)
}

// Service places orders.
type Service struct {
	db     *database.Database
	store  *Store
	stock  Availability
	events *event.Bus
}

// NewService builds the service from the values it uses. It does no I/O.
func NewService(db *database.Database, store *Store, stock Availability, events *event.Bus) *Service {
	return &Service{db: db, store: store, stock: stock, events: events}
}

// Place checks stock, then runs Record in one transaction.
func (service *Service) Place(ctx context.Context, placement Placement) error {
	available, err := service.stock.Available(ctx, placement.SKU)
	if err != nil {
		return err
	}

	if !available {
		return ErrOutOfStock
	}

	return service.db.WithTransaction(ctx, service.Record(placement))
}

// Record is the unit of work Place runs in a transaction: store the order and
// emit Placed once it commits.
func (service *Service) Record(placement Placement) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := service.store.Create(ctx, placement); err != nil {
			return err
		}

		return service.db.AfterCommit(ctx, service.Announce(placement))
	}
}

// Announce emits Placed for placement.
func (service *Service) Announce(placement Placement) func(context.Context) error {
	return func(ctx context.Context) error {
		service.events.Emit(ctx, Placed, placement)

		return nil
	}
}
```

`internal/catalog/catalog.go`

```go
package catalog

import "context"

// Service answers stock questions.
type Service struct{}

// NewService builds the catalog service.
func NewService() *Service {
	return &Service{}
}

// Available reports whether sku is in stock.
func (service *Service) Available(ctx context.Context, sku string) (bool, error) {
	return sku != "none", nil
}
```

## Web surface

A surface is a struct built by a `New` constructor with a `Register` method for its target. `Register` uses relative paths, so the provider decides the prefix. Handlers are methods that read with `request.ParseJSON` against a schema in `schemas.go` (invalid input becomes a 422 problem) and write with `response.JSON`; `problems.go` maps the root's plain domain errors to problem details for every route. The handlers take a small interface the surface defines, so a test needs no database.

`internal/orders/web/web.go`

```go
package web

import (
	"context"
	"net/http"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"example.com/shop/internal/orders"
)

// Placer is what the handlers need from the orders service. The web surface
// defines it so handlers can be tested without a database.
type Placer interface {
	Place(ctx context.Context, placement orders.Placement) error
}

// Handlers serves the orders HTTP routes. It knows no path prefix: the provider
// in internal/bootstrap/orders.go registers it on a router prefixed /orders.
type Handlers struct {
	placer Placer
}

// NewHandlers builds the handlers over placer. It does no I/O and registers nothing.
func NewHandlers(placer Placer) *Handlers {
	return &Handlers{placer: placer}
}

// Register mounts the routes on routes behind the Problems middleware, so
// domain errors become problem responses. Paths are relative to the router's
// prefix.
func (handlers *Handlers) Register(routes *router.Router) {
	routes.Use(Problems)

	routes.Post("/", handlers.Place)
}

// Place handles POST /. Invalid input becomes a 422 problem through
// request.ParseJSON; domain errors become problems through Problems.
func (handlers *Handlers) Place(writer http.ResponseWriter, r *http.Request) error {
	placement, err := request.ParseJSON(r, PlacementSchema)
	if err != nil {
		return err
	}

	if err := handlers.placer.Place(r.Context(), placement); err != nil {
		return err
	}

	return response.JSON(writer, http.StatusCreated, placement)
}
```

`internal/orders/web/schemas.go`

```go
package web

import (
	"arkoren.dev/foundation/validate"

	"example.com/shop/internal/orders"
)

// PlacementSchema validates and normalizes the POST body.
var PlacementSchema = validate.Struct(func(object *validate.StructBuilder[orders.Placement]) {
	object.Field("SKU", func(sku *validate.StringBuilder[string]) {
		sku.Trim()
		sku.Required()
		sku.Max(64)
	})
	object.Field("Total", func(total *validate.IntBuilder[int64]) {
		total.Required()
		total.Min(1)
	})
})
```

`internal/orders/web/problems.go`

```go
package web

import (
	"net/http"

	"arkoren.dev/core/http/middleware/errormap"
	"arkoren.dev/foundation/problem"

	"example.com/shop/internal/orders"
)

// OutOfStock is the problem clients receive when a SKU is unavailable.
var OutOfStock = problem.New(func(details *problem.Builder) {
	details.Type("https://shop.example/problems/out-of-stock")
	details.Title("Out of stock")
	details.Status(http.StatusConflict)
})

// Problems maps the orders domain errors to their problems for every orders route.
var Problems = errormap.Middleware(
	errormap.Map(orders.ErrOutOfStock, OutOfStock),
)
```

## Other surfaces

The jobs surface registers handlers on a `*queue.Consumer`, the events surface on the `*event.Bus`. The listener lives in the subscribing feature and is used by the `server` process, the process that emits `orders.Placed`; the bus is in-process.

`internal/orders/jobs/jobs.go`

```go
package jobs

import (
	"context"
	"log/slog"

	"arkoren.dev/core/service/queue"

	"example.com/shop/internal/orders"
)

// Handlers runs the orders queue jobs over the orders Service.
type Handlers struct {
	service *orders.Service
	logger  *slog.Logger
}

// NewHandlers builds the handlers. It does no I/O and registers nothing; call
// Register to attach them to a consumer.
func NewHandlers(service *orders.Service, logger *slog.Logger) *Handlers {
	return &Handlers{service: service, logger: logger}
}

// Register attaches the job handlers to consumer. It returns the consumer's
// error for a duplicate or invalid registration.
func (handlers *Handlers) Register(consumer *queue.Consumer) error {
	return consumer.Handle(orders.SendReceipt, handlers.SendReceipt)
}

// SendReceipt runs one orders.SendReceipt job.
func (handlers *Handlers) SendReceipt(ctx context.Context, receipt orders.Receipt) error {
	handlers.logger.InfoContext(ctx, "receipt sent", "sku", receipt.SKU)

	return nil
}
```

`internal/notifications/notifications.go`

```go
package notifications

import (
	"context"

	"arkoren.dev/core/service/queue"

	"example.com/shop/internal/orders"
)

// Service sends notifications. It depends on orders only through the root's
// exported job and payload types.
type Service struct {
	dispatcher *queue.Dispatcher
}

// NewService builds the service over dispatcher. It does no I/O.
func NewService(dispatcher *queue.Dispatcher) *Service {
	return &Service{dispatcher: dispatcher}
}

// Receipt queues the receipt for placement.
func (service *Service) Receipt(ctx context.Context, placement orders.Placement) error {
	return service.dispatcher.Dispatch(ctx, orders.SendReceipt, orders.Receipt{SKU: placement.SKU})
}
```

`internal/notifications/events/events.go`

```go
package events

import (
	"context"

	"arkoren.dev/core/event"

	"example.com/shop/internal/notifications"
	"example.com/shop/internal/orders"
)

// Listeners reacts to events of other features over the notifications Service.
type Listeners struct {
	service *notifications.Service
}

// NewListeners builds the listeners over service. It subscribes to nothing;
// call Register to subscribe them.
func NewListeners(service *notifications.Service) *Listeners {
	return &Listeners{service: service}
}

// Register subscribes the listeners on bus and returns the function that
// removes the subscription. The provider passes it to compose.MustDefer, so
// shutdown unsubscribes once.
func (listeners *Listeners) Register(bus *event.Bus) (event.Unsubscribe, error) {
	return bus.Listen(orders.Placed, listeners.Placed)
}

// Placed queues a receipt for a placed order.
func (listeners *Listeners) Placed(ctx context.Context, placement orders.Placement) error {
	return listeners.service.Receipt(ctx, placement)
}
```

## Tests

A handler test builds the surface with a fake and records a request on a plain router: no composer, no application. The process test starts the real `server.App` and `server.Compose` with `application.Start`, points the database at memory through the environment, applies the migrations through the `Migrate` provider of the server's `Compose`, and records a request on the shared router. To replace a provider's value, a test passes its own root provider to `application.Start`: it supplies the value with `compose.Supply` and then returns `compose.Use(server.App)`. The root is built first, so the supplied value exists before anything uses the provider, and the provider's own build, with its startup and cleanup, never runs. `compose.Supply` belongs to tests only; `arkoren check` flags it elsewhere.

`internal/orders/web/web_test.go`

```go
package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"

	"example.com/shop/internal/orders"
	ordersweb "example.com/shop/internal/orders/web"
)

// fakePlacer places nothing and fails for the SKU "none".
type fakePlacer struct{}

func (fakePlacer) Place(ctx context.Context, placement orders.Placement) error {
	if placement.SKU == "none" {
		return orders.ErrOutOfStock
	}

	return nil
}

func TestPlaceHandler(t *testing.T) {
	routes := router.New()
	ordersweb.NewHandlers(fakePlacer{}).Register(routes.Prefix("/orders"))

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"created", `{"sku":" tea ","total":300}`, http.StatusCreated},
		{"invalid", `{"sku":"","total":0}`, http.StatusUnprocessableEntity},
		{"out of stock", `{"sku":"none","total":300}`, http.StatusConflict},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := routes.Record(httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(test.body)))
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
		})
	}
}
```

`internal/bootstrap/server/server_test.go`

```go
package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"

	"example.com/shop/internal/bootstrap"
	"example.com/shop/internal/bootstrap/server"
)

func TestPlaceOrder(t *testing.T) {
	t.Setenv("DATABASE_URL", "file::memory:")
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")

	var routes *router.Router

	app, err := application.Start(t.Context(), server.App, server.Compose, func(compose *application.Composer) {
		routes = compose.Use(bootstrap.Router)
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	response := routes.Record(httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"sku":"tea","total":300}`)))
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
}
```

`internal/bootstrap/server/iam_test.go`

```go
package server_test

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
	"arkoren.dev/foundation/key"

	"example.com/shop/internal/bootstrap"
	"example.com/shop/internal/bootstrap/server"
)

// inbox is a mail driver that records every email in its fake and then signals
// on sent, so a test waits for the worker without sleeping.
type inbox struct {
	*mailtest.Fake
	sent chan struct{}
}

// Send records message and signals that one email arrived.
func (driver inbox) Send(ctx context.Context, message mail.Message) (mail.Receipt, error) {
	receipt, err := driver.Fake.Send(ctx, message)
	driver.sent <- struct{}{}

	return receipt, err
}

// writeKeys creates one shared key file per purpose and points the environment
// variables the providers read at them.
func writeKeys(t *testing.T) {
	t.Helper()

	for _, variable := range []string{"LINK_KEY_FILE", "TWO_FACTOR_KEY_FILE", "OAUTH_KEY_FILE"} {
		secret, err := key.GenerateShared()
		if err != nil {
			t.Fatal(err)
		}

		path := filepath.Join(t.TempDir(), "shared.key")

		if err := os.WriteFile(path, []byte(secret.Base64()+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv(variable, path)
	}
}

// TestRegistrationEmailVerifiesTheAddress registers a user, waits for the
// verification email the identity job sends, logs in and posts the link's signed
// value, as the verification page does.
func TestRegistrationEmailVerifiesTheAddress(t *testing.T) {
	writeKeys(t)

	t.Setenv("DATABASE_URL", "file::memory:")
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("PASSWORD_RESET_URL", "https://app.example.com/reset-password")
	t.Setenv("EMAIL_VERIFY_URL", "https://app.example.com/verify-email")
	t.Setenv("GITHUB_CLIENT_ID", "client")
	t.Setenv("GITHUB_CLIENT_SECRET", "secret")
	t.Setenv("GITHUB_REDIRECT_URL", "https://app.example.com/auth/github/callback")

	mailbox := inbox{Fake: mailtest.New(), sent: make(chan struct{}, 1)}

	root := application.Provide(func(compose *application.Composer) *application.Application {
		compose.Supply(bootstrap.Mailer, mail.New(mailbox, func(mailer *mail.Builder) {
			mailer.From(mail.Address{Email: "noreply@example.com"})
		}))

		return compose.Use(server.App)
	})

	var routes *router.Router

	app, err := application.Start(t.Context(), root, server.Compose, func(compose *application.Composer) {
		routes = compose.Use(bootstrap.Router)
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	post := func(path, body string, cookies []*http.Cookie) *http.Response {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")

		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}

		return routes.Record(request)
	}

	credentials := `{"email":"ada@example.com","password":"correct horse"}`

	if response := post("/auth/register", credentials, nil); response.StatusCode != http.StatusAccepted {
		t.Fatalf("register status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}

	select {
	case <-mailbox.sent:
	case <-t.Context().Done():
		t.Fatal("no verification email was sent")
	}

	var token string

	for _, word := range strings.Fields(mailbox.Sent()[0].TextBody()) {
		if encoded, found := strings.CutPrefix(word, "https://app.example.com/verify-email#"); found {
			token, err = url.QueryUnescape(encoded)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	login := post("/auth/login", credentials, nil)
	if login.StatusCode != http.StatusNoContent {
		t.Fatalf("login status = %d, want %d", login.StatusCode, http.StatusNoContent)
	}

	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		t.Fatal(err)
	}

	if response := post("/auth/email/verify", string(body), login.Cookies()); response.StatusCode != http.StatusNoContent {
		t.Fatalf("verify status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}
```

The identity test supplies a `Mailer` over `mailtest`, so no email leaves the process, and waits on a channel for the worker to send the verification email instead of sleeping. Use `mailtest` for email in every test, the memory queue or `queuetest` for jobs, and `Supply` only for values the test must control.
