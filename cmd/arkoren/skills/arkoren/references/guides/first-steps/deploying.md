---
title: Deployment
description: Build and run a service with explicit production settings, health probes, bounded shutdown, and application-owned credentials.
order: 60
summary:
  - Build a binary or use the starter's non-root container image.
  - Disable development diagnostics and supply runtime configuration outside the image.
  - Connect startup, liveness, and readiness probes to the correct platform decisions.
  - Budget HTTP draining, resource cleanup, and telemetry finalization before rollout.
---

# Deployment

Deploy the application you have already tested, not a different production
bootstrap. The [generated service](creating-a-project.md) connects server startup,
serving, cleanup, and signal handling; your platform supplies runtime settings,
credentials, traffic routing, and a termination grace period.

This page assumes that starter or equivalent `application.Application` wiring.
Keep driver-specific setup in its owning guide and decide
[migration rollout](../migrations/deployment.md) before deploying schema changes.

## Binary Builds

From your application's module root, build the server executable:

```sh
CGO_ENABLED=0 go build -trimpath -o bin/server ./cmd/server
```

This targets the build machine's OS and architecture. Select `GOOS` and `GOARCH`
for a different target, or use the container build below. Core does not require
cgo, but confirm that every dependency you select supports this build mode.
CGO-dependent libraries need a suitable builder and runtime image instead.

Commit `go.mod` and `go.sum`, use a supported patched Go toolchain, and rebuild
when the Go runtime, standard library, or compiled dependencies need security
updates. Patching the host or replacing a runtime image does not patch code
already inside the binary. Test the resulting artifact on the target platform.

## Production Settings

Before starting the process, supply required application settings through your
platform's environment or your own validated settings loader. Core does not
automatically read environment variables. The starter explicitly reads
`HTTP_ADDR` and `APP_DEBUG`; vendor SDK credential chains can have additional
documented sources.

> [!WARNING]
> The starter's debug `/mcp` endpoint has no authentication and can reveal logs
> and telemetry attributes. Set `APP_DEBUG=false` explicitly in production.
> Selecting the production environment alone does not disable debug.

For a local check of the built binary, bind to loopback and disable debug:

```sh
APP_DEBUG=false HTTP_ADDR=127.0.0.1:8080 ./bin/server
```

The executable still autoloads `.env` from its working directory, without
overriding existing environment values. Do not depend on a development file for
production configuration. Explicit process values above override that file.
The platform must select an appropriate listener address: a container usually
needs `:8080` inside its network namespace, while a host-local reverse proxy can
use loopback. Empty `HTTP_ADDR` selects `:8080` on all interfaces.

Set the application environment in code if you want production metadata. In
`cmd/server/main.go`'s existing `run` helper, replace its construction statement
with the following, preserving signal setup and the call to `app.Run`:

```go
app := application.New(bootstrap.Application(), func(app *application.Builder) {
	app.Environment(application.Production)
})
```

The later callback overrides environment while retaining the starter's name and
logger policy. `APP_ENV` is not a built-in loader. The starter's custom logger
uses JSON when stderr is not a terminal and Tint otherwise; changing environment
does not replace that policy. Without a custom log handler, the application's
default selects source-enabled text for development and JSON otherwise.

Inject passwords, API tokens, and keys outside the image and source tree. The
application owner provisions them and grants the process least-privilege access,
whether through environment values, mounted files, SDK workload identity, or a
[secret store](../secrets/overview.md). Validate required values before serving,
never log them, and do not generate a replacement key to repair a missing startup
secret. Rotation can require coordinated rollout or fallback keys; follow the
feature's credential rules.

## Container Builds

The generated Dockerfile already builds a stripped, cgo-disabled Linux binary
using BuildKit source mounts and module/compiler caches. Its runtime is
`gcr.io/distroless/static-debian13:nonroot`, with CA certificates and timezone
data, but no shell or Go toolchain. The generated `.dockerignore` excludes local
environment files from the build context.

With Docker available, build the image from the project root:

```sh
docker build -t hello:dev .
```

The build needs access to resolve modules unless its caches already contain them.
For a different Linux architecture, use Docker Buildx with the target `--platform`;
the generated build uses its target OS and architecture arguments. Pin base
images by digest when your release process requires reproducible image inputs.

Run a disposable local container, publishing only on host loopback:

```sh
docker run --rm --name hello-demo -p 127.0.0.1:8080:8080 \
  -e HTTP_ADDR=:8080 -e APP_DEBUG=false hello:dev
```

