---
title: Layout Commands
description: Orient in a project with map, add features, processes, providers, migrations and ark modules without hand-writing boilerplate, and enforce the layout rules with check.
order: 65
summary:
  - Read a compact inventory of processes, providers, features and migrations before searching the code.
  - Create a feature, process, provider, migration or ark module's providers from templates and wire them with inserted compose.Use lines.
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

Without `--text`, the same content is one document (JSON when piped or with `--json`) with `module`,
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
lists a `tidy` change for `go.mod`. Pass `--skip-tidy` to stay offline and tidy later.

## Add an Ark Module

An ark is an opt-in module such as `arkoren.dev/ark/iam`. `add ark` writes the
bootstrap providers that wire it, so the module's own settings stay in small
files you can read and change:

```sh
arkoren add ark oauth google github
arkoren add ark iam --with reset,verification,two-factor,tokens,permissions,account
```

`add ark iam` always writes `internal/bootstrap/iam.go` with the `IAM`,
`IAMRouter` and, when `reset` or `verification` is selected, `IAMJobs`
providers, and one file per selected feature that returns the construction
callback of that feature's package (`PasswordReset`, `EmailVerification`,
`TokenScopes`, `Roles`, `AccountView`). `--with` takes `reset` (registration and
password recovery), `verification`, `two-factor`, `tokens`, `permissions` and
`account`; password login is always on. Sign-in providers are not features: a
value such as `google` fails with a usage error whose next step is
`arkoren add ark oauth google`.

`add ark oauth PROVIDER...` adds one or more sign-in clients: `google`, `github`,
`microsoft`, `apple` or `oidc`. Each writes `internal/bootstrap/<provider>.go`
with a provider (`Google`, `GitHub`, `Microsoft`, `Apple`, `OIDC`) that reads its
settings from the environment (`<PROVIDER>_CLIENT_ID`, `_CLIENT_SECRET` and
`_REDIRECT_URL`; Microsoft also `MICROSOFT_TENANT`, defaulting to `common`; Apple
reads `APPLE_TEAM_ID`, `APPLE_KEY_ID` and the Apple-issued `.p8` key from
`APPLE_KEY_FILE` instead of a secret; OIDC reads `OIDC_ISSUER_URL`), and `OAuthKey`
once. `github` also writes `GitHubSignUp`, which lets a first GitHub login create a
user; the other providers only log in existing users, because creating accounts is
your decision: write a fragment like `GitHubSignUp` to enable it. When
`internal/bootstrap/iam.go` exists, the command inserts the matching
`identity.Provider` call into the `iam.New` callback; otherwise it writes only the
client, and a later `add ark iam` wires every client it finds.

Every shared provider the new code uses is written with a local default when
`internal/bootstrap` does not declare one by name, and kept when it does:
`Database` (SQLite), `Cache` (memory), `Hasher` (Argon2id), and with email
`LinkSigner`, `Mailer` (logs email), `Queue` (memory), `Dispatcher`, `Consumer`
and `Work`, plus `TwoFactorKey` and `OAuthKey` as selected. Keys load from files
named by environment variables, such as `LINK_KEY_FILE`; create them with
`arkoren key generate shared --out FILE`.

The command inserts `compose.Use(bootstrap.IAMRouter)` into the server's
`Compose`, and `compose.Use(bootstrap.IAMJobs)` and `compose.Use(bootstrap.Work)`
into the `Compose` of `--process NAME` (default `server`), skipping lines already
there. The identity routes are mounted under `/auth`, so login is
`POST /auth/login`, sessions are `GET /auth/sessions` and a provider's callback is
`/auth/<provider>/callback`. With the memory queue the process that serves HTTP
must also send the emails, which is why `server` is the default; move the two
lines to a worker when you swap the queue driver.

