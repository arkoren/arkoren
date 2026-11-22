---
title: Layout Commands
description: Orient in a project with map, add features, processes, providers and migrations without hand-writing boilerplate, and enforce the layout rules with check.
order: 65
summary:
  - Read a compact inventory of processes, providers, features and migrations before searching the code.
  - Create a feature, process, provider or migration from templates and wire it with inserted compose.Use lines.
  - Preview every change with --dry-run, and know that existing files are never overwritten.
  - Fail a build when imports, bootstrap shape, package names or Composer use break the layout rules.
---

# Layout Commands

Three local commands work on a project that follows the
[project layout](../first-steps/project-layout.md): `map` shows what exists,
`add` creates the next piece, and `check` reports rules the code breaks. They
read source with the Go parser, so they need no running application, no network
and no successful build. Run them in the project root.

A project conforms when it has `go.mod`, at least one `cmd/<process>/main.go`
and an `internal/bootstrap` directory. Each process's `App` and `Compose` live in
`internal/bootstrap/<process>`. In any other directory the commands exit 2 and
point at the `arkoren-structure` skill; they never search parent directories.
`add` and `map` read the process-root layout that `arkoren new` generates; a
project that still declares `Compose` in `cmd/<process>/main.go` needs it moved
first.

## Map the Project

Read the inventory before searching for where something lives:

```sh
arkoren map --text
```

Each line names one fact: the statements a process makes in `Compose`, the
bootstrap providers grouped by file, each feature's constructors and the
surfaces above and below its root, the migration ids, and platform packages. A
statement inside an `if` carries its condition, so optional wiring is visible.

```text
module example.com/shop
process server: compose.Use(bootstrap.OrdersRoutes); compose.Use(bootstrap.MCP) [if compose.App().Debug()]; compose.Use(bootstrap.Server)
bootstrap logging.go: LogHandler
bootstrap orders.go: OrdersStore; OrdersService; OrdersRoutes
bootstrap router.go: Router
feature orders constructors: NewService; NewStore
feature orders above: web(NewHandlers)
```

Without `--text`, the same content is one JSON document with `module`,
`processes`, `bootstrap`, `features`, `migrations` and `platform`. The listing
holds names only; open the files for behavior.

## Add a Feature

Create a feature with the surfaces it needs:

```sh
arkoren add feature orders --web --jobs
```

The command creates the plain-Go root `internal/orders` with `orders.go`,
`service.go` and `store.go`, and one child package for each surface flag:
`--web`, `--jobs`, `--flow`, `--tools`, `--events` and `--schedules`. Each child
is a struct built by a `New` constructor over the feature's `Service`, with a
`Register` method for its target where one exists. The feature imports neither
`application` nor `internal/bootstrap`.

The wiring goes in `internal/bootstrap/orders.go`: an `OrdersStore` and an
`OrdersService` provider, and one provider per surface, such as `OrdersRoutes`,
which registers the handlers on the shared router under `/orders`. The command
then inserts one `compose.Use(bootstrap.OrdersRoutes)` line per surface into
`Compose`, adding the `bootstrap` import if the file lacks it:

```go
compose.Use(bootstrap.OrdersRoutes)
compose.Use(bootstrap.OrdersJobs)
```

The lines go before the first `if` statement or `compose.Use(bootstrap.Server)`,
so routes exist before diagnostics and the server. If `Compose` has neither, the
lines are appended. `--process NAME` selects the `Compose` of
`internal/bootstrap/NAME` instead of `internal/bootstrap/server`.

The generated files compile. The store holds no database yet, because a new
project has no `Database` provider; a `TODO` explains how to add one. The web and
events providers register their surface; the jobs, workflow, tool and schedule
providers build theirs and leave a `TODO` for the registration that needs a
choice the project has not made, such as a queue driver or scheduler. Search the
generated files for `TODO` before relying on them. The generated code follows the
`arkoren-structure` rules: the store declares its statement in an exported
query-builder function, and the web child registers relative paths behind
`Problems`, declared in `web/problems.go`, which maps the root's `ErrNotFound`
to a problem.

Feature names use lowercase letters and digits, are not Go keywords, and avoid
the names of `bootstrap`, `migrations`, `platform`, the surface packages and the
packages the generated files import. Role names such as `models` are refused, as
is a name whose bootstrap providers (`OrdersStore`, for example) already exist.

## Add Other Pieces

```sh
arkoren add process worker
arkoren add provider mail-queue
arkoren add migration create_orders
```

