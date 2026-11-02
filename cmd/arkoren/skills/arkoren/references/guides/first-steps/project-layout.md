---
title: Project Layout
description: Grow an application into feature-owned packages while keeping dependency construction and resource ownership visible.
order: 50
summary:
  - Keep a small service small, then group related behavior into feature packages.
  - Mount feature routes without giving handlers responsibility for listeners or drivers.
  - Connect shared dependencies in one composition root and register cleanup once.
  - Choose explicit cross-feature boundaries and test them without a running listener.
---

# Project Layout

When one feature changes, its routes, rules, and persistence code should be easy
to find together. Group application behavior by feature and keep the code that
connects features in a visible composition root. Arkoren does not discover files
or require a particular directory layout.

A service with only the [Quickstart](quickstart.md) endpoint can keep its handler
in `internal/bootstrap`. Extract a package when that behavior has enough rules,
tests, or dependencies to deserve its own boundary—not to fill empty directories.

## Growing Structure

For a greeting service that later adds a catalog, an application-owned layout
could be:

```text
hello/
├── cmd/server/main.go
├── internal/
│   ├── bootstrap/
│   │   ├── app.go
│   │   ├── bootstrap.go
│   │   ├── logging.go
│   │   ├── mcp.go
│   │   ├── router.go
│   │   └── server.go
│   ├── greeting/
│   │   ├── greeting.go
│   │   ├── http.go
│   │   └── http_test.go
│   └── catalog/
│       ├── catalog.go
│       ├── store.go
│       └── store_test.go
└── go.mod
```

`cmd/server` owns process entry, signals, and exit status. `bootstrap` constructs
dependencies, mounts features, and registers lifecycle work. `greeting` and
`catalog` own application behavior. `internal` keeps these implementation packages
behind Go's internal import boundary.

The generator creates only the executable and bootstrap, not these feature
packages or tests. Add files as the application needs them. Environment loading
can remain beside construction; introduce an application settings package only
when the parsing or sharing described in [Configuration](configuration.md)
justifies one.

## Feature Ownership

A feature package owns related behavior from its incoming request to its data
operations. Put its HTTP adapters, business rules, queries, and tests together
rather than spreading them across global `handlers`, `models`, and
`repositories` folders.

That does not mean putting HTTP or SQL inside every rule. Keep calculations and
decisions in plain Go functions when they do not need I/O. Let adapters handle
request decoding and responses, and let a feature-owned store handle database
access. Dependencies should be explicit fields or arguments, not package globals.

If a feature adds jobs, events, or schema changes, keep those declarations with
the feature too. Application startup and deployment decide when to run them;
the presence of a file does not register a worker or apply a migration. Follow
[Migration Deployment](../migrations/deployment.md) for schema ownership and
rollout ordering.

## Mounting Routes

To extract the Quickstart's endpoint, move its `Greeting` type and `greet` helper
into `internal/greeting`, changing their package declaration to `package greeting`.
In that package's `http.go`, import `net/http`,
`arkoren.dev/core/http/router`, and `arkoren.dev/core/service/cache`, then expose
one route-registration function:

```go
// Routes mounts greeting endpoints using the caller's cache.
func Routes(routes *router.Router, visits *cache.Cache) {
	routes.Post("/greetings/{name}", func(w http.ResponseWriter, r *http.Request) error {
		return greet(w, r, visits)
	})
}
```

This function registers handlers; it neither constructs a listener nor starts
or stops the cache. The handler keeps the request context for cache calls.
The feature borrows a facade whose lifetime the caller owns.

Inside the existing `Router(visits)` factory callback in
`internal/bootstrap/router.go`, replace the old greeting registration with:

```go
greeting.Routes(routes, visits)
```

Import your application's package, `example.com/hello/internal/greeting`, and
remove `net/http` from `router.go` if the old closure was its only use there.
Keep middleware registration before feature routes. Remove the original helper
file from `bootstrap` only after moving its implementation, so there is one
owner and one registration of the endpoint.

## Composition Root

The composition root is where concrete dependencies meet. In the starter,
`main` passes `bootstrap.Compose` to `app.Run`; the root constructs shared
dependencies and passes them into typed factories. The Quickstart's cache wiring
stays there even after the handler moves:

```go
driver := memorycache.New()
compose.Cleanup(driver.Stop)
compose.Startup(driver.Start)
visits := cache.New(driver)

routes := compose.Use(Router(visits))
```

This fragment uses the Quickstart's `application.Composer` callback and imports
for `cache` and `memorycache`. Keep diagnostics and listener selection after it.
The acquiring owner registers cleanup once, immediately after acquisition.
Do not add another cleanup hook in every feature that receives the facade.

Factories capture dependencies when obtained; their callbacks construct objects
and mount routes when selected through `compose.Use`. Returning a value does not
memoize it, transfer cleanup ownership, or start it. Construct a shared dependency
once and pass that same value to its consumers. Split growing bootstrap code into
typed factories rather than a global service lookup. See
[Application Composition](../application/composition.md) for shared outputs and
conditional features.

## Feature Boundaries

When one feature needs an immediate answer from another, depend on the narrow
behavior it needs. For example, an application-owned checkout feature could
define this interface, with `context` imported:

```go
// Availability supplies the stock observation needed by checkout.
type Availability interface {
	Available(ctx context.Context, sku string) (bool, error)
}
```

Bootstrap can pass a catalog implementation that satisfies it without making
checkout import the whole catalog package. This observation alone does not
reserve stock; a checkout requiring an atomic reservation needs a contract that
expresses that operation instead.

If no immediate answer is needed, an [application event](../events/overview.md)
can notify another feature after a change. The in-process event bus is not a
durable cross-process handoff; use a queue when work must survive that boundary.
If two packages continually require each other's details, reconsider whether
they represent one feature rather than adding more wiring to preserve the split.

## Testing Features

Test pure rules directly. Test HTTP behavior through the feature's registered
router using request/response recorders; a listening socket is unnecessary.
Supply local drivers or fakes for the contracts the feature consumes, then use
integration tests for the backend-specific behavior those substitutes cannot
establish. [Testing Overview](../testing/overview.md) shows these boundaries.

Local infrastructure is optional application tooling, not a layout requirement.
If you add a Compose file, own its credentials, health checks, and teardown in
your project. Follow the chosen driver's setup rather than assuming every test
needs a container.

The result is still ordinary Go packages: move or combine them as the application
changes. Before deploying the assembled executable, review
[Deployment](deploying.md) for runtime configuration, probes, and shutdown.
