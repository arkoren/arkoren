---
name: arkoren-structure
description: >
  Where code goes in an Arkoren service and how it is wired and written:
  processes in cmd/ composed in internal/bootstrap/<process>, shared resources
  in internal/bootstrap, one package per feature with web, jobs, events and
  workflow surfaces, migrations, queries, validation and problem details. Use
  whenever you add, move, split or review code in a project that imports
  arkoren.dev (an endpoint, background job, event listener, workflow, database
  or third-party client, migration, process or helper) or need to decide where
  something belongs. Pair with the arkoren skill for API details.
license: MIT
metadata:
  go: "1.27"
  module: arkoren.dev
---

# Arkoren project structure

A project follows this layout when it has `cmd/<process>/main.go` files and an `internal/bootstrap` package, with each process's `Compose` in `internal/bootstrap/<process>` (projects generated before that move keep `Compose` in `main.go`, which the commands still accept); `arkoren new` creates it. In any other project, keep its existing layout and patterns; do not migrate it unless asked.

Verified code for every pattern below is in [references/examples.md](references/examples.md). Read it before writing a provider, feature, surface or process by hand.

## Workflow

1. Run `arkoren map --text` to see processes, providers, features and migrations.
2. Choose the location from the table below.
3. Generate files with `arkoren add` (it never overwrites and supports `--dry-run`), run `go mod tidy` because the stubs import foundation modules, then fill in the code.
4. Run `arkoren check` (also part of `make check`) and the tests.

## Where things go

| Adding | Location | Command |
|---|---|---|
| HTTP endpoints for a feature | `internal/<feature>/web/`, called from the server's `Compose` | `arkoren add feature <name> --web` |
| A background job handler | `internal/<feature>/jobs/`, called from the worker's `Compose`; declare the job in the feature root | `--jobs --process worker` |
| A listener for another feature's event | `internal/<listener>/events/`, called from the `Compose` of the process that emits the event | `--events` |
| A workflow, MCP tool or schedule | `flow/`, `tools/`, `schedules/` children of the feature | `--flow`, `--tools`, `--schedules` |
| Domain types, rules, a service or queries | the feature root: `<feature>.go`, `service.go`, `store.go` | `arkoren add feature <name>` |
| A database, cache, queue, secret store or SDK client shared by features | `internal/bootstrap/<name>.go` provider | `arkoren add provider <name>` |
| An outbound API client used by one feature | a child below that feature, such as `internal/<feature>/carrier/` | — |
| The same client needed by a second feature | promote it to its own feature that exposes a `Service` | — |
| A schema change | `internal/migrations/`, listed in `All()` | `arkoren add migration <name>` |
| A separate binary (worker, migrate, admin) | `cmd/<process>/main.go` running the `Compose` of `internal/bootstrap/<process>` | `arkoren add process <name>` |
| Code needed by three or more features, with no I/O | `internal/platform/<purpose>/` | — |
| Templates and static files | embedded in the feature's `web/`; shared layouts in `internal/platform/ui` | — |

## Layout

```
cmd/<process>/main.go          builds the application and runs the process's Compose
internal/bootstrap/            shared providers, settings and process-level registration
internal/bootstrap/<process>/  Compose: the explicit list of what this process runs
internal/migrations/           the application's ordered migrations
internal/<feature>/            one package per feature, with child packages for surfaces
internal/platform/<purpose>/   shared code without external systems or feature knowledge
```

- `Compose` calls registration functions in order: feature surfaces first, then process-level runners: the generated `bootstrap.Listen` serves HTTP, and a worker ends with a consumer runner such as `bootstrap.Work` in the examples. Each process registers only what it runs; a worker has no listener.
- `internal/bootstrap` never imports a feature. Features import bootstrap to use shared providers, so the reverse import would be a cycle. That is why `Compose` lives in the `internal/bootstrap/<process>` subpackage: it may import feature surfaces and bootstrap alike, and `main` imports it.
- `internal/migrations` exports `All() []migrate.Definition`. A bootstrap `Migrator` provider adds `All()` and each ark's `Migrations()`; the migrate process's `Compose` calls a registration function that runs `Up` with `compose.Run`, and the process exits when it returns.

## Writing the code

Each rule keeps one concern in one named, exported declaration that tests and readers can find. The examples show every one.

- **Migrations** always use the schema builder (`foundation/query/schema`), never dialect SQL. Declare each operation as an exported variable beside the migration (`var OrdersTable = schema.Create(...)`, `var DropOrdersTable = schema.Drop(...)`) and have `migrate.NewMigration` only reference them through `migrate.Schema`.
- **Queries** always use the query builder (`foundation/query`), in an exported function taking the statement's input (`func InsertOrder(placement Placement) query.Query`), or a variable when it takes none. Store methods only run them with `db.Exec`, `db.Select` or `db.Find`.
- **Transactions**: pass `db.WithTransaction` an exported method that returns the transaction body (`service.Record(placement)`), not an inline closure; likewise for `db.AfterCommit`.
- **Handlers** are exported functions returning `router.Handler`. Read input only through `core/http/request` (`request.ParseJSON(r, Schema)` for a body, `request.Param`, `request.Query`) and write through `core/http/response` (`response.JSON`); never decode or encode by hand.
- **Validation**: declare each `validate` schema as an exported variable in the surface's `schemas.go` (`var PlacementSchema = validate.Struct(...)`). `request.ParseJSON` turns a failure into a 422 problem.
- **Errors**: the feature root declares plain domain errors (`var ErrOutOfStock = errors.New(...)`). The web surface maps them to problem details in `problems.go`: one exported `problem.New` variable per problem and one exported `Problems = errormap.Middleware(errormap.Map(...))` that the route group `Use`s. Handlers just return the error.

