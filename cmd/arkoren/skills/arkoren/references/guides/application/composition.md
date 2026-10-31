---
title: Application Composition
nav: Composition
description: Share typed dependencies, give their construction one owner, and select optional features before startup.
order: 30
---

# Application Composition

## Introduction

Your HTTP handlers can share one PostgreSQL pool without looking it up in a
container or context. Construct the dependency once, register its lifecycle with
one owner, and pass its typed value to each consumer. This builds on
[lifecycle and ownership](lifecycle-and-ownership.md): composition connects
objects before startup makes them usable.

At the top of a composition function or factory callback, bind the application
handles it uses to locals such as `events`, `logger`, and `telemetry`. Follow
that group with a blank line before construction and wiring. Keep an `app` local
only when you also need application-owned operations such as probe handlers;
pass the specific handles to dependencies rather than the whole application.

## Produce one value and share it

Inside your existing composition function, assume `dsn` is your PostgreSQL
connection string and `routes` is your `*router.Router`. Your application-owned
`mountCatalog` and `mountOrders` functions both have the signature
`func(*router.Router, *database.Database)`: they register handlers that borrow
the database facade for request-time queries, without starting or stopping its
driver. Import `router` from `arkoren.dev/core/http/router`, `database` from
`arkoren.dev/core/service/database`, and `postgresdatabase` from
`arkoren.dev/driver/postgres/postgresdatabase`.

```go
connection := composer.Must(postgresdatabase.New(dsn))
composer.Cleanup(connection.Stop)
composer.Startup(connection.Start)
db := database.New(connection)

mountCatalog(routes, db)
mountOrders(routes, db)
```

Both consumers receive the same `*database.Database`; their signatures let the
compiler check the dependency without depending on PostgreSQL. The driver's
`New` validates configuration and creates a lazy pool; it does not connect.
Cleanup is registered immediately, even if
later composition fails or startup never reaches this driver. The handlers must
not query during composition: `Start` verifies connectivity later.

## Extract construction when ownership should travel with it

If several executables need the same database setup, move construction and its
lifecycle registrations together. A `Factory[T]` is a defined callback type that
receives `*application.Composer` and returns `T`. With `application` imported
from `arkoren.dev/core/application`, this helper owns the driver it constructs:

```go
func Database(dsn string) application.Factory[*database.Database] {
	return func(composer *application.Composer) *database.Database {
		connection := composer.Must(postgresdatabase.New(dsn))

		composer.Cleanup(connection.Stop)
		composer.Startup(connection.Start)

		return database.New(connection)
	}
}
```

Replace the inline construction with one `Use` call, then pass its result just
as before:

```go
db := composer.Use(Database(dsn))

mountCatalog(routes, db)
mountOrders(routes, db)
```

`Database(dsn)` captures configuration; `Use` executes its callback synchronously
and returns the concrete value unchanged. Apply the factory once to share one
pool: another `Use(Database(dsn))` constructs another driver. There is no cache,
cloning, or dependency registry. Returning a value transfers no ownership, and
nested factories share the enclosing context and cleanup stack rather than
creating a separate lifecycle.

### Stop on a construction error

The factory uses `Must` because `postgresdatabase.New` returns `(value, error)`.
On success, `Must` returns the value unchanged. On error, it aborts composition
before that driver's cleanup registration; previously registered cleanup still
runs. After cleanup, `Start` or `Run` returns the original error, joined with
cleanup or telemetry-finalization errors if necessary. Later composition and
startup do not run. Classify returned errors with `errors.Is` or `errors.As`,
because cleanup errors can be joined with the original cause.

For an operation returning only an error, use `Require`. For example, if you
already have a `*migrate.Migrator` and your application's migration definitions
as `migrations []migrate.Definition`, validate their registration during composition:

```go
composer.Require(migrator.Add(migrations...))
```

Here `migrate` is `arkoren.dev/core/service/database/migrate`. Invalid or
duplicate definitions abort composition rather than allowing startup with an
incomplete registration. `Add` does not run SQL; see
[database migrations](../database/migrations.md) for execution. If this fails
after `Database` ran, its cleanup already owns the pool.

Use `composer.Context()` when synchronous construction needs the startup
context. `Must`, `MustDefer`, and `Require` belong only in synchronous composition, even when
the error is nil. Return ordinary errors from startup, runners, and cleanup;
their [failure handling belongs to the lifecycle](lifecycle-and-ownership.md).
Do not recover the helpers' private abort: the first failure remains recorded
and a factory cannot publish a result after swallowing it. An unrelated
composition panic remains a programmer failure and re-panics after cleanup;
`Must` does not convert arbitrary panics into errors.

## Own subscriptions and infallible releases

A listener registration returns an unsubscribe function, not a context-aware
stop method. To keep it active until application cleanup and remove it on
rollback or shutdown, use `MustDefer`. Inside composition, with `application`
imported as above and an existing `onStarted func(context.Context, event.Void) error`
handler (`event` is `arkoren.dev/core/event`):

```go
events := composer.App().Events()

composer.MustDefer(events.Listen(application.Started, onStarted))
```

