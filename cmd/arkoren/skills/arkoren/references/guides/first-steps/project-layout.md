---
title: Project Layout
description: Organize a service into processes, bootstrap providers, and plain-Go feature packages, with one visible place that wires each process.
order: 50
summary:
  - Run each process from its own cmd directory, with an App provider and a Compose function in its process root.
  - Keep internal/bootstrap to providers that return the component they make live.
  - Write features as plain Go that takes dependencies as arguments and never imports bootstrap.
  - Wire features with one bootstrap file each, and connect them through consumer-defined interfaces.
---

# Project Layout

When one feature changes, its routes, rules, and persistence code should be easy
to find together, and the code that connects features should be easy to read in
one place. Projects created with `arkoren new` follow the layout below, and the
bundled `arkoren-structure` skill gives coding agents the same rules. Arkoren
does not discover files, so an existing project with another layout keeps
working: apply the construction rules to new code and leave its structure alone.

Three rules shape the layout:

1. Features are plain Go. They never import `arkoren.dev/core/application` or
   `internal/bootstrap`, and their constructors take dependencies as arguments.
2. `internal/bootstrap` holds only providers. Each returns the component it
   makes live.
3. Each process has a root, `internal/bootstrap/<process>`, with an `App`
   provider and a `Compose` function.

## Structure

For a service that serves HTTP and runs migrations, the layout is:

```text
hello/
├── cmd/
│   ├── server/main.go
│   └── migrate/main.go
├── internal/
│   ├── bootstrap/
│   │   ├── logging.go
│   │   ├── router.go
│   │   ├── server.go
│   │   ├── mcp.go
│   │   ├── catalog.go
│   │   ├── server/
│   │   │   ├── app.go
│   │   │   └── compose.go
│   │   └── migrate/
│   │       ├── app.go
│   │       └── compose.go
│   ├── migrations/
│   ├── platform/
│   │   └── ui/
│   └── catalog/
│       ├── catalog.go
│       ├── service.go
│       ├── store.go
│       ├── web/
│       └── carrier/
└── go.mod
```

The generator creates `cmd/server` and `bootstrap` with `bootstrap/server`. Add
the rest as the application needs it, for example with `arkoren add`; an empty
directory earns nothing.

## Processes

Each process has its own `cmd/<process>/main.go`: `server`, `worker`, `migrate`.
One binary can run several surfaces, and a process that needs a different set
of them gets its own directory. `main` calls
`application.Run(ctx, server.App, server.Compose)` and maps the result to the
exit status.

The process root `internal/bootstrap/<process>` is a package named for the
process that `main` imports. `App` is a provider that builds the
[application](../application/overview.md) from its settings. `Compose` is the
explicit list of what the process runs:

```go
func Compose(compose *application.Composer) {
	compose.Use(bootstrap.CatalogRoutes)

	if compose.App().Debug() {
		compose.Use(bootstrap.MCP)
	}

	compose.Use(bootstrap.Server)
}
```

Each `Use` builds a provider on first use. Feature providers come first so
their routes exist, optional diagnostics next, and `Server` last, which serves
the router. Reading `Compose` tells you what the process does; adding a feature
to a process is one line. The conditional is a plain `if` on
`compose.App().Debug()` or `compose.App().Environment()`.

A migration process uses one provider that registers a runner applying the
application's migrations. [`application.Run`](../application/overview.md) then
shuts down cleanly once its only runner returns without error:

```go
func Compose(compose *application.Composer) {
	compose.Use(bootstrap.Migrate)
}
```

## Bootstrap

`internal/bootstrap` holds what every process shares: providers for the log
handler, the router, the server, and shared resources such as `Database` and
`Migrator`, plus one file per feature that wires it. Every exported declaration
is `var X = application.Provide(...)` that returns the component it makes live:
it may register its lifecycle, mount itself on the router, or subscribe itself
to the event bus. Bootstrap imports features. Features never import bootstrap,
so the dependency graph points one way.

The only other exported functions are value-returning constructors such as
`NewDatabase(compose, envKey)`, which several providers share and only providers
call. A bootstrap function that takes the composer and returns nothing hides
what the process runs; make it a provider instead.

Settings are environment variables read inside the provider that uses them. The
starter has no configuration package.

## Features

A feature is one directory under `internal`. Its root package holds the
feature's own code:

| File | Holds |
| --- | --- |
| `catalog.go` | Domain types, pure rules and plain domain errors. |
| `service.go` | `Service`, the feature's API, built by `NewService(store, ...)`. |
| `store.go` | A concrete `Store` over the database and query packages, built by `NewStore(db)`. |

There is no repository interface: transactions travel in the context through
`db.WithTransaction`, so a service that only takes a `ctx` joins whatever
transaction its caller opened. The root imports `platform`, core contracts, and
its own sub-packages, never `application` or `bootstrap`. It may import another
feature's root for its types.

