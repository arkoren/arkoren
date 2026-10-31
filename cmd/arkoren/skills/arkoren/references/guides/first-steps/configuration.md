---
title: Configuration
description: Configure components and native drivers with construction callbacks, preserve defaults, and translate application settings.
order: 30
---

# Configuration

## Introduction

You may need a different listen address in production or the same timeout policy
across several servers. Arkoren keeps those choices at construction: pass the
object's dependencies and required settings as arguments, then change optional
settings in a **builder callback**. The callback receives a temporary draft;
the constructor publishes an object with independent settings.

Optional settings have defaults, so configure only what your application needs.
The same pattern applies to configurable components and drivers. Definitions
such as jobs, schemas, and queries also use builders, with validation and
publication rules described in their own guides. Settings-free constructors
need no empty callback.

Core packages do not load your application settings from configuration files
or the environment. The last part of the page shows how to load them yourself
and turn them into builder settings. Some vendor drivers also support their SDK's
credential or endpoint environment variables; each driver's guide describes
those defaults.

## The shape

With an existing router `routes`, set the server's address in its construction
callback. Import `arkoren.dev/core/http/server` for this example:

```go
srv := server.New(routes, func(listener *server.Builder) {
	listener.Addr(":9090")
})
```

The handler is a positional dependency; `Addr` changes an optional setting.
Leaving it out keeps the default `:8080`. Core components do no I/O until used
or started. Native cloud constructors retain their documented SDK configuration
and credential-loading behavior.

Any setting you leave out keeps its default. The server, for example, limits how long it
waits for request headers, which protects it from clients that open
connections and send nothing. You only need callbacks for what you want to
change. Defaults do not replace application security policy: the server's default
`:8080` address listens on every interface, and routes require explicit authentication.

## Positional arguments and options

A constructor's positional arguments are the values that have no sensible
default. There are two kinds.

The first kind is **something you already have**: a handler, a driver, a
client. The server needs a handler to serve and the cache facade needs a
driver to wrap, and there is nothing either could do without one. Import
`arkoren.dev/core/service/cache` and use an existing `cache.Driver` named `driver`:

```go
store := cache.New(driver)
```

The second kind is a **required setting with no safe default**, such as a
secret, a connection string, or an API key. Inventing a default for these
would be either useless or dangerous, so they are positional too. Import
`arkoren.dev/core/http/link`; `secret` is a validated `key.Shared`, loaded using
[`key.LoadShared` or `key.ParseShared`](../keys/overview.md), containing
exactly 32 decoded bytes:

```go
signer, err := link.New(secret, func(signer *link.Builder) {
	signer.Origin("https://app.example.com")
})
```

Everything else is optional configuration, including settings that most services change,
such as the server's address. Keeping the positional list this short means
that a constructor call shows at a glance what the object fundamentally
depends on.

## Defaults and construction policies

Construct components with their defaults, then set only the values your
application needs. A reusable callback is a **construction policy**. Name one
when several components share settings:

```go
sessions := func(store *cache.Builder) {
	store.Name("sessions")
}
store := cache.New(driver, sessions)
```

Callbacks run directly once, synchronously in order on one fresh draft. Supplied
nil entries panic; expanding a nil or empty callback slice supplies no edits.
Builder methods return the same draft, so ignoring their return values is
intentional. Publication detaches owned settings and containers, so changing a
retained draft does not reconfigure the component. Live services, drivers,
loggers and function captures remain shared dependencies. Do not mutate a draft
concurrently with construction.

When storing or returning a policy, use the builder's function alias, such as
`server.BuilderFunc` or `queue.ConsumerBuilderFunc`. These are aliases, not new
defined types, so equivalent ordinary functions and slices work without
conversion. Generic builders retain their type parameters, as in
`llm.AgentBuilderFunc[Out]`.

Builders expose only the settings for their role. For example, a queue
dispatcher has telemetry settings but no consumer queue restriction. Check the
constructor's source documentation with `go doc` for its accepted builder type.

## Applying a local override

