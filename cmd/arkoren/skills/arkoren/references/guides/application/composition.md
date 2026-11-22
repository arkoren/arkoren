---
title: Application Composition
nav: Composition
description: Construct typed dependencies, share one-per-process values with providers, and select optional features before startup.
order: 20
summary:
  - Share concrete typed dependencies while keeping construction and cleanup under one owner.
  - Declare values that must exist once per process as memoized providers, and write ordinary constructors for everything else.
  - Build the application itself from a provider so its settings come from ordinary providers.
  - Abort failed construction and own subscriptions before later setup can fail.
  - Skip optional acquisition and routes with a plain Go if on configuration, debug, or environment.
---

# Application Composition

When catalog and stock handlers use the same database, construct one facade and
pass it to both. Composition is the synchronous code that connects these typed
values and registers their lifecycle; it is not a container that resolves
dependencies later. This page assumes an [application coordinator](overview.md)
and an existing router. Startup execution and resource release belong to
[Lifecycle and Ownership](lifecycle-and-ownership.md).

## Share Typed Dependencies

Inside your composition callback, `composer` is a `*application.Composer` from
`arkoren.dev/core/application`. Assume `dsn` is your configured PostgreSQL
connection string and `routes` is a `*router.Router` from
`arkoren.dev/core/http/router`. Import `database` from
`arkoren.dev/core/service/database` and `postgresdatabase` from
`arkoren.dev/driver/postgres/postgresdatabase`.

Construct the driver and immediately register its release, before any later
construction can fail:

```go
connection := composer.Must(postgresdatabase.New(dsn))
composer.Cleanup(connection.Stop)
composer.Startup(connection.Start)

db := database.New(connection)
```

`New` validates configuration and creates a lazy pool without network I/O.
`Start` verifies connectivity later. The facade does not start or stop its driver;
the acquiring composition code owns that resource. Do not issue queries during
composition when connectivity has not been established.

Your application-owned `mountCatalog` and `mountStock` functions each accept
`(*router.Router, *database.Database)`. They register handlers that borrow the
facade without managing its lifecycle:

```go
mountCatalog(routes, db)
mountStock(routes, db)
```

Both receive the same concrete facade. Their signatures make the dependency
visible to the compiler without tying handlers to PostgreSQL. Returning or
passing a value transfers no cleanup ownership.

## Stop Failed Construction

`Must` unwraps a successful `(value, error)` result. On error it aborts composition:
later construction and every startup callback are skipped. Earlier registered
cleanup runs before `Start` or `Run` returns the original error, joined with
cleanup or telemetry-finalization errors if needed. Use `errors.Is` or `errors.As`
to classify the returned cause.

This ordering matters when a server's startup registration precedes another
constructor: an invalid later constructor fails first, because no startup
callback runs until all composition succeeds. Cleanup follows registration
position, not the number of successful startup callbacks.

For an operation returning only an error, use `Require`. With an existing
`migrator *migrate.Migrator` and `definitions []migrate.Definition`, both from
`arkoren.dev/core/service/database/migrate`, validate registration during composition:

```go
composer.Require(migrator.Add(definitions...))
```

Invalid or duplicate definitions abort construction; `Add` executes no SQL.
[Migrations](../migrations/overview.md) owns execution and deployment policy.
Use `composer.Context()` if synchronous construction needs the startup context.

`Must`, `MustDefer`, and `Require` are valid only during synchronous composition,
even for successful results. Return ordinary errors from startup, runners, and
cleanup. Do not recover their private abort: the first failure stays recorded,
and a provider cannot cache a result after swallowing it. Unrelated composition
panics remain programmer failures and re-panic unchanged after cleanup.

## Share One Value

If the process must hold exactly one database, declare it as a provider. A
provider is a package-level value created with `application.Provide`; its build
function constructs the value and registers its lifecycle:

