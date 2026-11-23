---
name: arkoren-structure
description: >
  Where code goes in an Arkoren service and how it is wired and written:
  plain-Go feature packages with web, jobs, events and workflow surfaces,
  providers in internal/bootstrap, processes in cmd/ run from
  internal/bootstrap/<process>, migrations, queries, validation and problem
  details. Use whenever you add, move, split or review code in a project that
  imports arkoren.dev (an endpoint, background job, event listener, workflow,
  database or third-party client, migration, process or helper) or need to
  decide where something belongs. Pair with the arkoren skill for API details.
license: MIT
metadata:
  go: "1.27"
  module: arkoren.dev
---

# Arkoren project structure

A project follows this layout when it has `cmd/<process>/main.go` files and an `internal/bootstrap` package, with each process's `App` and `Compose` in `internal/bootstrap/<process>`; `arkoren new` creates it. In any other project, keep its existing layout and patterns; do not migrate it unless asked.

Verified code for every pattern below is in [references/examples.md](references/examples.md). Read it before writing a provider, feature, surface or process by hand.

## Three rules

1. **Features are plain Go.** Feature roots, their children (surfaces above, clients below) and `internal/platform` never import `arkoren.dev/core/application` or `internal/bootstrap`. Constructors take their dependencies as arguments: `NewStore(db)`, `NewService(store, ...)`.
2. **`internal/bootstrap` holds only providers.** Every exported declaration is `var X = application.Provide(...)` that returns either the component it makes live (it may register its lifecycle, mount itself on the router or subscribe itself) or a construction callback for one, a fragment provider such as `PasswordReset` returning `password.BuilderFunc`. Feature wiring lives in `internal/bootstrap/<feature>.go`. Bootstrap imports features; features never import bootstrap.
3. **A process root `internal/bootstrap/<process>/` has `app.go` and `compose.go`.** `App` is the provider holding the application's settings. `Compose` is the explicit list of `compose.Use(bootstrap.X)` calls the process runs. `cmd/<process>/main.go` runs `application.Run(ctx, <process>.App, <process>.Compose)`.

## Workflow

1. Run `arkoren map --text` to see processes, providers, features and migrations.
2. Choose the location from the table below.
3. Generate files with `arkoren add` (it never overwrites, supports `--dry-run`, and runs `go mod tidy` when a stub imports a module `go.mod` lacks), then fill in the code.
4. Run `arkoren check` (also part of `make check`) and the tests.

## Where things go

| Adding | Location | Command |
|---|---|---|
| HTTP endpoints for a feature | `internal/<feature>/web/`, provider `<Feature>Routes` | `arkoren add feature <name> --web` |
| A background job handler | `internal/<feature>/jobs/`, provider `<Feature>Jobs`, used by the worker's `Compose`; declare the job in the feature root | `--jobs --process worker` |
| A listener for another feature's event | `internal/<listener>/events/`, provider `<Listener>Listeners`, used by the `Compose` of the process that emits the event | `--events` |
| A workflow, MCP tool or schedule | `flow/`, `tools/`, `schedules/` children of the feature | `--flow`, `--tools`, `--schedules` |
| Domain types, rules, a service or queries | the feature root: `<feature>.go`, `service.go`, `store.go` | `arkoren add feature <name>` |
| A database, cache, queue, secret store or SDK client shared by features | `internal/bootstrap/<name>.go` provider | `arkoren add provider <name>` |
| Users, login, registration, password reset, email verification, two-factor, API tokens, roles (`ark/iam`) | `internal/bootstrap/iam.go` (`IAM`, `IAMRouter`, `IAMJobs`) with one fragment provider per feature | `arkoren add ark iam --with reset,verification,two-factor,tokens,permissions,account,google,github [--process worker]` |
| Sign in with an external provider (`ark/oauth`) | `internal/bootstrap/<provider>.go` client provider, passed to `identity.Provider` in `IAM` | `arkoren add ark oauth google` (or `github`) |
| An outbound API client used by one feature | a child below that feature, such as `internal/<feature>/carrier/` | none |
| The same client needed by a second feature | promote it to its own feature that exposes a `Service` | none |
| A schema change | `internal/migrations/`, listed in `All()` | `arkoren add migration <name>` |
| A separate binary (worker, migrate, admin) | `cmd/<process>/main.go` and the process root `internal/bootstrap/<process>/` | `arkoren add process <name>` |
| Code needed by three or more features, with no I/O | `internal/platform/<purpose>/` | none |
| Templates and static files | embedded in the feature's `web/`; shared layouts in `internal/platform/ui` | none |

