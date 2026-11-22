// Package application coordinates ordered startup, workers, health checks,
// lifecycle events, and graceful shutdown. It has no dependency-injection layer;
// construct objects explicitly inside a [Composer] passed to [Application.Start]
// or [Application.Run]:
//
//	app := application.New()
//	err := app.Run(ctx, func(composer *application.Composer) {
//		composer.Startup(connect)
//		composer.Cleanup(disconnect)
//		composer.Check("database", ping)
//		composer.Run(serve)
//	})
//	if err != nil {
//		return err
//	}
//
// A process entry point can build the application from the composition graph
// instead. The application is a [Provider], so its settings take handles such
// as a log handler or telemetry from ordinary providers, and [Run] (or [Start],
// which returns the started application) builds it, composes the process on the
// same composer and runs it:
//
//	var App = application.Provide(func(compose *application.Composer) *application.Application {
//		return application.New(func(app *application.Builder) {
//			app.Telemetry(compose.Use(Telemetry))
//		})
//	})
//
//	err := application.Run(ctx, App, Compose)
//
// Cleanup registered by providers while the application is built is owned by
// the application. [Composer.App] panics with [ErrProviderCycle] until the
// build finishes; a failed build is reported once on stderr, since no
// application logger exists yet.
//
// An [Application] is optional: the router, server, and services work
// without one.
// At the process entry point, [NewSignalContext] requests graceful shutdown on
// SIGINT or SIGTERM using standard os/signal cancellation, without terminal
// output changes. Always defer its stop function to release signal handling:
//
//	ctx, stop := application.NewSignalContext(context.Background())
//	defer stop()
//	if err := app.Run(ctx, compose); err != nil {
//		return err
//	}
//
// [Composer.Startup], [Composer.Cleanup], [Composer.Defer], [Composer.Run], and
// [Composer.Check] return nothing. They panic with [ErrNilFunc] for a
// nil function (or an empty check name) and with [ErrInvalidPhase] when
// called in a phase that does not allow registration. The panic value is
// an error wrapping the sentinel, so errors.Is works on the recovered
// value. Both are wiring mistakes found at startup, not runtime failures.
//
// Composition callbacks execute synchronously, in order, before any startup
// hook. [Composer.App] exposes the existing application services, and
// [Composer.Context] carries startup cancellation. [Composer.Must] unwraps constructor
// results; [Composer.Require] aborts on an error. Both preserve the original
// error for Start or Run. Ordinary composition panics trigger cleanup and
// propagate unchanged, rather than being converted into lifecycle errors.
// [Composer.Defer] registers an infallible func() release on the same cleanup
// stack; [Composer.MustDefer] combines (release, error) acquisition with that
// registration, aborting composition on error. Neither release runs at the end
// of the enclosing Go function. MustDefer is composition-only like Must and
// Require; Defer is also allowed inside executing Startup callbacks.
//
// Composition values come in three kinds. A value the process must hold exactly
// once, such as the router or a database, is a [Provider] declared with
// [Provide]; [Composer.Use] runs its build function on first use in a
// composition and returns the cached value afterwards, from any nested provider.
// Every other value is an ordinary Go constructor. A constructor that acquires
// resources is an exported New function taking the composer, for example
// NewWorker(composer, args...), which returns a fresh instance and registers its
// own lifecycle. Conditional wiring is a plain if on [Application.Debug] or
// [Application.Environment], reached through [Composer.App]. Use is valid only
// during composition. Root [ComposerFunc] callbacks remain ordinary void
// functions accepted directly by Start and Run:
//
//	var Router = application.Provide(func(composer *application.Composer) *router.Router {
//		return router.New()
//	})
//
//	err := app.Start(ctx, func(composer *application.Composer) {
//		routes := composer.Use(Router)
//		routes.Get(application.LivePath, app.LiveHandler())
//		if composer.App().Debug() {
//			routes.Get("/debug", debugHandler)
//		}
//	})
//	if err != nil {
//		return err
//	}
//	// The owner must eventually call app.Shutdown with a shutdown context.
//
// The cache belongs to one Start or Run, so separate applications and
// compositions never share values. A provider re-entered while it is still
// building panics with [ErrProviderCycle] naming the chain of value types; a
// nil build function panics with [ErrNilFunc] at [Provide]. A build that aborts
// through [Composer.Must] or [Composer.Require] caches nothing. Returning a
// value does not start or stop it: register cleanup once, immediately after
// acquisition, in the build function or constructor that owns the resource.
//
// Because Startup hooks run in registration order and Cleanup in reverse, a
// provider that calls Use on its dependencies before registering its own
// lifecycle callbacks has its dependencies started first and stopped last.
//
// [Composer.Startup] hooks run sequentially in registration order.
// [Composer.Cleanup] and [Composer.Defer] releases become active at registration and
// run in reverse order exactly once, including composition and startup failure.
// Cleanup is not paired positionally with startup hooks. A startup hook may
// register cleanup for a resource it acquired, but cannot add startup hooks,
// runners, checks, or diagnostic tools. Retained composers reject registration
// outside their permitted callback phase; they are not runtime service handles.
//
// [Composer.Run] runners launch only after every start hook succeeded,
// on a context detached from the start context. A runner that returns an
// error or panics while not canceled fails the application and cancels the
// others. [Application.Shutdown] cancels the runners, waits for them, and
// then runs acquired cleanup. [Application.Run] combines Start, Wait, and
// Shutdown. [Builder.ShutdownTimeout] supplies the runner-wait budget; cleanup
// and telemetry finalization retain their minimum budgets. Callbacks must honor
// cancellation, so these deadlines do not forcibly interrupt user code.
//
// [Application.CheckHealth] runs the checks registered with
// [Composer.Check]; check names are unique. [Application.LiveHandler],
// [Application.ReadyHandler], and [Application.StartupHandler] are
// [router.Handler] probes that answer "ok" or "unavailable".
// [Application.ReadyHandler] never runs checks per request: while the
// application runs, a background runner runs every check every
// [Builder.ReadinessInterval] with a context deadline from
// [Builder.ReadinessTimeout], and the handler reads the latest result. Checks
// must honor cancellation; the application waits for them rather than forcibly
// interrupting callbacks. With ?verbose it answers the same status
// with a JSON report of each check, withholding errors unless
// [Builder.HealthDetails] is enabled. The runner logs each check that starts
// failing or recovers. Register the probes at [LivePath], [ReadyPath], and
// [StartupPath]:
//
//	r.Get(application.LivePath, app.LiveHandler())
//	r.Get(application.ReadyPath, app.ReadyHandler())
//	r.Get(application.StartupPath, app.StartupHandler())
//
// [Application.WelcomeHandler] serves a self-contained HTML welcome page with
// only the escaped application name and Arkoren resource links. It includes no
// runtime, configuration, or build details. Register it explicitly at a chosen
// path; creating an application or handler never mounts routes:
//
//	r.Get("/", app.WelcomeHandler(), application.WelcomeOperation)
//
// [Application.InfoHandler] is a separate opt-in handler that answers JSON
// describing the service (name,
// version, environment, state, log level, uptime) and the running binary
// (Go version, module, VCS revision). Register it at [InfoPath]. Verbose
// readiness and info reveal internals and are not protected; serve them
// where only operators can reach them, such as a second server on an
// internal port. [Application.Info] returns the same description as a
// [Info] value, and [Application.Checks] the cached state of each health
// check, for tools that inspect the running service.
//
// [Composer.MCPTools] attaches bounded diagnostic capture during composition,
// before startup hooks, and returns native MCP tools over application state and
// telemetry. Composition selects a subset, applies capability policy, and
// registers those tools on a protected MCP endpoint; the factory creates no
// HTTP listener or authentication policy. Resource authorization remains in
// shared typed application methods.
//
// Example:
//
//	var tools []mcp.Tool
//	err := app.Start(ctx, func(composer *application.Composer) {
//		tools = composer.Must(composer.MCPTools("app"))
//		// Select and register tools through the application's MCP adapter.
//	})
//	if err != nil {
//		return err
//	}
//	_ = tools
//
// The application dispatches [Starting], [Started], [Stopping],
// [Stopped], and [Failed] on [Application.Events], and the readiness
// runner dispatches [Ready], [Unready], [CheckFailed], and [CheckRecovered]
// on each transition. Pass Events() to the Events builder setting of components
// and to application code instead of the application; only the application
// dispatches its own topics. Listener errors are logged and
// never affect the lifecycle. The bus is an [arkoren.dev/core/event.Bus];
// see that package for topics, listeners, and name rules.
//
// The application owns a [slog.Logger], returned by [Application.Logger]
// and tagged with an env attribute. [Builder.Environment] selects the
// [Environment] ([Development], [Staging], or [Production]); development
// logs text with source locations to stderr, every other environment
// logs JSON. [Builder.Debug] lowers the level to debug, and
// [Application.SetLogLevel] changes it at runtime. [Builder.LogHandler]
// replaces the handler; its factory receives the shared level. The
// lifecycle is logged as "app started", "app stopping",
// "app stopped", and "app failed". Shutdown wait and cleanup failures are logged
// as "app shutdown failed" before telemetry stops. Rollback includes cleanup
// failures in its "app failed" record. Callers use Run's error for exit status
// or recovery rather than logging it again after telemetry finalization.
// Pass Logger() to services, never the application.
//
// [Builder.Name] and [Builder.Version] identify the service: when set, the logger
// also carries service and version attributes next to env, and telemetry
// drivers receive them in their resource. Without [Builder.Version] the version
// is the main module version the go command stamped into the binary, unless
// it is unknown ("(devel)").
//
// [Builder.Telemetry] enables telemetry with a [telemetry.Telemetry] built by
// [telemetry.New] from a driver (the in-process memory driver, a composed set
// of drivers, ...). The application owns its lifecycle: telemetry starts
// after all composition succeeds and before user Startup callbacks.
// Starting is dispatched after telemetry startup and before user Startup
// callbacks, so listeners registered during composition can log through active
// telemetry. Composition or telemetry startup failure dispatches Failed without
// Starting. There is no built-in "app starting" log.
// Its Stop runs after acquired cleanup and final lifecycle logs/events on every
// exit path (normal stop, runner failure, and failed-start rollback), with the
// remaining shutdown budget but at least ten seconds. The logger's handler is always
// wrapped with [telemetry.Telemetry.Handler], configured or not. Records logged
// with a valid span context carry trace_id and span_id in local output even
// before startup; export and diagnostic capture require started telemetry.
// A logger taken at any time reaches diagnostic capture attached later.
// The driver is started with the
// resource built from the configuration and the unwrapped logger, so
// exporter failures are not exported again. [Application.Telemetry] returns
// the stable application handle, like [Application.Logger]. Without a configured
// driver it remains a no-op unless [Composer.MCPTools] attaches local capture.
// [Application.SetDefaults] publishes the logger and telemetry as the
// process defaults ([slog.SetDefault] and [telemetry.SetDefault]); it is a
// method so the global mutation is visible at the call site.
//
//	app := application.New(
//		func(app *application.Builder) {
//			app.Name("billing")
//			app.Version(version)
//			app.Telemetry(telemetry.New(memorytelemetry.New()))
//		},
//	)
//	app.SetDefaults()
//
// The lifecycle states are [StateNew], [StateStarting], [StateRunning],
// [StateStopping], [StateStopped], and [StateFailed].
//
// # Construction
//
// New invokes callbacks directly once, synchronously in order on one fresh
// Builder, then resolves defaults and wires services. Settings return that
// draft; retained drafts cannot reconfigure an application. Dependencies and
// handler closures remain shared. Omitted callbacks and nil/empty expanded
// slices make no edits; supplied nil entries and callback panics propagate.
// [Composer.MCPTools] has its own [MCPToolsBuilder] for capture [CaptureLimits].
// While composition is active, it runs callbacks once outside the lifecycle lock and
// publishes SDK-independent owner tools for explicit application composition.
// Authentication, capability policies, and transport registration are external.
//
//	err := app.Run(ctx, func(composer *application.Composer) {
//		tools := composer.Must(composer.MCPTools("app"))
//		tools = append(tools, public.MCPTools("public")...)
//		// Register the selected tools on a protected endpoint here.
//		_ = tools
//	})
//	if err != nil {
//		return err
//	}
package application
