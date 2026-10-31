package application

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"arkoren.dev/core/application/internal/health"
	"arkoren.dev/core/application/internal/lifecycle"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// State identifies the application lifecycle phase.
//
// State values are stable lowercase strings suitable for diagnostics and JSON.
// Empty input retains the meaning of StateNew when formatted with String.
//
// Example: _ = application.State("running") == application.StateRunning
type State string

// Environment names a deployment for configuration and diagnostic output.
// New resolves the empty value to Development; custom nonempty names are kept
// unchanged and use the JSON default log handler, like Staging and Production.
// Environment matching during composition is exact and case-sensitive.
//
//	app := application.New(func(app *application.Builder) {
//		app.Environment(application.Production)
//	})
//	fmt.Println(app.Environment())
type Environment string

const (
	// Development identifies local development. It is the default.
	// It selects the text log handler but does not enable debug logging.
	//
	// Example: app := application.New(); _ = app.Environment() == application.Development
	Development Environment = "development"

	// Staging identifies a pre-production deployment.
	// Like other nondevelopment environments, it selects JSON logging.
	//
	//
	//	app := application.New(func(app *application.Builder) {
	//		app.Environment(application.Staging)
	//	})
	//	fmt.Println(app.Environment())
	Staging Environment = "staging"

	// Production identifies a production deployment.
	// It selects JSON logging; access controls and debug policy remain explicit.
	//
	//
	//	app := application.New(func(app *application.Builder) {
	//		app.Environment(application.Production)
	//	})
	//	fmt.Println(app.Environment())
	Production Environment = "production"
)

// Application coordinates startup hooks, runners, health checks, lifecycle
// events, and shutdown. Private lifecycle and health modules synchronize their
// state without holding locks while user callbacks run. Start and Run wire
// concrete dependencies before startup. Construction configures settings only.
//
//	app := application.New()
//	if err := app.Run(ctx, func(composer *application.Composer) {
//		composer.Run(serve)
//	}); err != nil {
//		return err
//	}
type Application struct {
	// events receives lifecycle events. It is never nil.
	events *event.Bus

	// logger reports lifecycle progress and listener failures. It is never
	// nil, carries the env attribute, and is always wrapped by the telemetry
	// handler. Span contexts are correlated even before startup; export and
	// diagnostic capture require started telemetry. It never changes after New.
	logger *slog.Logger

	// bareLogger is logger without the telemetry handler. Telemetry
	// drivers receive it so their own failures are never exported again.
	bareLogger *slog.Logger

	// telemetry is the stable, non-nil telemetry handle. It remains a no-op
	// unless a driver is configured or Composer.MCPTools attaches capture.
	telemetry *telemetry.Telemetry

	// telemetryConfigured reports whether the application starts and shuts
	// telemetry down: a driver was configured, or Composer.MCPTools requested
	// capture. Composition finishes before this setting is used for startup.
	telemetryConfigured bool

	// setup is the Setup handed to the telemetry driver on Start.
	setup telemetry.Setup

	// level is the minimum level shared by the default handler and any
	// handler built by Builder.LogHandler.
	level slog.LevelVar

	// environment is the deployment environment.
	environment Environment

	// debug reports whether debug logging was requested at construction.
	debug bool

	// shutdownTimeout bounds the shutdown performed by Run.
	shutdownTimeout time.Duration

	// health owns registered checks and their latest background results.
	health *health.Monitor

	// healthDetails reports whether the verbose readiness probe shows check
	// errors.
	healthDetails bool

	// name is the service name.
	name string

	// version is the service version.
	version string

	// build describes the running binary, read once by New.
	build BuildInfo

	// execution owns lifecycle state and synchronous composition permissions.
	execution *lifecycle.Runtime
}

const (
	// StateNew identifies an application that has not started.
	//
	// Example: _ = application.New().State() == application.StateNew
	StateNew State = "new"

	// StateStarting identifies an application composing dependencies or
	// executing startup callbacks.
	//
	// Example: _ = application.StateStarting.String() // "starting"
	StateStarting State = "starting"

	// StateRunning identifies an application whose runners are live.
	//
	// Example: _ = application.StateRunning.String() // "running"
	StateRunning State = "running"

	// StateStopping identifies an application draining runners, releasing
	// acquired resources, and finalizing telemetry.
	//
	// Example: _ = application.StateStopping.String() // "stopping"
	StateStopping State = "stopping"

	// StateStopped identifies an application whose cleanup and telemetry
	// finalization have finished.
	//
	// Example: _ = application.StateStopped.String() // "stopped"
	StateStopped State = "stopped"

	// StateFailed identifies an application whose startup failed and was
	// rolled back.
	//
	// Example: _ = application.StateFailed.String() // "failed"
	StateFailed State = "failed"
)

