---
name: arkoren-structure
description: >
  Project structure and construction rules for Arkoren services: where code
  lives (cmd, internal/bootstrap, internal/<feature>, platform), how features
  are laid out, and how Provide, Use and New* wire them. Use when adding or
  moving a feature, process, provider, route, job, migration or shared package
  in a project that imports arkoren.dev, or when reviewing its layout.
license: MIT
metadata:
  go: "1.27"
  module: arkoren.dev
---

# Arkoren project structure

Follow this layout in projects created with `arkoren new`. Preserve the layout of an existing project that does not follow it; apply only the construction rules below to new code.

## Layout

```
cmd/<process>/main.go      one directory per process: server, worker, migrate
internal/bootstrap/        shared providers and application settings
internal/migrations/       the application's own ordered migrations
internal/<feature>/        one directory per feature
internal/platform/<purpose>/   shared code without external systems or feature knowledge
```

- `cmd/<process>/main.go` builds the application and runs an exported `Compose(compose *application.Composer)`. `Compose` is the explicit list of what this process runs: route registrars, `if compose.App().Debug() { ... }` diagnostics, then `bootstrap.Listen(compose)`. One binary may run several surfaces; add a directory per distinct process.
- `internal/bootstrap` holds providers for shared resources (`Router`, `Database`, `Migrator`), application settings, logging, and plain registration functions (`Listen`, `Diagnostics`). Bootstrap never imports a feature.
- `internal/migrations` exports `func All() []migrate.Definition`. A bootstrap `Migrator` provider adds `All()` and each ark's `Migrations()`. `cmd/migrate` registers one `compose.Run` that calls Up and returns; `Application.Run` then shuts down cleanly.
- `internal/platform/<purpose>` is shared code with no external systems and no feature knowledge. Compiled-in assets belong here (`platform/ui` layouts).

## Feature layout

A feature root package holds:

- `<feature>.go`: domain types and pure rules.
- `service.go`: `Service`, the feature's API.
- `store.go`: a concrete `Store` over core database and query packages. Define no repository interface; transactions travel in the context through `db.WithTransaction`.
- `feature.go`: the exported `Default<Type>` providers, for example `var DefaultService = application.Provide(NewService)`.

The root imports bootstrap, platform, core, its children below, and other feature roots only in `feature.go`, to `Use` their provider. Service code depends on interfaces its own package defines.

Children above the root import the root, and only `cmd` imports them. Alias them `<feature><surface>` in `cmd` (`welcomeweb`):

- `web/` routes, `jobs/` queue handlers, `flow/` workflows (deterministic isolation), `tools/` MCP tools, `events/` listeners, `schedules/`, `<feature>test/` fakes and fixtures.
- Each exports plain registration functions that take the composer, such as `func Routes(compose *application.Composer)`. File names inside are free.

Children below the root are imported by the root and import nothing from the feature: outbound clients (`carrier/`), generated code, shared wire types.

Start flat and promote when grown. Begin a surface as a file in the root (`web.go` with `Routes`); move it to a child package when it outgrows a file or needs import isolation.

## Construction

- Use `application.Provide` only for a value that exists exactly once per process. `compose.Use(provider)` builds it on first use and returns the same value afterwards.
- Write ordinary constructors for everything else. A fresh instance that owns lifecycle is an exported `New*(compose, args...)` that registers Startup, Cleanup, Run or Check.
- Name feature providers `Default<Type>` and bootstrap providers for their resource (`Router`, `Database`). For several singletons of one kind, declare separate provider variables that share one constructor.
- Give providers no parameters. Read settings (environment variables) inside the provider that uses them; keep no config package.
- Call `Use` on dependencies at the top of a provider, then a blank line, then construct and register lifecycle. Dependencies then start first and stop last.
- Make providers construct and register lifecycle only. Mount routes, listeners and handlers through registration functions called from `Compose`.
- Point dependencies downward and let dependents mount. Call `Use` during composition only, never inside services or handlers.
- Call lifecycle-owning bootstrap `New*` functions only from bootstrap.
- Express conditions as a plain `if`.

## Across features

- Questions: the asking feature defines a small interface; `feature.go` wires the other feature's service into it.
- Side effects: declare the event in the publishing root; listen from the subscriber's `events/` child. Use a queue when the work must survive restarts.
- A compile cycle between features means they are one feature; merge them.
- The current user comes from `core/auth` (`request.Identity`, `request.User[T]`). Only bootstrap imports `ark/iam`.

## Shared code

- Extract on the third real use; inline a helper again when it gains flags.
- Name packages for their purpose. Avoid `util`, `common`, `shared`, `helpers`, `models`, `controllers`, `services`, `handlers`, `repositories`.