## Feature packages

The feature root package holds the parts other code depends on:

- `<feature>.go`: domain types, pure rules, and the event topics and jobs the feature emits or dispatches. Declare them here because the root cannot import its own children.
- `service.go`: `Service`, the feature's API, built by `NewService(compose)`.
- `store.go`: a concrete `Store` over the core database and query packages. Add no repository interface: transactions travel in the context through `db.WithTransaction`, so stores compose without one.
- `feature.go`: the `Default<Type>` providers, such as `var DefaultService = application.Provide(NewService)`.

Children **above** the root import it and are imported only by process roots (`internal/bootstrap/<process>`), so each process chooses which surfaces it runs. Process roots alias them `<feature><surface>`, such as `ordersweb`. Each exports plain registration functions taking the composer: `Routes`, `Handlers`, `Listeners`. Inside them, file names are free.

Children **below** the root are imported by the root and import nothing from the feature or bootstrap: outbound clients, generated code, wire types. That keeps them reusable when the client is later promoted.

Start flat: a small surface may begin as a file in the root (`web.go` exporting `Routes`) and moves to a child when it outgrows a file or needs import isolation, such as deterministic workflow code.

## Construction

- `application.Provide` is for values that exist exactly once per process: shared resources, stores and services. `compose.Use(provider)` builds the value on first use and returns it unchanged afterwards, so never use `Provide` for something each caller needs fresh.
- Provider build functions are exported constructors, `func New<Type>(compose *application.Composer) *<Type>`, that call `compose.Use` on their dependencies first, then a blank line, then construct and register lifecycle (`Cleanup`, `Startup`, `Run`, `Check`). Dependencies therefore start first and stop last.
- Providers take no parameters. For several singletons of one kind, declare one provider variable each and share a constructor such as `NewDatabase(compose, "REPORTING_DATABASE_URL")`; call such lifecycle-owning constructors only from bootstrap providers, because each call opens a new resource.
- Name bootstrap providers for their resource (`Database`, `Router`) and feature providers `Default<Type>`.
- Read settings with `os.Getenv` inside the provider that needs them; there is no config package.
- Providers construct and register lifecycle only. Routes, handlers and listeners are mounted by registration functions called from `Compose`.
- Call `compose.Use` only while composing: in providers and registration functions, never in handlers or service methods. Express conditions as a plain `if`, such as `if compose.App().Debug()`.

## Across features

- To ask another feature something, define a small interface in your feature, store the other feature's service in a field of that type, and obtain it with `compose.Use(other.DefaultService)` in your constructor.
- To react to something, emit a topic declared in the publishing root, after commit (`db.AfterCommit`), with `events.Emit`. Subscribers listen from their `events/` child. The application event bus is in-process: register listeners in the process that emits, and hand durable or slow work to a queue job.
- A compile cycle between two features means they are one feature; merge them.
- Read the current user through `core/auth` (`request.Identity`, `request.User[T]`); only bootstrap imports `ark/iam`.

## Testing

- Test pure rules in `<feature>.go` directly.
- Test a surface by starting an application with only the registration functions you need, then calling `routes.Record(request)` on `compose.Use(bootstrap.Router)`. Point providers at local drivers through the environment (`DATABASE_URL=file::memory:` for SQLite) and run `compose.Use(bootstrap.Migrator)`'s `Up` in a `Startup` callback.
- Put fakes shared by other packages' tests in `internal/<feature>/<feature>test/`.

## Common mistakes

- Feature routes, handlers or services inside `internal/bootstrap`: move them to the feature.
- A struct collecting every resource or service: use one provider per value instead.
- A provider with parameters, or one parameterized provider for several instances: declare separate provider variables.
- `compose.Use` in a handler or service method: obtain the value in the constructor.
- A job or topic declared in a `jobs/` or `events/` child while the root dispatches it: move the declaration to the root.
- A listener registered only in a process that never emits the event.
- `Compose` written in `main.go` or in `internal/bootstrap` itself: put it in `internal/bootstrap/<process>`.
- Raw SQL, inline schema operations in `NewMigration`, an inline closure in `WithTransaction`, `json.NewDecoder` or `http.Error` in a handler, or a schema or problem declared inside a handler: extract each into its exported declaration as above.
- Packages named `util`, `common`, `shared`, `helpers`, `models`, `controllers`, `services`, `handlers` or `repositories`: name packages for their purpose, extract shared code on its third real use, and inline a helper again once it grows flags.
- One feature importing another feature's `web/` or `jobs/` child, or any feature importing `arkoren.dev/driver/...`: depend on the other feature's root, and on drivers only through bootstrap providers.

`arkoren check` enforces the mechanical rules (bootstrap imports, surface imports, driver and `ark/iam` imports in features, banned names, `compose.Use` placement, `bootstrap.New*` calls outside bootstrap). It has no exceptions; if a rule blocks something intentional, explain the case to the user instead of working around it.