This listener is removed during cleanup, before `Stopped` or rollback `Failed`
is emitted. If you need those outcomes, keep the subscription until `Run` returns
as shown in [lifecycle observation](lifecycle-and-ownership.md#observe-outcomes-without-controlling-them),
rather than registering its unsubscribe with `MustDefer`.

If registration fails, composition aborts with that error and earlier releases
still run. The failed registration's release is not registered, even if non-nil;
the acquiring operation must release partial results itself before returning an
error. A successful registration must return a non-nil release.

For a release you already have, such as a resource-specific `cancel context.CancelFunc`,
register it directly:

```go
composer.Defer(cancel)
```

`Defer` accepts `func()`; use `Cleanup` for `func(context.Context) error` instead.
All releases share one reverse-order cleanup stack and run once, even when later
composition or startup fails. Release panics are handled like other cleanup
panics. Unlike Go's `defer`, neither method releases at the end of the composing
function. A nil release panics with `ErrNilFunc`.

`Defer` may also register an acquired release inside an executing startup
callback. `MustDefer` is composition-only like `Must`: during startup, handle
acquisition errors normally, then register successful releases with `Defer`.

## Package registrations that have no output

When a helper only registers work, use an `Action` instead of inventing a return
value. For an existing `listener *server.Server`, assume its acquiring owner
already registered `listener.Stop` with `Cleanup`. This action borrows that
server to register initialization and serving. Import `server` from
`arkoren.dev/core/http/server`:

```go
func Serve(listener *server.Server) application.Action {
	return func(composer *application.Composer) {
		composer.Startup(listener.Start)
		composer.Run(listener.Serve)
	}
}
```

Call `composer.Use(Serve(listener))` once, after the routes are wired. The action
adds no resource or second cleanup owner. The callback returns nothing, so call
`Use` as a statement and ignore its empty completion result. Registration
does not bind or serve immediately. Root functions passed to `app.Start` or
`app.Run` remain plain `func(*application.Composer)` callbacks, not actions.

## Select optional features before constructing them

If an HTTP feature is disabled, skip both its dependency and its routes. As an
alternative to the always-on database above, assume `cfg.CatalogEnabled` is a
boolean and `cfg.CatalogDSN` is the connection string for an optional catalog:

```go
db, selected := composer.UseWhen(cfg.CatalogEnabled, Database(cfg.CatalogDSN))

if selected {
	mountCatalog(routes, db)
}
```

When disabled, the factory callback never runs: no pool or cleanup is created,
and the branch registers no routes. Keep construction inside that callback.
Go still evaluates helper arguments before selection, so passing an already
opened resource to a helper would acquire it even on the skipped path.

For a factory, selectors return `(T, bool)`: skipped means the zero value of `T`
and `false`; selected means the callback's result and `true`, even for a valid
zero or nil result. Test the boolean for presence, not success or non-nilness.
A `Must`, `MustDefer`, or `Require` error aborts composition; it never becomes `false`.
Selectors also accept actions; call them as statements if neither the empty
completion result nor selection flag is needed.

### Choose debug or environment policy deliberately

Use a configuration boolean for a product feature. For optional developer aids,
choose debug selection when operators should opt in independently of deployment,
or environment selection when the deployment itself determines the policy.
Given an existing `diagnostics application.Action`, these are alternatives:

```go
composer.UseDebug(diagnostics)
composer.UseIn(application.Development, diagnostics)
composer.UseInAny([]application.Environment{application.Development, application.Staging}, diagnostics)
```

`UseDebug` reads the construction-time `Debug(true)` setting on
`application.Builder`, which defaults to false. Development does not enable it,
production does not disable it, and changing the runtime log level does not
change it. `UseIn` and `UseInAny` match the application's single environment
exactly, including custom values; debug mode does not affect matching.
[`UseInAny`](https://pkg.go.dev/arkoren.dev/core/application#Composer.UseInAny)
invokes the callback at most once, even if several entries match.
Selection is not access control: protect diagnostic endpoints before exposing
them remotely, following [diagnostic endpoint protection](../observability/exposing-diagnostics.md).

Call `Use`, its selectors, `Must`, `MustDefer`, and `Require` only during synchronous
composition, not in startup, runners, cleanup, or spawned goroutines; calls
outside composition panic with `ErrInvalidPhase`. For `Use` and its selectors,
nil or typed-nil operations panic with `ErrNilFunc`, even when skipped.
Skipping does not bypass a previously recorded failure.
See the [`UseWhen` contract](https://pkg.go.dev/arkoren.dev/core/application#Composer.UseWhen)
for these validation rules.

## Keep larger composition boundaries explicit

If a feature produces several related values, return a small named struct from
its factory and pass individual fields to consumers. The bundle does not change
ownership. Avoid a global bundle of every dependency: it hides what each
consumer needs.

If you already use a dependency injection container, construct and resolve it
inside synchronous composition, then pass typed values to business code.
Arkoren needs no adapter. Choose either the container or the application to
start and close each resource, never both, and do not retain the composer for
later container callbacks.

For all composition methods, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application).
A started resource is not necessarily usable now. Continue to
[health and readiness](health-and-readiness.md) to make that distinction observable.