```go
var InventoryDatabase = application.Provide(func(composer *application.Composer) *database.Database {
	connection := composer.Must(postgresdatabase.New(os.Getenv("INVENTORY_DSN")))
	composer.Cleanup(connection.Stop)
	composer.Startup(connection.Start)

	return database.New(connection)
})
```

Replace the direct construction with one `Use`:

```go
db := composer.Use(InventoryDatabase)

mountCatalog(routes, db)
mountStock(routes, db)
```

The first `Use` in a composition runs the build function synchronously and caches
the result. Later `Use` calls, including from other providers, return that same
value, so two features that both ask for `InventoryDatabase` share one pool and
one cleanup registration. The cache belongs to one `Start` or `Run`: separate
applications never share values. Declare a provider with no parameters and read
settings inside its build function, because the provider is built once.

A provider that uses other providers calls `Use` on them first, then registers
its own lifecycle. Startup runs in registration order and cleanup in reverse, so
dependencies start first and stop last:

```go
var Inventory = application.Provide(func(composer *application.Composer) *inventory.Service {
	db := composer.Use(InventoryDatabase)

	composer.Startup(warmCache)

	return inventory.NewService(db)
})
```

A provider that is used again while it is still building panics with
`ErrProviderCycle`, naming the chain of value types. Break the cycle by moving
the shared piece into a third provider. A nil build function panics with
`ErrNilFunc` at `Provide`. A build that aborts through `Must` or `Require` fails
composition and caches nothing.

## Construct Everything Else

Use `Provide` only for values that must exist once. For anything else, write an
ordinary Go constructor. A constructor that acquires resources takes the composer,
registers its lifecycle, and returns a fresh instance each time; name it
`New<Thing>(composer, args...)`:

```go
func NewWorker(composer *application.Composer, queue string) *Worker {
	worker := &Worker{queue: queue}

	composer.Startup(worker.Start)
	composer.Cleanup(worker.Stop)

	return worker
}
```

Calling `NewWorker` twice creates two workers with two lifecycles. Values that
need no lifecycle are plain constructors without the composer. For several
named singletons of one kind, declare separate providers that share one
constructor. For related outputs, return a small named struct and pass
individual fields to consumers; avoid a global bundle of every dependency. If
you use a dependency-injection container, resolve typed values during
composition and choose either the container or the application to own each
resource's startup and release, never both.

## Own Subscriptions

An event subscription returns an unsubscribe function rather than a fallible
stop method. At the top of composition, obtain the specific event handle, then
use `MustDefer` to acquire and own the subscription. Here `onStarted` is your
existing `func(context.Context, event.Void) error` callback, with `event` from
`arkoren.dev/core/event`:

```go
events := composer.App().Events()

composer.MustDefer(events.Listen(application.Started, onStarted))
```

The subscription lasts until rollback or shutdown cleanup, not until the
composition function returns. A failed acquisition registers no release, even
if it returns one: the acquiring operation must clean up partial results itself.
A successful acquisition must return a nonnil release.

For an already acquired `cancel context.CancelFunc`, use `composer.Defer(cancel)`.
`Defer` accepts `func()`; `Cleanup` accepts `func(context.Context) error`. They
share one once-only, reverse-registration-order stack. Nil releases panic with
`ErrNilFunc`; release panics follow cleanup's panic-to-error handling.

