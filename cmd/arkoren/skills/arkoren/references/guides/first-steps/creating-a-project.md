---
title: Creating a Project
description: Generate a small HTTP service with direct composition, preview its files safely, and install dependencies.
order: 15
---

# Creating a Project

## Introduction

To start a new HTTP service without assembling its bootstrap by hand,
use `arkoren new`. It writes a starter service that you own
and maintain. The generated layout separates the executable,
application composition, and environment configuration into ordinary Go files.
It includes debug-only MCP diagnostics and local telemetry capture but no application tests.
Generation does not need a running application or MCP endpoint settings.

This is the starting point for a new service. The [Quickstart](quickstart.md) uses the generated
files to teach routing, a cache counter, and error responses. The generated layout is not a runtime directory
requirement; you can move or reorganize the files as your application grows.

## Previewing the project

Install the CLI as described in [Arkoren CLI Overview](../cli/overview.md#installing-the-cli).
Generation does not download dependencies.

From the directory that will contain your new project, preview the destination and files:

```sh
arkoren new billing --module example.com/billing --dry-run
```

The JSON preview summarizes the resolved choices, file count, and conflict count.
Add `--verbose` to list every generated path and conflict. It never prompts, writes files, or
accesses the network. Resolve any conflicts before running the command without `--dry-run`.

The required positional argument is the destination. Use `arkoren new billing` to create a new directory, or
`arkoren new .` to use the current directory explicitly. Bare `arkoren new` reports usage with exit code 2
without writing files. Optional flags can precede or follow the destination; the former `--folder` flag is not supported.

All flags are optional. By default, the command uses the basename of the absolute destination as the module
path. The starter uses direct constructor calls, without a DI container.
An invalid inferred module name is an error; the CLI does not repair or normalize it. Pass `--module` with
the Go module path you intend to keep, especially when the project will be published or shared.

`new` accepts exactly one destination. For the local usage listing, run without a destination:

```sh
arkoren new --help
```

Help works offline before endpoint settings are read, even if a project env file is missing or invalid.

## Writing the files

The command creates a missing destination and accepts an empty destination
without confirmation. A nonempty destination, including one containing only
`.git`, requires confirmation; the default answer is no. Terminal confirmation
accepts only a complete `y` or `yes` line, ignoring case and surrounding whitespace.
EOF, an unterminated answer, and all other answers cancel without writing. If the
prompt cannot be written completely to stderr, the command fails before reading
input or writing files.

The complete file plan is checked before confirmation or writes. Existing files
or symlinks at generated paths always block generation, even with `--yes`.
Duplicate generated file paths and paths needed as both a file and a directory
also block generation. These plan comparisons ignore case for portability and
do not require the destination to exist.

Once the preview matches your intended project, generate it:

```sh
arkoren new billing --module example.com/billing
```

For this example, the command writes the application files, project instructions,
and the complete bundled agent skill in this starter layout. Its JSON result
contains `created: true`, the absolute `destination`, the selected `module`, and
`file_count`. Add `--verbose` for the complete created-file inventory:

```text
billing/
├── .agents/
│   └── skills/
│       └── arkoren/
│           ├── .arkoren-skill.json
│           ├── SKILL.md
│           └── references/
├── .dockerignore
├── .editorconfig
├── .env
├── .env.example
├── .gitignore
├── Dockerfile
├── Makefile
├── AGENTS.md
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   └── bootstrap/
│       ├── app.go
│       ├── bootstrap.go
│       ├── logging.go
│       ├── mcp.go
│       ├── router.go
│       └── server.go
├── go.mod
└── README.md
```

`main.go` constructs the application with `application.New(bootstrap.Application())`, without returning
a construction error. `Application()` in `internal/bootstrap/app.go` sets the name to the destination's basename
and enables debug only when `APP_DEBUG=true`. The executable calls
`app.Run(ctx, bootstrap.Compose)` with a signal
context. The executable's run helper defers signal cleanup before returning an exit status to `main`.
`bootstrap.go` defines the void root `Compose(compose *application.Composer)`.
The other bootstrap files export typed factories: `Router()` returns
`application.Factory[*router.Router]`, `MCP(routes)` returns
`application.Factory[*mcpserver.Server]`, and `Server(handler)` returns
`application.Factory[*server.Server]`. Obtaining a factory only captures its
arguments; it does not register routes, construct endpoints, or create listeners.
The root obtains the shared router with `routes := compose.Use(Router())`, selects
`MCP(routes)` with `compose.UseDebug`, and applies `Server(routes)` with `compose.Use`.
The selected callbacks construct and return their typed values, register the
welcome page and probes, mount diagnostics at `/mcp`, and register listener startup,
cleanup, and serving. Returned components are constructed, not started; startup
runs after all composition succeeds. Shared facilities come from `compose.App()`.
`Server(handler)` reads `HTTP_ADDR` inside its composition callback, alongside server construction and lifecycle registration.
An empty or unset value uses the server's `:8080` default.
The generated README explains dependency setup and startup. Add tests as your
application gains behavior.

The generated Makefile belongs to your application and requires Make and standard Unix tools,
not the Arkoren CLI. Run `make` to list commands, `make run` to start the service, and
`make build` to write `bin/server`. After installing dependencies, use `make check` for
nonmutating formatting verification, static analysis, and tests. `make fmt` formats Go source;
`make test-race` enables CGO for race tests and requires a C compiler. These targets preserve
Go's test cache and do not run `go get` or `go mod tidy`.

`.editorconfig` sets UTF-8, LF line endings, final newlines, tabs for Go, `go.mod`, and Makefile recipes, and two-space
indentation elsewhere. It preserves Markdown trailing spaces for hard line breaks. Use an
EditorConfig-compatible editor; `gofmt` remains the source of truth for Go formatting.

In a noninteractive run, explicitly permit a nonempty destination with `--yes`:

```sh
arkoren new billing --module example.com/billing --yes
```

`--yes` only skips that confirmation. An existing file at any generated path is always a conflict, and generation
fails rather than overwriting it. Choose another destination or move the conflicting file yourself after
reviewing it. Previewing an existing directory remains safe with `--dry-run`.

Symlinks at generated paths are conflicts even when their targets do not exist.
This includes `cmd/server/main.go`: the executable directory is always `cmd/server`,
not the destination directory's basename. Generation rejects these conflicts
before writing any files.

An I/O failure during generation can leave a partial project. The error names
the failed file; inspect the destination before retrying, because already
created files remain and count as conflicts on the next run.

## Agent instructions

`AGENTS.md` contains project-specific defaults: module identity, package layout,
the selected composition approach, checks, and lifecycle and secret-handling
conventions. It points the agent to `.agents/skills/arkoren/SKILL.md` and the
relevant guides and version-selected `go doc` reference before changing Arkoren code. Agent discovery varies;
configure your agent to read that path if needed.

Both the instructions and skill are generated offline. Commit `AGENTS.md`, the
skill directory, and its `.arkoren-skill.json` manifest. Application code and
`AGENTS.md` are user-owned; only the skill directory is managed by
[`arkoren skills update`](../agent-skills/working-with-agents.md#choosing-a-version).
Skill updates leave `AGENTS.md` untouched and reject modified managed files.

Choose another project-relative skills parent directory when your agent requires it:

```sh
arkoren new billing --skills-dir .claude/skills
```

The CLI appends `arkoren` and adjusts the pointers in `AGENTS.md` and README.
Use slash-separated paths containing letters, digits, dots, underscores, or
hyphens. Absolute paths, `..` components, and shell-special characters are
rejected. To omit both `AGENTS.md` and the skill, pass `--no-agent-files` instead;
it cannot be combined with a custom `--skills-dir`.

Choose a skills parent that does not require a generated file to become a
directory: for example, `--skills-dir go.mod` conflicts with the generated
`go.mod` file. Sharing a generated directory such as `internal/bootstrap` is
allowed.

Existing `AGENTS.md` and skill-file collisions block generation, even with
`--yes`. There is no automatic merge. Preview all paths with `--dry-run`, or
use `--no-agent-files` to preserve an existing agent setup without generating one.

## Installing dependencies

First enter the generated project root:

```sh
cd billing
```

Resolve dependencies and check the generated code:

```sh
go get arkoren.dev/core arkoren.dev/driver/mcp
go mod tidy
go vet ./...
```

These Go commands, unlike `arkoren new`, can download dependencies. The starter
pins `github.com/joho/godotenv` to `v1.5.1` in the generated `go.mod`.
It also pins Tint and go-isatty for terminal logging; these are application
dependencies, not dependencies of Arkoren core.

## Running the service

Start the example from its project root:

```sh
go run ./cmd/server
```

Open `http://127.0.0.1:8080/` to see the welcome page with the development defaults.
The server factory's `application.Started` observer logs `http server started`
after `app started`, once startup succeeds and runners launch. Its `address`
field reports the bound socket address, including an assigned port when
`HTTP_ADDR` selects port `0`. It does not infer a browser URL, public domain, or
container port mapping. Startup failures produce no successful HTTP startup log,
and readiness recovery does not repeat it.

The service serves `GET /` and the application probes `/livez`, `/readyz`, and `/startupz`. In another terminal,
check the root and readiness endpoints:

```sh
curl -i http://127.0.0.1:8080/
curl -i http://127.0.0.1:8080/readyz
```

The root serves an HTML welcome page through `app.WelcomeHandler()`. It does not expose operator information;
`InfoHandler` remains an explicit opt-in. Readiness returns `200 OK` while the application is ready. To change the listen
address, set the environment variable before starting:

```sh
HTTP_ADDR=127.0.0.1:8081 go run ./cmd/server
```

`bootstrap.Router` registers [security headers](../browser-security/secure-headers.md),
telemetry, [panic recovery](../http-fundamentals/routing.md#recovering-from-panics), and
[same-origin CSRF protection](../browser-security/csrf-protection.md) before routes,
in that order. Recovery converts handler panics before response commitment to
private server errors; `http.ErrAbortHandler` still aborts the connection.
Headers prevent MIME sniffing and framing, limit referrer disclosure, and disable
the legacy XSS auditor. The welcome page keeps its stronger CSP. Security headers
omit HSTS by default; enable it in the application or ingress after establishing
HTTPS deployment.

No additional trusted origins or CORS permissions are granted. Keep GET, HEAD,
and OPTIONS free of state changes. Requests without Origin or Sec-Fetch-Site
headers are allowed; CSRF protection is not authentication. Configure cross-origin
frontends and provider callbacks explicitly with narrow route scopes. These
defaults do not configure host validation, proxy trust, cookies, or rate limits,
and do not make debug diagnostics safe to expose publicly.

The executable imports `godotenv/autoload`, which loads `.env` from its working
directory during initialization without replacing existing environment variables.
Loading errors are ignored. The generated file selects loopback through
`HTTP_ADDR=127.0.0.1:8080`. Without a nonempty address in either source, the
server default `:8080` listens on all interfaces.
The executable uses `application.NewSignalContext(context.Background())` and
defers its stop function. Ctrl+C or `SIGTERM` cancels that context, and the
Arkoren application owns graceful server shutdown. Signal handling uses standard
Go cancellation; it does not erase the terminal's echoed `^C`.
Clean shutdown exits with status 0. The run helper returns `app.Run` directly;
the executable maps an error to exit status 1 after signal cleanup. The application
reports lifecycle failures before telemetry finalization, so the executable does
not log them again after telemetry stops. Failures before telemetry starts or
during its finalization reach stderr only. Application
construction has no error result; lifecycle failures still propagate to the process exit status.
See [Application](../application/overview.md) for startup, readiness, and shutdown
behavior.

To customize log output, edit `Logger()` in `internal/bootstrap/logging.go`.
The application writes logs to stderr: terminals use Tint with source locations;
redirected output uses JSON. A nonempty `NO_COLOR` disables terminal colors.
Both handlers honor runtime log-level changes through `app.SetLogLevel`.

### Local telemetry, diagnostics, and secrets

The generated `.env` enables `APP_DEBUG=true`. Exclusive creation applies POSIX
mode `0600` or, on Windows, a protected discretionary access control list (DACL)
granting access only to the current user. The Windows ACL is installed atomically
with creation, before content is written, rather than repaired after inheriting
parent access. Native Windows ACL execution remains unverified. This protection
does not make file writes or project generation transactional: an I/O failure
can still leave a partial project.
Protect parent directories as well. `.gitignore` excludes `.env`;
`.env.example` disables debug. The starter needs no secrets or encryption keys,
so none are provisioned.

`bootstrap.Compose` uses `compose.UseDebug(MCP(routes))` to select the diagnostics
composer. That callback constructs and mounts the endpoint at
`http://127.0.0.1:8080/mcp`, sharing
the main listener and inherited router middleware. There is no authentication:
anyone who can reach that listener can read logs and telemetry attributes as well
as route diagnostics. Keep debug off in
production and bind development listeners to loopback.
Its read-only `http.routes` tool lists registrations, handler identities, and
source paths; it does not invoke HTTP routes. From the project root, inspect
the running service with:

```sh
arkoren http:routes
arkoren app:traces
arkoren app:logs
arkoren app:metrics
```

Send a request to `/` before inspecting traces or metrics. The router's tracing
middleware records request spans and metrics, returns a `traceresponse` header,
and ignores incoming trace context. Context-aware application logs carry the
request's trace and span IDs. The debug MCP factory calls `compose.MCPTools("app")`
once to attach application-owned capture and registers its read-only tools directly,
alongside the router tools. All application diagnostics use the `app` prefix.

Capture keeps up to 2000 finished spans, 5000 log records, 1000 metric series per
instrument, and 500 event records in memory. No external backend, exporter,
credentials, or additional dependency is needed; process exit loses the history.
With debug disabled and no configured telemetry driver, the middleware is a
no-op. See [Telemetry](../observability/telemetry.md) to add instrumentation and
[Exposing Diagnostics](../observability/exposing-diagnostics.md) to customize capture.

The CLI reads `ARKOREN_MCP_URL` from `.env`. Update that URL if you change
`HTTP_ADDR`. No token is needed; clear any inherited `ARKOREN_MCP_TOKEN`.
Set `APP_DEBUG=false` in production to omit the diagnostics endpoint.
Add independent encryption or signing keys only when a feature needs them;
see [Application Keys](../encryption/keys.md).

## Building a container

The generated Dockerfile uses BuildKit source mounts and persistent module/compiler caches, then copies
a stripped static binary into a non-root distroless image. The runtime includes CA certificates and
timezone data but no shell or Go toolchain. The build supports Linux cross-compilation through Docker
Buildx `--platform`; CGO-dependent drivers require a different build and runtime image.

With Make and Docker installed, build and run the image from the project root:

```sh
make docker-build
make docker-run
```

Visit `http://127.0.0.1:8080/` to check the welcome page. Stop the container with
Ctrl+C; `--rm` removes it after it exits. `docker-run` uses an existing image, sets
`HTTP_ADDR=:8080` inside the container, disables debug diagnostics, and publishes port 8080
only on host loopback. Override the image with `IMAGE=billing:dev` on both commands,
or change the host port with `make docker-run PORT=8081`.
Commit `go.mod` and `go.sum`. Pin base images
by digest when you need reproducible production builds.

`.gitignore` excludes local environment files, workspace settings, and build/test output but keeps
`.env.example` and `go.sum`. `.dockerignore` excludes local environment files and Git/editor metadata
from the build context. Pass runtime configuration explicitly rather than baking secrets into the image.

## Choosing composition

The starter uses direct constructor calls checked by the Go compiler. It has no
composition selector, generated providers, or DI dependency. Add dependencies
explicitly in `internal/bootstrap` and pass them to their consumers. The
application owns startup and shutdown. If your project later adopts a container,
[Application Composition](../application/composition.md) explains how to
keep graph construction separate from lifecycle ownership.

## Evolving the project

The application files and `AGENTS.md` are yours after generation. Edit, move, or delete them as the service grows; `arkoren new` has no
regeneration or upgrade step. It creates no drivers, feature packages, CLI project configuration,
application generator state, or Git repository. The skill's installation manifest
tracks documentation ownership only. Add other features explicitly when the application needs them.

[Project Layout](project-layout.md) shows how to grow the starter into feature packages.
For the lifecycle and HTTP server options used by the starter, see the [`application` API reference](https://pkg.go.dev/arkoren.dev/core/application)
and the [`server` API reference](https://pkg.go.dev/arkoren.dev/core/http/server).