| Command | Creates |
| --- | --- |
| `add process NAME` | `cmd/NAME/main.go`, which runs `application.Run` with the process root's `App` and `Compose`, and the process root `internal/bootstrap/NAME` with `app.go` (an `App` provider naming the service `<project>-NAME`, where project is the module path's last element without a trailing `v2`-style element) and `compose.go` (an empty `Compose`). It needs the `LogHandler` provider that `arkoren new` generates. Hyphens are dropped from the package name: `admin-tasks` is package `admintasks`. |
| `add provider NAME` | `internal/bootstrap/NAME.go` with `var <Name> = application.Provide(...)` and a placeholder value to replace. `mail-queue` declares `MailQueue`. |
| `add migration NAME` | `internal/migrations/NNNN_NAME.go` with schema-builder operations in exported variables and a `migrate.NewMigration` that runs them, and appends it to `All()` in running order. `create_orders` creates and drops the `orders` table; any other name alters the table named by the words after the first with an empty change. |

`NNNN` is one more than the highest numbered file in `internal/migrations`, so
files sort in running order. If the directory has no `All` function, the command
creates `migrations.go` with one. It refuses an existing `migrations.go` that
declares none, and a migration whose id or identifier already exists. Declare
the columns or the change before you ship: an empty alteration fails when it
runs, and nothing runs until your migrator adds `migrations.All()`.

When a created file imports a module that `go.mod` does not require yet, such
as `arkoren.dev/foundation/query`, `add` runs `go mod tidy` after writing and
reports `"tidied": true`. Pass `--skip-tidy` to stay offline and tidy later.

## Preview and Safety

Add `--dry-run` to any `add` command to print the plan and write nothing:

```json
{
  "dry_run": true,
  "created": ["internal/orders/orders.go", "internal/orders/service.go"],
  "edited": [{"path": "internal/bootstrap/server/compose.go", "inserted": ["compose.Use(bootstrap.OrdersRoutes)"]}],
  "tidied": false
}
```

Without it the command prints the same report after writing. `add` never
overwrites: if any target path exists, including a feature directory, it fails
with exit 2 before writing. It changes existing code only by inserting lines
into `Compose` (and its imports) or into the `All` list, formatted with gofmt,
and refuses with exit 2 when `Compose` is missing, takes no named
`*application.Composer`, or another package is already imported as `bootstrap`.
If the file changed between planning and writing, it stops without writing.
Review the diff with your version control after any `add`.

## Check the Layout

```sh
arkoren check
```

The command prints one JSON document, `{"ok": true, "violations": []}` for a
conforming project. Each violation has `rule`, `file`, `line` and a `message`
naming the fix. It exits 1 when there are violations, so it can gate a build.
Projects created with `arkoren new` run it as part of `make check`; the target
needs the `arkoren` command on `PATH` (override with `make check ARKOREN=...`).

| Rule | Reported when |
| --- | --- |
| `feature-imports-application` | A feature root, child or `internal/platform` package, test files included, imports `arkoren.dev/core/application` or `internal/bootstrap` (including a process root). Features are plain Go. |
| `bootstrap-action-func` | An exported function of the `internal/bootstrap` package takes a `*application.Composer` and returns nothing. Export providers or value-returning `New*` constructors instead. Process roots are exempt. |
| `surface-imported-outside-bootstrap` | A non-test file outside `internal/bootstrap` (including its process roots) imports a surface child (`web`, `jobs`, `flow`, `tools`, `events`, `schedules`, `<feature>test`). Test files may import fixtures. |
| `below-child-imports-feature` | A package below a feature root imports that feature's root or surfaces. |
| `feature-imports-driver` | A feature or `internal/platform` package imports `arkoren.dev/driver/...` or `arkoren.dev/ark/iam`. Only bootstrap wires them. |
| `banned-package-name` | A package path contains `util`, `common`, `shared`, `helpers`, `models`, `controllers`, `services`, `handlers` or `repositories`. |
| `composer-use-outside-composition` | `Use` is called on a `*application.Composer` parameter inside a method, or inside a closure the function creates, such as a `Startup` callback or handler. A settings callback passed directly to `application.New` runs during the build and is allowed. |
| `bootstrap-new-outside-bootstrap` | Code outside the `internal/bootstrap` package calls `bootstrap.New...`. |

The checks are syntactic, so they run without a build but have limits. The
`Use` rule follows only a Composer received as a function parameter, not one
stored in a struct field or passed to another function. A rule never replaces a
review of what a feature does. An existing project with another layout is
refused by the layout detection rather than reported as full of violations.