Policies apply in argument order. A later scalar setting replaces the earlier
one; crypto `Previous` instead appends fallback drivers. Mail `ReplyTo` replaces
the address list, including clearing it with no arguments. Collection settings
document whether they replace or append; do not infer that behavior from scalar
settings. To override the earlier `sessions` policy for one cache:

```go
store := cache.New(driver, sessions, func(store *cache.Builder) {
	store.Name("accounts")
})
```

The published cache reports `accounts` in telemetry. There is no `cache.Config`
constructor input. Translate your own
[environment settings](#loading-from-the-environment) into a policy. Components
that expose `Config()` use it for reporting, not construction. Shared services
and native objects are not recursively cloned.

## Building options conditionally

Builder policies and retained options are ordinary functions you can collect in a
slice. Here `cfg.Addr` is your application's string address setting,
`cfg.SlowClients` is a boolean, and `r` is an existing HTTP handler. Import
`time` for `time.Minute`, then collect callbacks and apply them in order:

```go
opts := []server.BuilderFunc{func(listener *server.Builder) {
	listener.Addr(cfg.Addr)
}}
if cfg.SlowClients {
	opts = append(opts, func(listener *server.Builder) {
		listener.ReadTimeout(time.Minute)
	})
}

srv := server.New(r, opts...)
```

The read timeout changes
only for servers constructed with the added policy; it does not change existing servers.

## Validation

Constructors validate their configuration when you call them, so a mistake
shows up when the service starts, not when the first request arrives. How a
constructor reports a problem depends on its documented contract. Queue job
settings retain the first error in `Job.Err()`; `Handle` and `Dispatch` reject
invalid jobs before effects. Callback misuse panics separately from those errors.
Likewise, a supplied nil declaration callback panics, but a nil workflow or
MCP tool handler is a domain error reported through the declaration's
normal validation path. Do not use nil callbacks to mean “no optional policy”:
omit the argument or expand an empty callback slice instead.

A constructor whose input typically comes from the environment, such as a
connection string or a secret, returns an error. A missing `DATABASE_URL` is a
deployment problem that your composition function should report clearly.
Install the [PostgreSQL driver](../database/drivers.md#postgresql), then import
`fmt`, `os`, and `arkoren.dev/driver/postgres/postgresdatabase`. Inside a
function returning an error, validate the configured connection string:

```go
driver, err := postgresdatabase.New(os.Getenv("DATABASE_URL"))
if err != nil {
	return fmt.Errorf("database: %w", err)
}
```

Middleware constructors, whose settings are usually written directly in code,
panic on an invalid value instead. An allowed CORS origin with a path, for
example, is a programming mistake that no retry can fix, and the panic happens
while the router is being built, before the service accepts any traffic.

## Options that connect packages

A few builder settings appear in many packages because they connect a package to the
rest of the service rather than tune it:

- `Events(bus)` makes the package publish its events on the application's
  [event bus](../events/overview.md). Without it, the package publishes
  nothing.
- `Telemetry(t)` makes the package record traces and metrics with the
  service's [telemetry](../observability/telemetry.md).
- `Logger(logger)` gives the package the logger to write to. Without it,
  packages that log use `slog.Default()`.

Set these methods on the component's role-specific builder when it exposes them.
Queue dispatchers and broadcast publishers expose telemetry but no event bus.
Nil events publish nothing; nil telemetry follows `telemetry.Default()` at each
operation, including later default changes.

## Loading from the environment

Start with environment reads next to component construction in
`internal/bootstrap`; a separate configuration package is not required.
The generated starter reads `HTTP_ADDR` inside the composition callback returned
by `Server(handler)`, where it constructs the listener:

```go
listener := server.New(handler, func(listener *server.Builder) {
	listener.Addr(os.Getenv("HTTP_ADDR"))
})
```

This fragment belongs inside `internal/bootstrap/server.go`'s existing callback,
with imports for `os` and `arkoren.dev/core/http/server`. Obtaining the factory
does not read the environment; composition does. An unset or empty `HTTP_ADDR`
preserves `:8080`, which listens on all interfaces. Keep the starter's development
listener on loopback because its debug diagnostics have no authentication.

Similarly, `Application()` in `internal/bootstrap/app.go` returns a construction
callback that reads `APP_DEBUG` when `application.New` applies it. Only the exact
value `true` enables debug. Keep debug disabled in production; see
[the starter's debug defaults](creating-a-project.md#local-telemetry-diagnostics-and-secrets).
Neither environment changes nor retained callbacks reconfigure existing components.

If substantial parsing or shared validated settings justify a separate loader,
extract an application-owned settings struct and loading function. Its fields
hold only the configurable values your code needs, in the types it uses.
For this optional approach, create `internal/config/config.go` with
`package config` and imports for `cmp`, `errors`, and `os`:

```go
type Config struct {
	Addr        string
	DatabaseURL string
}

func Load() (Config, error) {
	cfg := Config{Addr: cmp.Or(os.Getenv("ADDR"), ":8080"), DatabaseURL: os.Getenv("DATABASE_URL")}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	return cfg, nil
}
```

`Load` treats an unset or empty `ADDR` as `:8080` and rejects an empty
`DATABASE_URL`. A successful load does not prove the database is reachable;
the driver's `Start` performs that check.

With this optional loader, your composition root translates settings into constructor arguments and callbacks.
This step is where your names (`ADDR`, `DATABASE_URL`) meet Arkoren's settings, and it is the only place
that knows about both:

```go
cfg, err := config.Load()

driver, err := postgresdatabase.New(cfg.DatabaseURL)
srv := server.New(r, func(listener *server.Builder) {
	listener.Addr(cfg.Addr)
})
```

Import your own module's `internal/config` package (for example,
`example.com/billing/internal/config`). Here `r` is your existing router.
Register the database's lifecycle before the server's, as shown in
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md).

If you prefer a library for this, such as one that fills a struct from
environment variables using struct tags, use it in `Load`. Arkoren does not
care how the values were obtained.

## Keeping application settings separate

Keep your file or environment schema in your application. Construct a component
by translating only the settings it owns:

```go
store := cache.New(driver, func(store *cache.Builder) {
	store.Name(cfg.CacheName)
})
```

Here `cfg.CacheName` is your application's string setting. The cache applies its
own defaults; your application owns that setting and the external file format.
Some components expose a `Config()` reporting snapshot, but those records are
not constructor inputs. Check the component's API rather than assuming every
component exposes a settings getter.

> [!NOTE]
> A retained native `Config` struct is not a file format. Its
> field names and types can change between versions. If you load settings from
> a JSON or YAML file, decode the file into a struct of your own and build the
> construction callbacks from it, as in the previous section. Your file format then stays
> stable even when a package's `Config` changes.

## Declaration and driver details

Reusable definitions need the same separation between temporary configuration
and published values. Keep execution handlers separate from settings callbacks:
handlers run later, while settings callbacks run during construction. Retained
drafts cannot change published settings, but functions and captured application
state are not deep-copied.

Definitions can require settings even when their callbacks are optional. For
example, a migration needs an `Up` step, and a workflow needs a maximum duration
before registration. Follow the feature's guide for those requirements and for
when declaration errors are reported: [Migrations](../database/migrations.md),
[Workflows](../workflows/overview.md), and
[Scheduled Tasks](../scheduled-tasks/overview.md).

To create variants, reuse a construction policy and apply a later override.
Only types that document `Derive`, such as a workflow step or SQL query, support
deriving an existing definition. Per-call options, such as a workflow start
delay, affect one operation rather than the shared component.

If you already own a native client, check conversion settings and ownership
before wrapping it with `NewFrom`. For example,
[`postgresdatabase.NewFrom`](https://pkg.go.dev/arkoren.dev/driver/postgres/postgresdatabase)
uses a pool you configure with pgxpool, while
[`rediscache.NewFrom`](https://pkg.go.dev/arkoren.dev/driver/redis/rediscache) closes its supplied
client when `Stop` runs. Do not assume that wrapping means borrowing. Keep
native SDK settings on the native client unless the driver's guide explicitly
states that a construction callback applies them.

## Learn more

Each package's source documentation describes its builder settings and defaults.
Read it with `go doc` from your application's module directory. The
[`server` API reference](https://pkg.go.dev/arkoren.dev/core/http/server) is a short example to
start with.