## Layout

```
cmd/<process>/main.go          runs application.Run with the process root's App and Compose
internal/bootstrap/            providers only: shared resources and feature wiring
internal/bootstrap/<process>/  app.go (App) and compose.go (Compose) of one process
internal/migrations/           the application's ordered migrations
internal/<feature>/            plain-Go root, with child packages for surfaces and clients
internal/platform/<purpose>/   shared code without external systems or feature knowledge
```

- `Compose` lists `compose.Use` calls in order: feature providers first, diagnostics under `if compose.App().Debug()`, and the provider that runs the process last (`bootstrap.Server` for HTTP, `bootstrap.Work` for a worker's consumer). Each process uses only what it runs; a worker has no listener.
- `Compose` lives in `internal/bootstrap/<process>` because it imports bootstrap providers, while `main` imports only that package.
- `internal/migrations` exports `All() []migrate.Definition`. A bootstrap `Migrator` provider adds `All()` and each ark's `Migrations()`; the migrate process uses a provider that runs `Up` with `compose.Run`, and the process exits when it returns.

## Writing the code

Each rule keeps one concern in one named, exported declaration that tests and readers can find. The examples show every one.

- **Migrations** always use the schema builder (`foundation/query/schema`), never dialect SQL. Declare each operation as an exported variable beside the migration (`var CreateOrdersUp = schema.Create(...)`, `var CreateOrdersDown = schema.Drop(...)`) and have `migrate.NewMigration` only reference them through `migrate.Schema`.
- **Queries** always use the query builder (`foundation/query`), in an exported function taking the statement's input (`func InsertOrder(placement Placement) query.Query`), or a variable when it takes none. Store methods only run them with `db.Exec`, `db.Select` or `db.Find`.
- **Transactions**: pass `db.WithTransaction` an exported method that returns the transaction body (`service.Record(placement)`), not an inline closure; likewise for `db.AfterCommit`.
- **Surfaces** are structs built by `New<Surface>(deps)` with a `Register(target)` method: web `Register(routes *router.Router)` with relative paths, events `Register(bus *event.Bus) (event.Unsubscribe, error)`, jobs `Register(consumer *queue.Consumer) error`, schedules `Register(scheduler *schedule.Scheduler) error`. Handlers and middleware that need dependencies are methods on the struct.
- **Handlers** read input only through `core/http/request` (`request.ParseJSON(r, Schema)` for a body, `request.Param`, `request.Query`) and write through `core/http/response` (`response.JSON`); never decode or encode by hand.
- **Validation**: declare each `validate` schema as an exported variable in the surface's `schemas.go` (`var PlacementSchema = validate.Struct(...)`). `request.ParseJSON` turns a failure into a 422 problem.
- **Errors**: the feature root declares plain domain errors (`var ErrOutOfStock = errors.New(...)`). The web surface maps them to problem details in `problems.go`: one exported `problem.New` variable per problem and one exported `Problems = errormap.Middleware(errormap.Map(...))` that `Register` applies with `routes.Use(Problems)`. Handlers just return the error.

## Feature packages

The feature root package holds the parts other code depends on:

- `<feature>.go`: domain types, pure rules, and the event topics and jobs the feature emits or dispatches. Declare them here because the root cannot import its own children.
- `service.go`: `Service`, the feature's API, built by `NewService(store, ...)`. Surfaces and other features call it and never reach the `Store`.
- `store.go`: a concrete `Store` over the core database and query packages, built by `NewStore(db)`. Add no repository interface: transactions travel in the context through `db.WithTransaction`, so stores compose without one.

Children **above** the root import it and are imported only by `internal/bootstrap` (its providers and process roots) and tests. `internal/bootstrap/<feature>.go` aliases them `<feature><surface>`, such as `ordersweb`.

Children **below** the root are imported by the root and import nothing from the feature or bootstrap: outbound clients, generated code, wire types. That keeps them reusable when the client is later promoted.

Start flat: a small surface may begin as a file in the root and move to a child when it outgrows a file or needs import isolation, such as deterministic workflow code. Wire it with a provider all the same.

## Construction

- **Features take dependencies as arguments.** `orders.NewService(db, store, stock, events)` receives what it uses; it does not know where the values come from, so a test builds it with fakes.
- **Bootstrap providers wire.** A provider `Use`s its dependencies first, then constructs the component and registers what makes it live. `compose.Use(provider)` builds the value on first use and returns it unchanged afterwards, so a provider is for something that exists once per process.
- **Activating providers register their component.** The web provider builds the handlers and calls `handlers.Register(compose.Use(Router).Prefix("/orders"))`; the events provider calls `compose.MustDefer(listeners.Register(compose.App().Events()))`; a runner provider calls `compose.Run`. The provider returns the component, so `Compose` is only a list of `Use` calls. A bootstrap function that takes the composer and returns nothing is a mistake.
- **The path prefix belongs to the provider**, not the surface: the surface registers relative paths.
- **Parameterized constructors are the one exception to "only providers".** For several singletons of one kind, declare one provider variable each and share a value-returning `NewDatabase(compose, "REPORTING_DATABASE_URL")`. Only providers call it, because each call opens a new resource.
- Name bootstrap providers for their resource (`Database`, `Router`) and feature providers `<Feature>Store`, `<Feature>Service` and `<Feature><Surface>` (`OrdersRoutes`, `NotificationsListeners`).
- Read settings with `os.Getenv` inside the provider that needs them; there is no config package. Do not turn plain settings such as `DATABASE_URL` into providers by default; do it only when a real need appears.
- **Extract a facade's sub-builder configuration into fragment providers.** When a facade callback grows (`iam.New(db, store, func(identity *iam.Builder) { ... })`), move each sub-builder's settings into a provider that returns the subpackage's `BuilderFunc` (`var PasswordReset = application.Provide(... password.BuilderFunc ...)`), and resolve it with `compose.Use` inside the facade's callback: `identity.Password(compose.Use(Hasher), compose.Use(PasswordReset))`. A construction callback (a function literal taking `*pkg.XBuilder` of an `arkoren.dev` package) runs once, synchronously, while the constructor builds, so `compose.Use` there is still composition. Resolve everything else first and bind the app handles at the top (`events := compose.App().Events()`).
- Call `compose.Use` only while composing: in providers, a `New*(compose, ...)` constructor and `Compose`, never in handlers, services or other feature code. Express conditions as a plain `if`, such as `if compose.App().Debug()`.

## Across features

- To ask another feature something, define a small interface in your feature, take it as a constructor argument, and pass the other feature's service from your provider: `orders.NewService(db, store, compose.Use(CatalogService), events)`.
- To react to something, emit a topic declared in the publishing root, after commit (`db.AfterCommit`), with `events.Emit`. Subscribers listen from their `events/` child. The application event bus is in-process: use the listeners provider in the process that emits, and hand durable or slow work to a queue job.
- A compile cycle between two features means they are one feature; merge them.
- Read the current user through `core/auth` (`request.Identity`, `request.User[T]`); only bootstrap imports `ark/iam`.

## Testing

- Test pure rules in `<feature>.go` directly.
- Test a surface as plain Go: build it with fakes for the interfaces it defines, register it on `router.New().Prefix(...)`, and call `routes.Record(request)`. No composer is needed.
- Test a whole process with `application.Start(ctx, server.App, server.Compose, extra...)`. Point providers at local drivers through the environment (`DATABASE_URL=file::memory:` for SQLite) and run `compose.Use(bootstrap.Migrator).Up` in a `Startup` callback. Shut the application down in `t.Cleanup` with `context.Background()`, since the test context is already canceled there.
- Replace a provider's value in a process test with a **test root provider**: pass `application.Provide(...)` to `application.Start` instead of `server.App`; it calls `compose.Supply(bootstrap.Mailer, mail.New(mailtest.New()))` for each value to replace and then returns `compose.Use(server.App)`. The root is built first, so `Supply` precedes every `Use`, and the supplied provider's own build (with its startup and cleanup) never runs. `Supply` panics after the provider is built or while it is building, and `arkoren check` allows it only in `_test.go` files.
- Email in tests goes through `core/service/mail/mailtest` (`mailtest.New()` as the mailer's driver, `AssertSent`, `Sent()`), jobs through the memory queue or `queuetest`. The identity module sends its emails from queue jobs: wait for the worker without sleeping (a mail driver that signals a channel, or `queuetest.Fake.Drain`). `examples.md` has a complete identity process test.
- Put fakes shared by other packages' tests in `internal/<feature>/<feature>test/`.

## Common mistakes

- A feature importing `arkoren.dev/core/application` or `internal/bootstrap`: take the value as a constructor argument and wire it in a bootstrap provider.
- A `Listen`-style bootstrap function that takes the composer and returns nothing: make it `var Listen = application.Provide(...)` that returns the component, and `compose.Use` it.
- `compose.Use` in a handler, service method or any feature code: obtain the value in the provider and pass it in.
- A hard-coded path prefix inside a surface: register relative paths and let the provider pass `routes.Prefix("/orders")`.
- Feature routes, handlers or services inside `internal/bootstrap`: move them to the feature and wire them with a provider.
- A struct collecting every resource or service: use one provider per value instead.
- One parameterized provider for several instances: declare separate provider variables over a shared `New*(compose, ...)` constructor.
- A job or topic declared in a `jobs/` or `events/` child while the root dispatches it: move the declaration to the root.
- A listener provider used only by a process that never emits the event.
- A fragment provider that returns a value other than a construction callback or component, or `compose.Use` of a fragment provider outside the facade's callback: resolve it inside `iam.New(..., func(identity *iam.Builder) { ... })`.
- `compose.Supply` in a provider or `Compose`: it replaces a value and belongs in a test root provider.
- Enabling password reset or email verification without `identity.Queue(...)`, forgetting `IAMJobs` and `Work` in the process that sends the emails, or restricting the consumer's `Queues(...)` without `iam.QueueName` ("iam"): the module fails construction or its jobs wait unconsumed. Add `compose.Use(IAM).Migrations()` to the `Migrator`.
- `Compose` or `App` written in `main.go` or in `internal/bootstrap` itself: put them in `internal/bootstrap/<process>`.
- Raw SQL, inline schema operations in `NewMigration`, an inline closure in `WithTransaction`, `json.NewDecoder` or `http.Error` in a handler, or a schema or problem declared inside a handler: extract each into its exported declaration as above.
- Packages named `util`, `common`, `shared`, `helpers`, `models`, `controllers`, `services`, `handlers` or `repositories`: name packages for their purpose, extract shared code on its third real use, and inline a helper again once it grows flags.
- One feature importing another feature's `web/` or `jobs/` child, or any feature importing `arkoren.dev/driver/...`: depend on the other feature's root, and on drivers only through bootstrap providers.

`arkoren check` enforces the mechanical rules: `feature-imports-application`, `bootstrap-action-func`, `surface-imported-outside-bootstrap`, `below-child-imports-feature`, `feature-imports-driver` (drivers and `ark/iam`), `banned-package-name`, `composer-use-outside-composition` (construction callbacks, function literals taking `*pkg.XBuilder` of an `arkoren.dev` package, are inside composition), `composer-supply-outside-tests` and `bootstrap-new-outside-bootstrap`. Beyond that exemption it has none; if a rule blocks something intentional, explain the case to the user instead of working around it.