Migrations run when the server starts. Without a `Migrator` provider, the command
writes `internal/bootstrap/migrator.go` with `Migrator` (your `migrations.All()`
and the module's `Migrations()`) and `Migrate`, which runs `Migrator.Up` in a
startup callback, creates an empty `internal/migrations/migrations.go` when the
project has no `All`, and inserts `compose.Use(bootstrap.Migrate)` before
`IAMRouter`. `Up` holds the migration lock, so replicas are safe. For production,
remove `Migrate` from the server and run the migrations in a one-shot `migrate`
process (`compose.Run(compose.Use(bootstrap.Migrator).Up)`); the process ends when
`Up` returns. With an existing `Migrator`, the command inserts
`compose.Require(migrator.Add(compose.Use(IAM).Migrations()...))` into it instead.

It also appends the variables with safe local defaults that `.env` lacks, and
never changes one it sets: `DATABASE_URL=file:app.db` (when it writes `Database`),
`PASSWORD_RESET_URL` and `EMAIL_VERIFY_URL`, and each client's `_REDIRECT_URL`,
all derived from `HTTP_ADDR`. Secrets, client IDs and key paths are never written.
Each line is an `edit` change on `.env`.

The module's pinned version is fetched with `go get` before `go mod tidy`, so tidy
does not pick a newer release; `--skip-tidy` leaves both for you, and the report
lists each module as a `require` change. The report also lists `next_steps`, the
work only you can do: create the key files with the `arkoren key generate shared
--out FILE` command it prints, register each client's callback URL and set its
credentials, replace the logging mailer before production, and run `make run`.

## Preview and Safety

Add `--dry-run` to any `add` command to print the plan and write nothing:

```json
{
  "summary": "Would add feature orders",
  "dry_run": true,
  "changes": [
    {"action": "create", "path": "internal/orders/orders.go"},
    {"action": "create", "path": "internal/orders/service.go"},
    {"action": "edit", "path": "internal/bootstrap/server/compose.go", "detail": "compose.Use(bootstrap.OrdersRoutes)"}
  ]
}
```

`changes` holds one `create` per new file and one `edit` per inserted line, whose
`detail` is that line. `add ark` also reports a `require` change per pinned
module and `next_steps` objects with a `text` and, only when one command does
the step, a `command`; a run that executed `go mod tidy` ends with a `tidy`
change for `go.mod`. Without `--dry-run` the summary is "Added feature orders".

Without it the command prints the same report after writing. `add` never
overwrites: if any target path exists, including a feature directory, it fails
with a `file_conflict` error and exit 1 before writing. It changes existing code only by inserting lines
into `Compose` (and its imports) or into the `All` list, formatted with gofmt,
and refuses with a `usage` error (exit 2) when `Compose` is missing, takes no named
`*application.Composer`, or another package is already imported as `bootstrap`.
If the file changed between planning and writing, it stops without writing.
Review the diff with your version control after any `add`.

## Check the Layout

```sh
arkoren check
```

The command prints one report: a `summary` ("No layout violations" or "3 layout
violations"), `ok`, and `problems`, an array that is empty for a conforming
project. Each problem has `rule`, `path`, `line` and a `message` naming the fix. It exits 1 when there are violations, so it can gate a build.
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
| `composer-use-outside-composition` | `Use` is called on a `*application.Composer` parameter inside a method, or inside a closure the function creates, such as a `Startup` callback or handler. A construction callback, a function literal taking a `*pkg.XBuilder` of an `arkoren.dev` package (the settings callback of `application.New` or `iam.New`, and the sub-builder callbacks nested in it), runs during the build and is allowed. |
| `composer-supply-outside-tests` | `Supply` is called on a `*application.Composer` parameter in a file that is not a `_test.go` file. A test replaces a provider's value from its own root provider. |
| `bootstrap-new-outside-bootstrap` | Code outside the `internal/bootstrap` package calls `bootstrap.New...`. |

The checks are syntactic, so they run without a build but have limits. The
`Use` rule follows only a Composer received as a function parameter, not one
stored in a struct field or passed to another function. A rule never replaces a
review of what a feature does. An existing project with another layout is
refused by the layout detection rather than reported as full of violations.