Cleanup removes this listener before `Stopped` or rollback `Failed`. Keep
subscriptions needed for those events outside the cleanup stack, as shown in
[outcome observation](lifecycle-and-ownership.md#observe-outcomes).
`Defer` and `Cleanup` can also register releases inside an executing startup
callback; `MustDefer` cannot.

## Package Registration

A package that adds routes or background work exposes a plain function that
takes the composer. It obtains the shared providers it needs with `Use` and
registers its work. Given an existing `listener *server.Server` from
`arkoren.dev/core/http/server`, whose acquiring owner already registered
`listener.Stop`, this function borrows it:

```go
func ServeInventory(composer *application.Composer, listener *server.Server) {
	composer.Startup(listener.Start)
	composer.Run(listener.Serve)
}
```

Call `ServeInventory(composer, listener)` after wiring routes. It adds startup
and supervised serving, not a second cleanup owner. Root callbacks passed
directly to application `Start` or `Run` are ordinary `func(*application.Composer)`
functions, so a registration function with only the composer parameter can be
passed to them as is.

## Build the Application from a Provider

The application itself is a provider. Declare it with `Provide`, take its
handles from ordinary providers, and let `application.Run` build it, compose the
process and run it. With your own `LogHandler` provider (a
`func(slog.Leveler) slog.Handler` factory) and `Telemetry` provider (a
`*telemetry.Telemetry`):

```go
var App = application.Provide(func(compose *application.Composer) *application.Application {
	return application.New(func(app *application.Builder) {
		app.Name("shop")
		app.Debug(os.Getenv("APP_DEBUG") == "true")
		app.LogHandler(compose.Use(LogHandler))
		app.Telemetry(compose.Use(Telemetry))
	})
})

func Compose(compose *application.Composer) {
	compose.Use(OrdersRoutes)

	if compose.App().Debug() {
		compose.Use(MCP)
	}

	compose.Use(Server)
}
```

The entry point calls `application.Run(ctx, App, Compose)`. `application.Start`
takes the same arguments and returns the started application, so the caller can
`Wait` and `Shutdown` it. A nil provider panics with `ErrNilFunc`.

`Run` uses one composer for the whole call. It uses `App` first, so a provider
the settings depend on is built like any other and its value is the same one
`Compose` gets from a later `Use`. Cleanup registered by those providers belongs
to the application: it runs at shutdown and when composition or startup fails,
in reverse registration order. Build one fresh application on every call; a
provider that returns an application that already started fails with
`ErrInvalidPhase`, and one that returns nil fails with `ErrNilApplication`.

`composer.App()` exists only after that build. Calling it from a provider the
application depends on panics with `ErrProviderCycle`, naming the chain, because
the application cannot depend on itself. Read settings there from the
environment or from another provider instead. If building the application
fails through `Must` or `Require`, `Run` runs the registered cleanup, returns
the error and reports it once on standard error, because no application logger
exists yet.

To run an application you construct yourself, as in tests or a library, call
`application.New` and its `Start` or `Run` methods with the same `Compose`
callbacks.

## Select Optional Features

If a catalog is disabled, skip its dependency and its routes together with an
ordinary `if`. Assume validated configuration with these typed fields:

```go
type CatalogSettings struct {
	Enabled bool
	DSN     string
}
```

With `catalog CatalogSettings`, construct only when selected:

```go
if catalog.Enabled {
	db := composer.Use(InventoryDatabase)

	mountCatalog(routes, db)
}
```

When disabled, `Use` never runs: no pool, cleanup, or catalog routes are created.
Keep acquisition inside the branch. Go evaluates arguments before the call, so
passing an already opened resource does not defer acquisition. Construction
errors raised through `Must` or `Require` abort composition.

### Choose Selection Policy

Use a configuration boolean for a product feature. For a developer aid, choose
debug selection when operators opt in independently of deployment, or environment
selection when deployment determines policy. Read both from the application:

```go
app := composer.App()

if app.Debug() {
	diagnostics(composer)
}

if app.Environment() == application.Development {
	devTools(composer)
}
```

`Debug` is the construction-time flag, which defaults to false; environment and
runtime log-level changes do not alter it. `Environment` returns the single
deployment name, and custom values compare exactly. See
[environment settings](../first-steps/configuration.md#environment-settings)
for configuration. Selection is not access control: protect
[diagnostic endpoints](../diagnostics/capture-setup.md) separately.

Call `Use` only during synchronous composition, never from startup, runners,
cleanup, services, handlers, or spawned goroutines. Invalid phases panic with
`ErrInvalidPhase`; a nil provider panics with `ErrNilFunc`. Do not retain the
composer for later container callbacks.

For every composition method, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application).
