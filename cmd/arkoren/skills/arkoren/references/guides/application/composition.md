---
title: Application Composition
nav: Composition
description: Construct typed dependencies once, keep ownership explicit, and select optional features before startup.
order: 20
summary:
  - Share concrete typed dependencies while keeping construction and cleanup under one owner.
  - Extract factories and actions without introducing a registry or automatic lifecycle adoption.
  - Abort failed construction and own subscriptions before later setup can fail.
  - Skip optional acquisition and routes using explicit feature, debug, or environment decisions.
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
and a factory cannot publish a result after swallowing it. Unrelated composition
panics remain programmer failures and re-panic unchanged after cleanup.

## Extract Owned Construction

If multiple executables need the same database setup, extract construction and
its lifecycle registrations together. A `Factory[T]` is a defined callback type
that publishes one typed result:

```go
func InventoryDatabase(dsn string) application.Factory[*database.Database] {
	return func(composer *application.Composer) *database.Database {
		connection := composer.Must(postgresdatabase.New(dsn))
		composer.Cleanup(connection.Stop)
		composer.Startup(connection.Start)

		return database.New(connection)
	}
}
```

The helper captures configuration; acquisition happens inside the returned
callback. Replace the direct construction with one invocation:

```go
db := composer.Use(InventoryDatabase(dsn))

mountCatalog(routes, db)
mountStock(routes, db)
```

`Use` invokes the callback synchronously and returns its result unchanged.
Calling it again creates another driver: there is no cache, cloning, registry,
or automatic lifecycle adoption. Nested factories share the enclosing context
and cleanup stack, not a separate lifecycle.

For several related outputs, return a small named struct and pass individual
fields to consumers. Avoid a global bundle of every dependency: it hides what
each consumer needs. If you use a dependency-injection container, resolve typed
values during composition and choose either the container or the application
to own each resource's startup and release—never both.

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

Use an `Action` when a helper registers work without producing a value. Given an
existing `listener *server.Server` from `arkoren.dev/core/http/server`, whose
acquiring owner already registered `listener.Stop`, this helper borrows it:

```go
func ServeInventory(listener *server.Server) application.Action {
	return func(composer *application.Composer) {
		composer.Startup(listener.Start)
		composer.Run(listener.Serve)
	}
}
```

Invoke `composer.Use(ServeInventory(listener))` after wiring routes. The action
adds startup and supervised serving, not a second cleanup owner. It returns no
application value; call `Use` as a statement and ignore its empty completion
result. Root callbacks passed directly to application `Start` or `Run` remain
ordinary `func(*application.Composer)` functions.

## Select Optional Features

If a catalog is disabled, skip its dependency and its routes together. As an
alternative to the always-on database above, assume validated configuration with
these typed fields:

```go
type CatalogSettings struct {
	Enabled bool
	DSN     string
}
```

With `catalog CatalogSettings`, construct only when selected:

```go
db, selected := composer.UseWhen(catalog.Enabled, InventoryDatabase(catalog.DSN))
if selected {
	mountCatalog(routes, db)
}
```

When disabled, the callback never runs: no pool, cleanup, or catalog routes are
created. Keep acquisition inside the callback. Go evaluates helper arguments
before selection, so passing an already opened resource does not defer acquisition.

Selectors return `(T, bool)`. A skipped factory returns zero and false; a selected
factory returns its value and true, even when zero or nil is legitimate. Test
the boolean for presence, not success. Construction errors abort composition;
they never become false. Selectors also accept actions; their result pair can
be ignored when no later branch needs it.

### Choose Selection Policy

Use a configuration boolean for a product feature. For a developer aid, choose
debug selection when operators opt in independently of deployment, or environment
selection when deployment determines policy. Given an existing
`diagnostics application.Action`, choose one of these alternatives:

```go
composer.UseDebug(diagnostics)
composer.UseIn(application.Development, diagnostics)
composer.UseInAny([]application.Environment{application.Development, application.Staging}, diagnostics)
```

`UseDebug` reads the construction-time `Debug` flag, which defaults to false;
environment and runtime log-level changes do not alter it. `UseIn` and `UseInAny`
match the single application environment exactly, including custom values.
`UseInAny` runs at most once; duplicate matches do not repeat it, and an empty
slice skips it. See [environment settings](../first-steps/configuration.md#environment-settings)
for configuration. Selection is not access control: protect
[diagnostic endpoints](../diagnostics/capture-setup.md) separately.

Call `Use` and its selectors only during synchronous composition, never from
startup, runners, cleanup, or spawned goroutines. Invalid phases panic with
`ErrInvalidPhase`. Nil or typed-nil operations panic with `ErrNilFunc` even on
skipped branches; skipping also cannot erase a recorded composition failure.
Do not retain the composer for later container callbacks.

For every composition method, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application).