`internal/bootstrap/catalog.go` wires the feature with `CatalogStore`,
`CatalogService`, and one provider per surface, such as `CatalogRoutes`:

```go
var CatalogRoutes = application.Provide(func(compose *application.Composer) *catalogweb.Handlers {
	routes := compose.Use(Router)

	handlers := catalogweb.NewHandlers(compose.Use(CatalogService))
	handlers.Register(routes.Prefix("/catalog"))

	return handlers
})
```

Sub-packages sit on one side of the root:

- Above the root, packages import the root, and only `internal/bootstrap` and
  tests import them: `web/` for routes, `jobs/` for queue handlers, `flow/` for
  workflows, `tools/` for MCP tools, `events/` for listeners, `schedules/`, and
  `<feature>test/` for fakes and fixtures. Each surface is a struct built by a
  `New` constructor with a `Register` method for its target, such as
  `Register(routes *router.Router)` with relative paths. Bootstrap aliases them
  `<feature><surface>`, as in `catalogweb`, and chooses the path prefix.
- Below the root, packages are imported by the root and import nothing from the
  feature: outbound clients such as `carrier/`, generated code, and shared wire
  types.

Start flat. A surface can begin as a file in the root and move into a child
package when it outgrows a file or needs import isolation.
`internal/platform/<purpose>` holds shared code that touches no external system
and knows no feature, for example compiled-in page layouts.
`internal/migrations` exports `func All() []migrate.Definition`, and a bootstrap
`Migrator` provider adds it together with each module's `Migrations()`. See
[Migration Deployment](../migrations/deployment.md) for ownership and rollout
ordering.

## Wiring

Arkoren separates two kinds of construction. See
[Application Composition](../application/composition.md) for each in detail.

- A value the process holds exactly once is a provider declared with
  `application.Provide`. Name bootstrap providers for their resource (`Router`,
  `Database`) and feature providers `<Feature>Store`, `<Feature>Service` and
  `<Feature><Surface>`. A provider takes no parameters.
- Everything else is an ordinary constructor. One that owns lifecycle is an
  exported `New*(compose, args...)` in bootstrap that registers it and returns a
  fresh instance; only providers call it.

A provider calls `Use` on its dependencies at the top, then a blank line, then
constructs and registers what makes the component live. Dependencies start first
and stop last. Call `Use` during composition only: never inside a service or
handler, which receive their dependencies as arguments.

## Feature Boundaries

When one feature needs an immediate answer from another, define the narrow
interface it needs in the asking feature. For example, checkout defines, with
`context` imported:

```go
// Availability supplies the stock observation needed by checkout.
type Availability interface {
	Available(ctx context.Context, sku string) (bool, error)
}
```

Checkout's provider in `internal/bootstrap/checkout.go` passes
`compose.Use(CatalogService)` to `checkout.NewService`, so checkout never
imports the rest of the catalog. This observation alone does not reserve stock;
a checkout requiring an atomic reservation needs a contract that expresses that
operation instead.

If no immediate answer is needed, an [application event](../events/overview.md)
can notify another feature after a change. Declare the event in the publisher's
root and listen from the subscriber's `events/` package. The in-process event bus
is not a durable cross-process handoff; use a queue when work must survive that
boundary. If two features need each other's details, they are one feature:
merge them rather than adding wiring to preserve the split.

The current user comes from `core/auth` through `request.Identity` and
`request.User[T]`. Only bootstrap imports an identity module such as `ark/iam`.

## Shared Code

Extract shared code on its third real use, and inline a helper again when it
gains flags. Name packages for their purpose: avoid `util`, `common`, `shared`,
`helpers`, `models`, `controllers`, `services`, `handlers`, and `repositories`.
Group a feature's behavior instead of spreading it across global folders by kind.

## Tooling

The CLI works with this layout. `arkoren map` lists processes, providers,
features and migrations, `arkoren add feature|process|provider|migration`
creates the files and inserts the `compose.Use` lines, and `arkoren check`
reports violations of the import and construction rules above. The generated
`make check` runs it. See [Layout Commands](../cli/layout-commands.md).

## Testing Features

Test pure rules directly. Test a surface as plain Go: build it with fakes for the
interfaces it defines, register it on a router, and record requests without a
listening socket. Test a whole process by starting it with
`application.Start(ctx, server.App, server.Compose)` against local drivers.
Supply local drivers or fakes for the contracts the feature consumes, then use
integration tests for the backend-specific behavior those substitutes cannot
establish. [Testing Overview](../testing/overview.md) shows these boundaries.

The result is still ordinary Go packages: move or combine them as the
application changes. Before deploying the assembled executable, review
[Deployment](deploying.md) for runtime configuration, probes, and shutdown.