// String returns the state's wire value unchanged. An empty State denotes a
// new application for compatibility with the original zero-value state.
// Unknown nonempty values are preserved for diagnostics.
//
// Example: _ = application.StateRunning.String() // "running"
func (state State) String() string {
	if state == "" {
		return "new"
	}

	return string(state)
}

// New creates an application in [StateNew]. Construction callbacks run
// directly once, synchronously in order on one fresh draft. Supplied nil
// callbacks and callback panics propagate; omitted callbacks and expanded
// nil/empty slices make no edits. Retained drafts cannot change the result.
// It does no lifecycle I/O; the configured LogHandler factory runs once.
//
//	app := application.New(func(app *application.Builder) {
//		app.Name("billing")
//	})
func New(configure ...BuilderFunc) *Application {
	config := newSettings(configure...)

	application := &Application{
		shutdownTimeout: config.ShutdownTimeout,

		healthDetails: config.HealthDetails,
		name:          config.Name,
		version:       config.Version,
		build:         newBuildInfo(debug.ReadBuildInfo()),
		environment:   config.Environment,
		debug:         config.Debug,
	}

	application.level.Set(slog.LevelInfo)

	if application.debug {
		application.level.Set(slog.LevelDebug)
	}

	build := config.LogHandler

	if build == nil {
		build = func(level slog.Leveler) slog.Handler {
			return newDefaultHandler(application.environment, level)
		}
	}

	handler := build(&application.level)

	application.telemetryConfigured = config.Telemetry != nil
	application.telemetry = config.Telemetry

	if !application.telemetryConfigured {
		application.telemetry = telemetry.New(nil)
	}

	// The logger is always wrapped, even without telemetry: the wrapper only
	// exports or feeds observers while telemetry is started, so a logger taken
	// before [Composer.MCPTools] still reaches diagnostic capture.
	application.bareLogger = tagLogger(slog.New(handler), config)
	application.logger = tagLogger(slog.New(application.telemetry.Handler(handler)), config)
	application.setup = telemetry.Setup{
		Resource: telemetry.Resource{
			Service:     config.Name,
			Version:     config.Version,
			Environment: string(config.Environment),
		},
		Logger: application.bareLogger,
	}

	application.events = event.New(func(events *event.Builder) {
		events.Logger(application.logger)
	})

	application.health = health.New(config.ReadinessInterval, config.ReadinessTimeout, application.healthTransitions)
	application.execution = lifecycle.New(application.logger, config.ShutdownTimeout,
		application.lifecycleSignal, application.startTelemetry, application.shutdownTelemetry,
		application.health.Runner, application.health.Reset)

	return application
}

// tagLogger adds the env, service, and version attributes to logger. Service
// and version are added only when set.
func tagLogger(logger *slog.Logger, config settings) *slog.Logger {
	logger = logger.With("env", string(config.Environment))

	if config.Name != "" {
		logger = logger.With("service", config.Name)
	}

	if config.Version != "" {
		logger = logger.With("version", config.Version)
	}

	return logger
}

// startTelemetry starts configured telemetry after successful composition and
// before Starting listeners and user Startup callbacks. Cancellation prevents
// admission; driver panics retain startup's lifecycle panic-to-error contract.
func (application *Application) startTelemetry(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if !application.telemetryConfigured {
		return nil
	}

	return lifecycle.Protect(func() error {
		return application.telemetry.Start(ctx, application.setup)
	})
}

