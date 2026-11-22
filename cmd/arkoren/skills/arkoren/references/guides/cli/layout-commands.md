---
title: Layout Commands
description: Orient in a project with map, add features, processes, providers and migrations without hand-writing boilerplate, and enforce the layout rules with check.
order: 65
summary:
  - Read a compact inventory of processes, providers, features and migrations before searching the code.
  - Create a feature, process, provider or migration from templates and register it with one inserted line.
  - Preview every change with --dry-run, and know that existing files are never overwritten.
  - Fail a build when imports, package names or Composer use break the layout rules.
---

# Layout Commands

Three local commands work on a project that follows the
[project layout](../first-steps/project-layout.md): `map` shows what exists,
`add` creates the next piece, and `check` reports rules the code breaks. They
read source with the Go parser, so they need no running application, no network
and no successful build. Run them in the project root.

A project conforms when it has `go.mod`, at least one `cmd/<process>/main.go`
and an `internal/bootstrap` directory. Each process's `Compose` lives in
`internal/bootstrap/<process>`; projects that still declare it in
`cmd/<process>/main.go` are read the same way. In any other directory the commands exit
2 and point at the `arkoren-structure` skill; they never search parent
directories.

## Map the Project

Read the inventory before searching for where something lives:

```sh
arkoren map --text
```

Each line names one fact: the calls a process makes in `Compose`, the bootstrap
providers and registration functions, each feature's providers and the
surfaces above and below its root, the migration ids, and platform packages. A
call inside an `if` carries its condition, so optional registration is visible.

```text
module example.com/shop
process server: welcomeweb.Routes(compose); ordersweb.Routes(compose); bootstrap.Diagnostics(compose) [if compose.App().Debug()]; bootstrap.Listen(compose)
bootstrap providers: Router
bootstrap register: Listen; Diagnostics
feature orders providers: DefaultStore; DefaultService
feature orders above: web(Routes)
```

Without `--text`, the same content is one JSON document with `module`,
`processes`, `bootstrap`, `features`, `migrations` and `platform`. The listing
holds names only; open the files for behavior.

## Add a Feature

Create a feature with the surfaces it needs:

```sh
arkoren add feature orders --web --jobs
```

The command creates `internal/orders` with `orders.go`, `service.go`,
`store.go` and `feature.go`, and one child package for each surface flag:
`--web`, `--jobs`, `--flow`, `--tools`, `--events` and `--schedules`. Each child
exports a registration function (`Routes`, `Handlers`, `Workflows`, `Tools`,
`Listeners`, `Entries`). The command then inserts one call per surface into
`Compose`, importing the child as `<feature><surface>`, such as `ordersweb`:

```go
ordersweb.Routes(compose)
ordersjobs.Handlers(compose)
```

The calls go after the existing registrations and before the bootstrap tail
(`bootstrap.Diagnostics` and `bootstrap.Listen`), so routes exist before
diagnostics list them and the listener serves them. If `Compose` has no such
tail, the calls are appended. `--process NAME` selects the `Compose` of
`internal/bootstrap/NAME` instead of `internal/bootstrap/server`.

The generated files compile and mount a placeholder route, job, workflow, tool
or schedule entry. They follow the `arkoren-structure` code rules: the store
declares its statement in an exported query-builder function, and the web child
mounts its routes in a group behind `Problems`, declared in `web/problems.go`,
which maps the root's `ErrNotFound` to a problem. Where a working registration needs a choice the project has
not made, such as a queue driver or workflow engine, the registration function
is empty and its comment says what to add. Search the generated files for
`TODO` before relying on them.

Feature names use lowercase letters and digits, are not Go keywords, and avoid
the names of `bootstrap`, `migrations`, `platform`, the surface packages and the
packages the generated files import. Role names such as `models` are refused.

## Add Other Pieces

```sh
arkoren add process worker
arkoren add provider mail-queue
arkoren add migration create_orders
```

| Command | Creates |
| --- | --- |
| `add process NAME` | `internal/bootstrap/NAME/compose.go` with an empty exported `Compose`, and `cmd/NAME/main.go`, which builds the application and runs it. Hyphens are dropped from the package name: `admin-tasks` is package `admintasks`. |
| `add provider NAME` | `internal/bootstrap/NAME.go` with `var <Name> = application.Provide(...)` and a placeholder value to replace. `mail-queue` declares `MailQueue`. |
| `add migration NAME` | `internal/migrations/NNNN_NAME.go` with schema-builder operations in exported variables and a `migrate.NewMigration` that runs them, and appends it to `All()` in running order. `create_orders` creates and drops the `orders` table; any other name alters the table named by the words after the first with an empty change. |

`NNNN` is one more than the highest numbered file in `internal/migrations`, so
files sort in running order. If the directory has no `All` function, the command
creates `migrations.go` with one. It refuses an existing `migrations.go` that
declares none, and a migration whose id or identifier already exists. Declare
the columns or the change before you ship: an empty alteration fails when it
runs, and nothing runs until your migrator adds `migrations.All()`.

The stubs import foundation modules such as `arkoren.dev/foundation/query`.
Run `go mod tidy` after `add` so `go.mod` requires them.

## Preview and Safety

Add `--dry-run` to any `add` command to print the plan and write nothing:

```json
{
  "dry_run": true,
  "created": ["internal/orders/orders.go", "internal/orders/service.go"],
  "edited": [{"path": "internal/bootstrap/server/compose.go", "inserted": ["ordersweb.Routes(compose)"]}]
}
```

Without it the command prints the same report after writing. `add` never
overwrites: if any target path exists, including a feature directory, it fails
with exit 2 before writing. It changes existing code only by inserting lines
into `Compose` (and its imports) or into the `All` list, formatted with gofmt,
and refuses with exit 2 when `Compose` is missing, takes no named
`*application.Composer`, or an alias clashes with another import. If the file
changed between planning and writing, it stops without writing. Review the
diff with your version control after any `add`.

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
| `bootstrap-imports-feature` | A file in the `internal/bootstrap` package imports any `internal/<feature>` package. Process roots below it, `internal/bootstrap/<process>`, may. |
| `surface-imported-outside-cmd` | A non-test file outside `cmd` and the process roots imports a surface child (`web`, `jobs`, `flow`, `tools`, `events`, `schedules`, `<feature>test`). Test files may import fixtures. |
| `below-child-imports-feature` | A package below a feature root imports that feature's root or surfaces. |
| `feature-imports-driver` | A feature or `internal/platform` package imports `arkoren.dev/driver/...` or `arkoren.dev/ark/iam`. Only bootstrap wires them. |
| `banned-package-name` | A package path contains `util`, `common`, `shared`, `helpers`, `models`, `controllers`, `services`, `handlers` or `repositories`. |
| `composer-use-outside-composition` | `Use` is called on a `*application.Composer` parameter inside a method, or inside a closure the function creates, such as a `Startup` callback or handler. |
| `bootstrap-new-outside-bootstrap` | Code outside `internal/bootstrap` calls `bootstrap.New...`. |

The checks are syntactic, so they run without a build but have limits. The
`Use` rule follows only a Composer received as a function parameter, not one
stored in a struct field or passed to another function. A rule never replaces a
review of what a feature does. An existing project with another layout is
refused by the layout detection rather than reported as full of violations.
