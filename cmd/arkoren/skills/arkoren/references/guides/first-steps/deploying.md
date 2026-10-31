---
title: Deploying to Production
nav: Deploying
description: Build a service into a small binary or container, configure it through the environment, and wire its probes and shutdown to your platform.
order: 50
---

# Deploying to Production

## Introduction

To deploy an Arkoren service, build its binary, supply configuration, and connect
its health probes and shutdown behavior to your platform. The lifecycle wiring
binds the server's port before the application reports readiness and drains
traffic when the application stops.

The same approach applies to Kubernetes, a container platform, or a virtual
machine running systemd. An Arkoren service can compile to a single static
binary; the build, logging, and migration guidance below does not assume a
particular host.

Start with the [generated service](creating-a-project.md) or equivalent wiring:
an `application.Application`, an HTTP router, and a server whose lifecycle hooks are
registered on that application. Code fragments use `app` for the
application, `r` for the router, and `driver` for a backend that exposes `Ping`.
Import `arkoren.dev/core/application`, `arkoren.dev/core/http/router`, and
`arkoren.dev/core/http/server` where used.

## Building the binary

From your project root, build the generated `cmd/server` executable with the
standard Go toolchain. Adjust the path if you use a different executable.
Disabling cgo produces a
static binary with no dependency on the system's C library, which lets it run
in the smallest container images:

```sh
CGO_ENABLED=0 go build -trimpath -o bin/server ./cmd/server
```

As the application owner, build with a supported, patched Go toolchain and keep
your application's dependencies patched. Updating the host, its Go installation,
or the runtime container image alone does not patch the Go runtime,
standard library, or dependencies already compiled into the executable; rebuild
and redeploy the application to apply those fixes.

Core needs no cgo, and the SQLite driver uses a pure Go implementation of
SQLite. Build your application with the drivers and other dependencies you
actually use to confirm that they support `CGO_ENABLED=0`.

The Go toolchain records the module version and the version control revision
in the binary. Arkoren reads them back: unless you set `application.Builder.Version`, the
application uses a known module version as the `version` attribute on log
records and in telemetry. Local builds commonly have the version `(devel)`,
which Arkoren omits. Set an explicit build version when your deployment process
needs an identifier that the toolchain does not provide.

## Building a container image

A two-stage build compiles the service in a Go image and copies only the
binary into a minimal runtime image:

```dockerfile
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
```

The runtime image contains no shell and runs as a non-root user. If your
service makes outgoing TLS connections, the `static` distroless image already
includes CA certificates.

Commit `go.mod` and `go.sum` so the build resolves the same dependency versions.
The build stage needs network access to download modules unless you vendor them
and include `vendor/` in the build context.

## Configuring the service

Pass application configuration through the environment. Core packages do not
load it for you; your `main` function reads it (usually
through a small `config` package, as shown in
[Project Layout](project-layout.md)) and passes the values to each
constructor as required arguments or builder callbacks. This keeps every setting visible in code and makes the
environment the only thing that differs between staging and production.
Vendor drivers may also use their SDK's default credential and endpoint
sources, as described in their driver guides.

Set the application's environment explicitly in production, because it
changes how the service logs:

```go
app := application.New(func(app *application.Builder) {
	app.Environment(application.Production)
})
```

In the `Development` environment, the application writes human-readable text
logs with source locations. In `Staging` and `Production`, it writes one JSON
object per line to standard error, which log collectors can parse without
configuration. Every record carries `env`, and `service` and `version` when
they are known.

> [!WARNING]
> Keep secrets such as database passwords and API keys out of the image and
> out of your source. Inject them through the environment, or read them at
> startup from a secret store with the [Secrets](../secrets/overview.md)
> service.

## Health probes

Register the application's three probe handlers on the router. Their paths
follow the Kubernetes naming convention, and the constants keep your routes and
your platform configuration in step:

```go
r.Get(application.LivePath, app.LiveHandler())       // /livez
r.Get(application.ReadyPath, app.ReadyHandler())     // /readyz
r.Get(application.StartupPath, app.StartupHandler()) // /startupz
```

Each probe answers `200 OK` with the body `ok`, or `503 Service Unavailable`
with the body `unavailable`. They mean different things, and platforms use
them differently:

- **Liveness** (`/livez`) answers `200` while the application is running and
  none of its runners has failed. A platform restarts the process when this
  probe fails. It never depends on a database or another service, so a slow
  dependency cannot cause a restart loop.
- **Readiness** (`/readyz`) answers `200` while the application is live and
  its latest health checks passed. A load balancer stops sending traffic while
  this probe fails, without restarting anything.