Check `http://127.0.0.1:8080/readyz` from another terminal. Stop the test container
with `docker stop hello-demo`; `--rm` removes it after exit. These commands
create no database volume. The equivalent starter shortcuts are
`make docker-build IMAGE=hello:dev` and `make docker-run IMAGE=hello:dev`.
Runtime credentials belong in the platform's secret mechanism, not Docker build
arguments, copied `.env` files, or committed image layers.

Configure HTTPS at your ingress or explicitly on the [HTTP server](../http-networking/server.md).
The starter does not enable HSTS before you establish that HTTPS policy. It
also does not establish authentication, trusted proxy headers, host validation,
or cross-origin permissions merely by running in a container.

## Health Probes

The starter already mounts the application's three probe handlers. Configure
your platform to use their paths rather than adding duplicate registrations:

| Path | Meaning | Platform Decision |
| --- | --- | --- |
| `/startupz` | Startup has completed successfully. | Allow the startup phase to finish before applying ordinary health thresholds. |
| `/livez` | The application is running and no supervised runner has failed. | Restart an unhealthy process. |
| `/readyz` | The application is live and its latest background health pass is acceptable. | Send or withhold traffic without restarting the process. |

Plain probes return `200` with `ok` or `503` with `unavailable`. Startup stays
successful after completed startup, including while stopping; it is not a traffic
readiness signal. Liveness and readiness become unavailable during shutdown,
although an HTTP response is possible only while the listener can still serve it.

Only readiness depends on registered dependency checks. Inside an existing
composition callback, a driver with `Ping(context.Context) error` can supply one:

```go
compose.Check("catalog", driver.Ping)
```

The application runs checks in the background, every five seconds by default;
probe requests read the latest result instead of calling the backend. With checks
registered, readiness waits for the first successful pass and rejects results
older than three intervals. Without checks, readiness equals liveness. Callbacks
must honor their context deadline; a timeout cannot forcibly interrupt them.
See [Health and Readiness](../application/health-and-readiness.md) for tuning.

Keep operator endpoints off public routes unless you add access control.
`InfoHandler`, verbose readiness (`?verbose`), and diagnostics can reveal internal
metadata. A second port or loopback bind is not authentication; enforce the
actual network and authorization boundary appropriate to your platform.

## Shutdown Budgets

Ensure the platform sends `SIGTERM` to the executable rather than only to a shell
wrapper. The starter calls `application.NewSignalContext`, defers its stop
function, and passes the context to `app.Run`. Keep that signal cleanup even
when startup fails. Lifecycle errors are reported before telemetry finalization;
the executable uses the returned error for status 1 rather than logging it again.

On cancellation, the server runner stops accepting connections and drains
in-flight requests. After runners return, cleanup runs in reverse registration
order. Register backend cleanup before listener cleanup so the backend remains
available while requests finish.

The HTTP drain defaults to 15 seconds. To change it, add a setting inside the
existing constructor callback in `internal/bootstrap/server.go`, with `time`
imported:

```go
listener.ShutdownTimeout(20 * time.Second)
```

The application's runner-wait and shutdown budget defaults to 30 seconds. Change
it in the callback returned by `Application()` in `internal/bootstrap/app.go`:

```go
app.ShutdownTimeout(45 * time.Second)
```

These lines edit existing construction callbacks; they are not runtime setters.
The HTTP drain must fit within the application budget. Cleanup receives at least
a fresh ten-second context when the preceding budget is exhausted. Configured
telemetry finalization runs last with at least another ten seconds. Allow room
for these phases and platform scheduling overhead, not just the application
timeout, when setting Kubernetes `terminationGracePeriodSeconds` or an equivalent
platform grace period. Cleanup callbacks must cooperate with their deadlines.

Long-lived handlers should observe `server.ShuttingDown(r.Context())` and return
when draining begins. Drain timeout closes tracked HTTP connections; it does not
join hijacked connections or forcibly stop arbitrary handler goroutines. Their
owners must coordinate completion before dependencies are released. See
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md).

## Rollout Checks

Before directing production traffic to a release:

- Verify the built artifact, environment, debug setting, and log format on the
  target platform rather than relying only on a successful local build.
- Apply schema changes using the reviewed [migration deployment](../migrations/deployment.md)
  process; driver installation and startup do not create tables automatically.
- Confirm startup and readiness succeed, and that a dependency failure removes
  readiness without turning the dependency into a liveness restart condition.
- Send the platform's termination signal under representative load and verify
  requests drain, drivers stop, and the process exits within the allotted grace.
- Check that secrets are absent from artifacts and logs, and that diagnostics
  and operator details are inaccessible to unauthorized clients.

For the lifecycle and listener contracts behind these checks, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application)
and the [`server` API reference](https://pkg.go.dev/arkoren.dev/core/http/server).
