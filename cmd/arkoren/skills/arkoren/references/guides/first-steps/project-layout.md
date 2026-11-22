---
title: Project Layout
description: Organize a service into processes, shared bootstrap providers, and feature packages, with one visible place that wires each process.
order: 50
summary:
  - Run each process from its own cmd directory whose Compose function lists what it runs.
  - Keep shared resources as providers in bootstrap, which never imports a feature.
  - Lay out a feature as a root package with surfaces above it and clients below it.
  - Wire features with providers, registration functions, and consumer-defined interfaces.
---

# Project Layout

When one feature changes, its routes, rules, and persistence code should be easy
to find together, and the code that connects features should be easy to read in
one place. Projects created with `arkoren new` follow the layout below, and the
bundled `arkoren-structure` skill gives coding agents the same rules. Arkoren
does not discover files, so an existing project with another layout keeps
working: apply the construction rules to new code and leave its structure alone.

## Structure

For a service that serves HTTP and runs migrations, the layout is:

```text
hello/
├── cmd/
│   ├── server/main.go
│   └── migrate/main.go
├── internal/
│   ├── bootstrap/
│   │   ├── app.go
│   │   ├── logging.go
│   │   ├── router.go
│   │   ├── listener.go
│   │   └── mcp.go
│   ├── migrations/
│   ├── platform/
│   │   └── ui/
│   ├── welcome/
│   │   └── web/routes.go
│   └── catalog/
│       ├── catalog.go
│       ├── service.go
│       ├── store.go
│       ├── feature.go
│       ├── web/
│       └── carrier/
└── go.mod
```

The generator creates `cmd/server`, `bootstrap`, and the `welcome` example. Add
the rest as the application needs it; an empty directory earns nothing.

## Processes

Each process has its own `cmd/<process>/main.go`: `server`, `worker`, `migrate`.
One binary can run several surfaces, and a process that needs a different set
of them gets its own directory. `main` builds the application, runs an exported
`Compose` function, and maps the result to the exit status.

`Compose` is the explicit list of what the process runs:

```go
func Compose(compose *application.Composer) {
	welcomeweb.Routes(compose)
	catalogweb.Routes(compose)

	if compose.App().Debug() {
		bootstrap.Diagnostics(compose)
	}

	bootstrap.Listen(compose)
}
```

Registration functions that mount routes come first, then optional
diagnostics, then `Listen`, which serves the router after every route exists.
Reading `Compose` tells you what the process does; adding a feature to a process
is one line. The conditional is a plain `if` on `compose.App().Debug()` or
`compose.App().Environment()`.

A migration process registers one runner that applies the application's
migrations and returns. [`Application.Run`](../application/overview.md) then
shuts down cleanly once its only runner returns without error:

```go
func Compose(compose *application.Composer) {
	migrator := compose.Use(bootstrap.Migrator)

	compose.Run(func(ctx context.Context) error {
		return migrator.Up(ctx)
	})
}
```

## Bootstrap

`internal/bootstrap` holds what every process shares: application settings,
logging, and providers for shared resources such as `Router`, `Database`, and
`Migrator`. It also holds registration functions for infrastructure that is
not a feature, such as `Listen` and `Diagnostics`. Bootstrap never imports a
feature package. Features import bootstrap, never the reverse, so the
dependency graph points one way.

Settings are environment variables read inside the provider or function that
uses them. The starter has no configuration package.

## Features

A feature is one directory under `internal`. Its root package holds the
feature's own code:

| File | Holds |
| --- | --- |
| `catalog.go` | Domain types and pure rules. |
| `service.go` | `Service`, the feature's API. |
| `store.go` | A concrete `Store` over the database and query packages. |
| `feature.go` | Exported `Default<Type>` providers. |

There is no repository interface: transactions travel in the context through
`db.WithTransaction`, so a service that only takes a `ctx` joins whatever
transaction its caller opened. The root imports bootstrap, `platform`, core, and
its own sub-packages. It imports another feature's root only in `feature.go`, to
`Use` that feature's provider.

Sub-packages sit on one side of the root:

- Above the root, packages import the root and only `cmd` imports them:
  `web/` for routes, `jobs/` for queue handlers, `flow/` for workflows, `tools/`
  for MCP tools, `events/` for listeners, `schedules/`, and `<feature>test/` for
  fakes and fixtures. Each exports plain registration functions that take the
  composer, like `Routes(compose)`. In `cmd`, alias them `<feature><surface>`,
  as in `catalogweb`.
- Below the root, packages are imported by the root and import nothing from the
  feature: outbound clients such as `carrier/`, generated code, and shared wire
  types.

Start flat. A surface can begin as a file in the root, such as `web.go` with a
`Routes` function, and move into a child package when it outgrows a file or
needs import isolation. `internal/platform/<purpose>` holds shared code that
touches no external system and knows no feature, for example compiled-in page
layouts. `internal/migrations` exports `func All() []migrate.Definition`, and a
bootstrap `Migrator` provider adds it together with each module's
`Migrations()`. See [Migration Deployment](../migrations/deployment.md) for
ownership and rollout ordering.

## Wiring

Arkoren separates three kinds of construction. See
[Application Composition](../application/composition.md) for each in detail.

- A value the process holds exactly once is a provider declared with
  `application.Provide`. Name bootstrap providers for their resource (`Router`,
  `Database`) and feature providers `Default<Type>`, as in
  `var DefaultService = application.Provide(NewService)`. A provider takes no
  parameters.
- Everything else is an ordinary constructor. One that owns lifecycle is an
  exported `New*(compose, args...)` that registers it and returns a fresh
  instance.
- Registration, such as mounting routes, is a plain function called from
  `Compose`. Providers construct values and register lifecycle; they never mount
  routes.

A provider calls `Use` on its dependencies at the top, then a blank line, then
constructs. Dependencies start first and stop last. Call `Use` during
composition only: never inside a service or handler, which receive their
dependencies as arguments. Dependencies point downward, and dependents mount.
Call a lifecycle-owning bootstrap `New*` function only from bootstrap.

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

Checkout's `feature.go` wires the catalog's service into it, so checkout never
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

## Testing Features

Test pure rules directly. Test HTTP behavior through the feature's registered
router using request/response recorders; a listening socket is unnecessary.
Supply local drivers or fakes for the contracts the feature consumes, then use
integration tests for the backend-specific behavior those substitutes cannot
establish. [Testing Overview](../testing/overview.md) shows these boundaries.

The result is still ordinary Go packages: move or combine them as the
application changes. Before deploying the assembled executable, review
[Deployment](deploying.md) for runtime configuration, probes, and shutdown.
