# Own a service lifecycle

Use `application` when one process must start dependencies in order, supervise
workers, and release acquired resources on failure or shutdown. Construct
dependencies explicitly. A `Composer` registers lifecycle work; the application
controls execution and exposes logging, events, telemetry, and probes.

## Register work with its cleanup owner

Given a configured, unstarted `*server.Server` named `listener`, this function
takes ownership of its lifecycle. `Run` returns after startup failure or after
waiting and shutdown; the caller uses its error for exit status or recovery.
The snippet uses `context`, `arkoren.dev/core/application`, and
`arkoren.dev/core/http/server`.

```go
func runService(ctx context.Context, listener *server.Server) error {
	app := application.New()

	return app.Run(ctx, func(compose *application.Composer) {
		compose.Startup(listener.Start)
		compose.Cleanup(listener.Stop)
		compose.Run(listener.Serve)
	})
}
```

At a process entry point, obtain `ctx` with `application.NewSignalContext` and
immediately defer its stop function.

All composition finishes before telemetry and startup callbacks run. Cleanup is
active as soon as it is registered and executes once in reverse order, including
when a later composition or startup callback fails. Register releases at the
acquiring owner, not at every borrower. `Defer` puts an infallible release on this
same stack; it does not run at the end of the composing Go function.

An executing startup callback can register `Cleanup` or `Defer` for resources it
acquires. It cannot add startup callbacks, workers, checks, or diagnostic tools.
`Must`, `MustDefer`, `Require`, and child composition are composition-only.

Cancellation is cooperative. Runners and checks must honor their contexts; the
application does not forcibly interrupt arbitrary callbacks. Shutdown can stop
waiting for runners when its context expires, but still attempts acquired cleanup.
Cleanup and telemetry finalization receive independent minimum budgets, so the
configured runner-wait timeout is not a hard wall-clock bound on `Run`.

## Distinguish readiness from liveness

Register dependency checks during composition, and mount probes explicitly on
your router. Reading a readiness probe never executes a dependency check.

```go
compose.Check("database", connection.Ping)
routes.Get(application.LivePath, app.LiveHandler(), application.LiveOperation)
routes.Get(application.ReadyPath, app.ReadyHandler(), application.ReadyOperation)
routes.Get(application.StartupPath, app.StartupHandler(), application.StartupOperation)
```

Here `connection` supplies `Ping(context.Context) error`, `routes` is your router,
and `app` is the composing application. The first background pass starts
immediately; later passes follow a ticker, not a full pause after each completion.
Passes do not overlap. A result older than three readiness intervals is stale and
does not permit traffic, even if its last verdict passed. `RunChecks` and
`CheckHealth` explicitly run checks without replacing the background cache.

`?verbose` returns cached readiness details. Error text is withheld unless you
enable `HealthDetails`, which also affects MCP health diagnostics. Enable that
setting only when all affected endpoints are operator-protected.
The separate `InfoHandler` also needs operator access because it exposes build
and runtime identity. The welcome handler includes neither kind of information.

## Capture diagnostics without changing lifecycle ordering

During composition, `compose.MCPTools("app")` attaches local diagnostic capture
and returns SDK-independent tools. It does not mount a transport or establish
authentication or authorization. Select tools, apply capability policy, and
register them on a protected MCP endpoint in your composition.

```go
tools := compose.Must(compose.MCPTools("app"))
_ = tools // Register the selected tools with your MCP adapter here.
```

Capture activates after composition, before startup callbacks. A logger obtained
earlier reaches that capture once telemetry starts. Without an explicit driver
or diagnostic capture, application telemetry records nothing. Local log
correlation is separate: a valid span context adds trace and span IDs even before
application startup.

The `Stopped` event runs after runner waiting and cleanup, while state is still
`StateStopping` and telemetry can accept a listener's final records. Telemetry
finalization and publication of `StateStopped` follow. To observe `Stopped` or
rollback `Failed` events, keep the subscription until `Run` returns; do not
unsubscribe it on the application's cleanup stack before the event is emitted.

The application reports lifecycle failures before telemetry finalization.
Finalization errors go to the configured local handler instead of back to the
failed exporter. Do not duplicate this reporting by logging `Run`'s result after
telemetry has stopped.

See the [application API](https://pkg.go.dev/arkoren.dev/core/application) for
construction defaults, callback phases, event payloads, and examples.