- **Startup** (`/startupz`) answers `200` once startup has finished. Use it on
  platforms that support a separate startup probe, so a slow start is not
  mistaken for a dead process.

The checks behind readiness are the ones you register with
`composer.Check` during composition, typically one per driver:

```go
compose := func(composer *application.Composer) {
	composer.Check("database", driver.Ping)
}
```

The readiness handler never runs these checks when a request arrives. The
application runs every check in the background, every five seconds by default,
and the handler answers from the latest result. A burst of probe requests
therefore never reaches your database, and a result that is too old counts as
not ready. [Health and readiness](../application/health-and-readiness.md) describes the
checks, the intervals, and the detailed JSON report in full.

## Graceful shutdown

When a platform stops a service, it sends `SIGTERM`, waits for a grace period,
and then kills the process. Arkoren handles the signal through the context
you pass to `app.Run`. Import `context` and `arkoren.dev/core/application`.
In a function returning `error`, create the context and pass it to the
application. The generated executable does this in its run helper
and translates the returned error into a process exit status:

```go
ctx, stop := application.NewSignalContext(context.Background())
defer stop()
return app.Run(ctx, bootstrap.Compose)
```

Always call `stop`, including on startup failure or parent cancellation, to
release the process signal registration. The helper does not change terminal
settings or erase the terminal's echoed `^C`.

When the signal arrives, the application stops: liveness and readiness start
answering `503`, signaling the load balancer to stop sending traffic; the server stops
accepting connections and waits for the requests in flight; and then the stop
hooks run in reverse registration order, closing the server, the database
pools, and the other drivers.

The server waits up to 15 seconds for in-flight requests by default.
`app.Run` supplies a 30-second context for runner waiting and stop
hooks. Both settings can be changed during construction; import `time`:

```go
app := application.New(func(app *application.Builder) {
	app.ShutdownTimeout(45 * time.Second)
})
srv := server.New(r, func(listener *server.Builder) {
	listener.ShutdownTimeout(20 * time.Second)
})
```

Choose them so that the server's drain fits inside the application's shutdown
budget. Stop hooks must honor their context. If waiting for runners exhausts
the application budget, the application still invokes stop hooks with a fresh
ten-second cleanup context. Telemetry cleanup runs last with at least ten
seconds, even when the preceding budget has expired. Your platform's grace
period must allow for both cleanup phases and scheduling overhead, not merely
equal the application timeout. On Kubernetes, set
`terminationGracePeriodSeconds` accordingly; its default 30 seconds can cut off
cleanup with the application's default budget.

> [!NOTE]
> Shutdown does not cancel the contexts of requests that are still running.
> Long-lived handlers such as server-sent event streams should watch
> `server.ShuttingDown(ctx)` and return on their own, or they are cut off when
> the drain timeout expires.

## Operator endpoints

The application can describe itself through two endpoints that are useful when
you debug a deployment: `app.InfoHandler()`, which reports the service
name, version, environment, state, and build details, and the readiness probe
with `?verbose`, which returns a JSON report of every check. Both reveal
internal details and are not protected.

To expose them only to operators, serve them on a second server that listens on
an internal port, and register its hooks on the same application:

```go
ops := router.New()
ops.Get(application.InfoPath, app.InfoHandler())
ops.Get(application.ReadyPath, app.ReadyHandler())

listener := server.New(ops, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:9090")
})
composeOps := func(composer *application.Composer) {
	composer.Cleanup(listener.Stop)
	composer.Startup(listener.Start)
	composer.Run(listener.Serve)
}
```

## Running migrations

Decide where schema migrations run before your first deployment. There are two
common choices:

- **At startup**, as a start hook that runs before the server starts. A
  migration failure then prevents the new instance from serving. The
  migrator takes a lock, so several replicas starting at once do not apply the
  same migration twice.
- **As a separate step**, with a second executable such as
  `cmd/migrate` that runs before the new version is rolled out. This keeps
  startup fast and lets you review the migration's effect separately.

[Migrations](../database/migrations.md) shows both setups.

## A deployment checklist

Before you deploy a service for the first time, check that:

- the binary builds with `CGO_ENABLED=0` and runs in your target image;
- the environment is set to `Production`, and logs arrive as JSON;
- each driver's lifecycle hooks and health check are registered when that
  driver provides them; stateless drivers need no hooks;
- the liveness, readiness, and startup probes are configured on the platform
  with the paths above;
- the platform's grace period is longer than the application's shutdown
  timeout, with additional time for telemetry cleanup;
- operator endpoints are only reachable from inside your network;
- secrets come from the environment or a secret store, not from the image.

For lifecycle options and probe handlers, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application). For listener and drain settings,
see the [`server` API reference](https://pkg.go.dev/arkoren.dev/core/http/server).
