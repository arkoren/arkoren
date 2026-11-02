---
title: Creating Projects
description: Preview and generate an owned HTTP starter, resolve its dependencies, and understand its development defaults.
order: 20
summary:
  - Preview a project before allowing the CLI to write files.
  - Understand confirmation, conflicts, and recovery from partial generation.
  - Resolve starter dependencies and recognize its security and configuration defaults.
  - Choose bundled agent files without giving the generator ownership of application code.
---

# Creating Projects

Use `arkoren new` when you want a new HTTP service with composition, probes, and
shutdown already connected. The result is ordinary Go code you own, not a project
that needs regeneration. For an existing application, [install individual
modules](installation.md#existing-applications) instead.

## Installing Tools

With [Go 1.27 or later](installation.md#go-requirements), install the CLI:

```sh
go install arkoren.dev/cmd/arkoren@latest
```

Go places it in `GOBIN`, if set, otherwise under the first `GOPATH` entry's
`bin` directory, usually `$HOME/go/bin`. Put that directory on `PATH`, then
check the available generation options:

```sh
arkoren new --help
```

Help and generation work offline without a running application or MCP endpoint
configuration. [CLI Installation](../cli/overview.md#installing-arkoren) also
offers module-tool installation; use `go tool arkoren` in place of `arkoren`
throughout this page if you choose it.

## Previewing Files

Choose a destination and the Go module path your application will keep. From
the destination's parent directory, preview a service named `hello`:

```sh
arkoren new hello --module example.com/hello --dry-run --verbose
```

The JSON report lists the absolute destination, module, planned files, and
conflicts. Without `--verbose`, it reports counts instead of full inventories.
Dry runs do not prompt, write files, or download dependencies. A conflicting plan
still returns a nonzero status.

The destination is required; use `.` explicitly for the current directory.
Without `--module`, the module path is the destination's basename. Invalid
names are rejected, not repaired, so set `--module` explicitly for shared projects.

## Writing Files

Use an unused or empty directory for the ordinary path. After inspecting the
preview, write the project:

```sh
arkoren new hello --module example.com/hello
```

Success produces one JSON report containing `created: true`, the absolute
destination, module, and file count. Generation initializes neither Git nor
application infrastructure, downloads nothing, and creates no application tests.

A nonempty destination, even one containing only `.git`, requires confirmation.
The default is no; only a complete `y` or `yes` line approves it. Without an
interactive terminal, explicitly approve that directory after reviewing its plan:

```sh
arkoren new hello --module example.com/hello --yes
```

This is an alternative invocation, not a second generation step for an already
created project. `--yes` never permits overwriting. Existing files or symlinks at generated paths,
unsafe parent paths, case-insensitive filename collisions, and planned
file/directory overlaps block generation before writes. Choose another
destination or resolve the conflict yourself; there is no automatic merge.

An I/O failure during writing can leave a partial project. Inspect the paths
reported by the error before retrying: files already created remain and become
conflicts. Do not delete an existing directory merely to clear a generation error.

## Resolving Dependencies

Enter the generated project:

```sh
cd hello
```

Resolve its Arkoren imports and dependency files, then check the code:

```sh
go get arkoren.dev/core arkoren.dev/driver/mcp
go mod tidy
go vet ./...
```

Unlike generation, these commands can download modules. The starter also declares
godotenv for local environment loading, Tint for terminal logs, and go-isatty for
terminal detection. They are application dependencies, not dependencies of core.
Commit the resolved `go.mod` and `go.sum`.

The generated Makefile is optional application tooling. `make` lists its targets;
`make check` verifies formatting, runs vet, and runs tests without modifying
dependencies. `make fmt` changes formatting. Race tests require a C compiler.
Continue to [Quickstart](quickstart.md#first-request) to run the service and add
your first endpoint.

## Starter Defaults

`cmd/server/main.go` creates the application and calls
`app.Run(ctx, bootstrap.Compose)`. Files in `internal/bootstrap` construct the
router, optional diagnostics endpoint, and server. Typed factories return
constructed values; the application starts components only after composition
succeeds. The generated signal context requests shutdown on Ctrl+C or `SIGTERM`.

The router serves a welcome page at `/` and probes at `/livez`, `/readyz`, and
`/startupz`. It applies security headers, request telemetry, panic recovery, and
same-origin CSRF protection before registering routes. It grants no extra trusted
origins or CORS permissions. These defaults are not authentication, host validation,
proxy trust, or rate limiting. Keep GET, HEAD, and OPTIONS free of state changes.
HSTS requires an explicit HTTPS deployment policy.

The executable loads `.env` from its working directory without replacing existing
environment values; loading errors are ignored. The generated file binds
`HTTP_ADDR=127.0.0.1:8080` and sets `APP_DEBUG=true`. Empty or unset `HTTP_ADDR`
falls back to `:8080`, listening on all interfaces.

> [!WARNING]
> Debug mounts unauthenticated diagnostics at `/mcp` on the main listener.
> Anyone who can reach it can inspect logs and telemetry attributes. Keep local
> listeners on loopback and set `APP_DEBUG=false` in production.

Only the exact value `true` enables debug. Debug capture retains bounded recent
history in memory and loses it on exit; it needs no external telemetry backend.
See [Capture Setup](../diagnostics/capture-setup.md) for customization. If you
change the listener address, update `ARKOREN_MCP_URL` before using CLI diagnostics.

The generated `.env` is excluded from Git and created with restricted access
(POSIX mode `0600`, or a protected current-user DACL on Windows). Protect its
parent directory too. `.env.example` disables debug and can be committed. No
application secret or signing key is generated; provision credentials only when
the feature needs them.

The starter's logger uses Tint on terminals and JSON when stderr is redirected.
`NO_COLOR` disables terminal colors. Customize `Logger()` in
`internal/bootstrap/logging.go`, rather than assuming application environment
alone selects the starter's log format.

## Agent Files

By default, generation includes `AGENTS.md` and the bundled documentation skill
under `.agents/skills/arkoren/`, including its installation manifest. Commit them
if you use them. Agent discovery varies; configure your agent to read the
generated instructions when needed.

If your agent needs a different skills parent, choose it during generation
instead of the earlier invocation:

```sh
arkoren new hello --module example.com/hello --skills-dir .claude/skills
```

The CLI appends `arkoren` and updates the generated pointers. The parent must be
a project-relative slash-separated path using letters, digits, dots, underscores,
or hyphens, without `..` components. File/directory conflicts still block writes.

Alternatively, omit both instructions and the skill with `--no-agent-files`.
Do not combine that flag with a custom `--skills-dir`. Existing agent-file
collisions are not merged or overwritten.

Application files and `AGENTS.md` remain yours.
[`arkoren skills update`](../agent-skills/working-with-agents.md#updating-skills)
manages only the skill directory and rejects modified managed files. There is
no application regeneration or upgrade step. Grow the code using
[Project Layout](project-layout.md), and use [Deployment](deploying.md) when
you are ready to build an image or binary.