// shutdownTelemetry shuts telemetry down. It runs outside the hook list,
// after the final lifecycle log and event, so that those and every stop
// hook's telemetry are still exported. It gets the remaining budget of ctx
// but at least [cleanupTimeout], and a failure is logged on the bare logger
// and returned.
func (application *Application) shutdownTelemetry(ctx context.Context) error {
	if !application.telemetryConfigured {
		return nil
	}

	budget := cleanupTimeout

	if deadline, ok := ctx.Deadline(); ok {
		budget = max(budget, time.Until(deadline))
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()

	err := lifecycle.Protect(func() error {
		return application.telemetry.Stop(ctx)
	})

	if err != nil {
		application.bareLogger.ErrorContext(ctx, "telemetry shutdown failed", "err", err)
	}

	return err
}

// Logger returns the stable application logger, tagged with env and configured
// service/version attributes. A valid span context adds trace_id and span_id
// to local output even before startup or without application telemetry.
// Export and diagnostic capture begin only when telemetry starts. A logger
// obtained before Composer.MCPTools still reaches capture attached later.
// Pass this handle to services instead of the application.
//
//	app := application.New()
//	app.Logger().InfoContext(ctx, "billing configured")
func (application *Application) Logger() *slog.Logger {
	return application.logger
}

// Telemetry returns the stable, non-nil application telemetry handle. Without
// Builder.Telemetry or Composer.MCPTools it records nothing. Diagnostic capture
// enables local recording without requiring a primary export driver; recording
// starts after composition. The application owns the handle's lifecycle.
// This is not the process default unless SetDefaults publishes it.
//
//	handle := app.Telemetry()
//	ctx, span := handle.Tracer("example.com/billing").Start(ctx, "billing charge")
//	defer span.End()
func (application *Application) Telemetry() *telemetry.Telemetry {
	return application.telemetry
}

// SetDefaults publishes the application's logger and telemetry as the
// process-wide defaults with [slog.SetDefault] and [telemetry.SetDefault], so
// code that logs or traces without being handed them is correlated and
// exported. It is a method, not an option, so the global mutation is visible
// at the call site. It publishes the same handles, so diagnostic capture attached
// later during composition also reaches callers using the process defaults.
// Without a driver or diagnostic capture, the published telemetry records nothing.
//
//	app := application.New()
//	app.SetDefaults()
//	slog.InfoContext(ctx, "using application defaults")
func (application *Application) SetDefaults() {
	slog.SetDefault(application.logger)
	telemetry.SetDefault(application.telemetry)
}

// Environment returns the construction-time deployment name. Empty configuration
// has already resolved to Development; custom nonempty names are unchanged.
//
// Example: app := application.New(); fmt.Println(app.Environment())
func (application *Application) Environment() Environment {
	return application.environment
}

// Debug reports the construction-time debug flag, independently of environment
// and changes made through SetLogLevel. It controls Composer.UseDebug selection.
//
// Example: app := application.New(); fmt.Println(app.Debug()) // false
func (application *Application) Debug() bool {
	return application.debug
}

// SetLogLevel changes the minimum log level at runtime. It affects
// every handler built on the application's level variable, without changing
// Debug or the construction-time decisions made by Composer.UseDebug.
//
// Example: app := application.New(); app.SetLogLevel(slog.LevelWarn)
func (application *Application) SetLogLevel(level slog.Level) {
	application.level.Set(level)
}

// Events returns the bus that receives the lifecycle topics [Starting],
// [Started], [Stopping], [Stopped], and [Failed], and the readiness topics
// [Ready], [Unready], [CheckFailed], and [CheckRecovered]. Pass it to the Events
// builder setting of Arkoren packages so they publish their topics on it, and to
// application code that listens or dispatches its own topics, instead of
// passing the application. Only the application dispatches the lifecycle
// and readiness topics.
//
//	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
//		consumer.Events(app.Events())
//	})
func (application *Application) Events() *event.Bus {
	return application.events
}

// State returns the current lifecycle phase under the application's state lock.
// StateStopped is published only after cleanup and telemetry finalization.
//
// Example: app := application.New(); fmt.Println(app.State()) // new
func (application *Application) State() State {
	state, _ := application.execution.Snapshot()

	return State(state)
}

// Live reports whether the application is running, no runner has failed,
// and the runner group has not finished. It does not run health checks or
// inspect their cached verdict; use ReadyHandler for traffic readiness.
//
// Example: app := application.New(); fmt.Println(app.Live()) // false
func (application *Application) Live() bool {
	return application.execution.Live()
}

// Start composes dependencies synchronously in order, starts telemetry, then
// executes Startup callbacks in order and launches runners and health checks.
// All composers finish before any Startup callback runs. On failure or context
// cancellation every registered Cleanup runs once in reverse order with a
// bounded uncanceled context, followed by telemetry finalization.
// Failures are logged by the application before telemetry finalization; errors
// before telemetry starts reach the configured log handler only.
// Starting is dispatched after composition and telemetry startup, before user
// Startup callbacks, so its listeners can log through active telemetry.
//
// ctx supplies startup cancellation only; synchronous composition and startup
// callbacks must cooperate with it. Runners use a context that keeps ctx's values
// but is canceled solely by Shutdown or a runner failure. Composition
// programmer panics trigger best-effort rollback and are re-panicked; Startup,
// runner, and Cleanup panics retain the lifecycle panic-to-error behavior.
// Do not call Shutdown synchronously from composition or Startup.
//
//	err := app.Start(ctx, func(composer *application.Composer) {
//		composer.Startup(connect)
//		composer.Cleanup(disconnect)
//	})
//	if err != nil {
//		return err
//	}
func (application *Application) Start(ctx context.Context, compose ...ComposerFunc) error {
	return application.execution.Start(ctx, application.composers(compose)...)
}

// Wait blocks until the runner group finishes or ctx ends, then returns the
// first runner failure, if any. Without runners it blocks until ctx ends or
// Shutdown begins. Caller cancellation alone is a normal wait completion and
// does not return ctx.Err or stop the application. Call Shutdown afterwards,
// or use Run to combine startup, waiting, and shutdown.
//
//	if err := app.Wait(ctx); err != nil {
//		return errors.Join(err, app.Shutdown(context.WithoutCancel(ctx)))
//	}
//	return app.Shutdown(context.WithoutCancel(ctx))
func (application *Application) Wait(ctx context.Context) error {
	return application.execution.Wait(ctx)
}

// Run starts the application, waits for runners or ctx, then shuts down with
// the configured shutdown timeout. Composition has the same ordering, failure,
// and panic semantics as [Application.Start].
// Lifecycle failures are already logged when Run returns. Telemetry has been
// finalized, so callers should use the result for exit status or recovery rather
// than logging it again. Telemetry finalization failures reach the log handler
// directly because export is no longer available.
//
//	if err := app.Run(ctx, func(composer *application.Composer) {
//		composer.Run(serve)
//	}); err != nil {
//		return err
//	}
func (application *Application) Run(ctx context.Context, compose ...ComposerFunc) error {
	return application.execution.Run(ctx, application.composers(compose)...)
}

// composers adapts callbacks while retaining one public composer per execution.
func (application *Application) composers(compose []ComposerFunc) []lifecycle.ScopeFunc {
	composer := &Composer{application: application}
	callbacks := make([]lifecycle.ScopeFunc, len(compose))

	for index, callback := range compose {
		callbacks[index] = func(scope *lifecycle.Scope) {
			composer.scope = scope
			callback(composer)
		}
	}

	return callbacks
}

// Shutdown cancels the runners, waits for them until ctx ends, and then runs
// every cleanup callback exactly once in reverse order, even if waiting
// timed out. Cleanup receives an uncanceled context with the remaining budget
// of ctx but at least ten seconds. The application ends in [StateStopped] only
// after telemetry finalization. Concurrent and
// later calls return the result of that single pass. Shutdown after a failed
// Start returns nil because Start already cleaned up.
//
// When telemetry is configured it is shut down last, after the "app stopped"
// log and the [Stopped] event, with the remaining budget of ctx but at least
// ten seconds, on every exit path; its error is joined into the result.
// These budgets are context deadlines, not forced interruption: a callback
// that ignores cancellation can keep cleanup or finalization from returning.
//
// While starting, Shutdown cancels startup and waits for its owner to roll back;
// ctx bounds that wait. Shutdown must not be called synchronously from
// composition, Startup, a runner, or a Cleanup callback.
//
//	if err := app.Shutdown(ctx); err != nil {
//		return err
//	}
func (application *Application) Shutdown(ctx context.Context) error {
	return application.execution.Shutdown(ctx)
}

// lifecycleSignal maps private execution outcomes to the public typed topics.
func (application *Application) lifecycleSignal(ctx context.Context, phase string, err error) {
	switch phase {
	case "starting":
		application.events.Signal(ctx, Starting)
	case "started":
		application.events.Signal(ctx, Started)
	case "stopping":
		application.events.Signal(ctx, Stopping)
	case "stopped":
		application.events.Signal(ctx, Stopped)
	case "failed":
		application.events.Emit(ctx, Failed, FailedEvent{Err: err})
	}
}

var (
	// ErrInvalidPhase reports an operation outside its lifecycle phase.
	// Lifecycle methods return it wrapped; invalid composer registration panics
	// with a wrapping error instead. Use errors.Is rather than string matching.
	//
	//
	//	if err := app.Start(ctx); err != nil {
	//		if errors.Is(err, application.ErrInvalidPhase) {
	//			return fmt.Errorf("startup requires a new application: %w", err)
	//		}
	//		return err
	//	}
	ErrInvalidPhase = lifecycle.ErrInvalidPhase

	// ErrNilFunc reports a registration without a function.
	// Composer registration panics with a wrapping error for nil callbacks or
	// empty check names; it is programmer misuse, not a runtime retry signal.
	//
	// Example: matched := errors.Is(registrationError, application.ErrNilFunc); fmt.Println(matched)
	ErrNilFunc = lifecycle.ErrNilFunc

	// ErrDuplicateCheck reports a health check registered under a name that
	// is already taken.
	// Composer.Check panics with a wrapping error before appending the duplicate.
	//
	// Example: matched := errors.Is(registrationError, application.ErrDuplicateCheck); fmt.Println(matched)
	ErrDuplicateCheck = health.ErrDuplicateCheck
)
