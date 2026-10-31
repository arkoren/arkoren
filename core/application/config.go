package application

import (
	"log/slog"
	"os"
	"runtime/debug"
	"time"

	"arkoren.dev/core/service/telemetry"
)

// BuilderFunc edits one fresh application draft during New. Callbacks execute
// directly once in order before defaults are resolved; nil entries panic.
// It is an alias, so ordinary function slices retain their type identity.
//
//	policies := []application.BuilderFunc{func(app *application.Builder) {
//		app.Name("billing")
//	}}
//	app := application.New(policies...)
type BuilderFunc = func(*Builder)

// defaultShutdownTimeout bounds the shutdown performed by
// [Application.Run] unless [Builder.ShutdownTimeout] says otherwise.
const defaultShutdownTimeout = 30 * time.Second

// defaultReadinessInterval is the cadence used when no positive interval is set.
const defaultReadinessInterval = 5 * time.Second

// readinessTimeoutMultiplier selects two fifths of the completed interval.
const readinessTimeoutMultiplier = 2

// readinessTimeoutDivisor completes the default timeout ratio.
const readinessTimeoutDivisor = 5

// cleanupTimeout supplies a minimum uncanceled cleanup budget on rollback and
// shutdown, so resource releases still get a usable context after cancellation.
const cleanupTimeout = 10 * time.Second

// settings holds the completed application configuration. New copies it
// before wiring lifecycle services; dependencies and handler closures remain
// shared, while retained construction drafts cannot change published fields.
type settings struct {
	// Name is the service name. When set, the logger carries it as the
	// service attribute and telemetry drivers receive it as the resource's
	// service name.
	Name string

	// Version is the service version. When set, the logger carries it as
	// the version attribute and telemetry drivers receive it in the
	// resource. Empty selects the main module version stamped into the
	// binary by the go command (a tag such as v1.4.0 or a pseudo-version),
	// and stays empty when the binary has none.
	Version string

	// Environment is the deployment environment. Empty selects
	// [Development].
	Environment Environment

	// Debug enables debug level logging. When false the application logs
	// at info level in every environment.
	Debug bool

	// ShutdownTimeout bounds the shutdown performed by [Application.Run].
	// Zero or negative selects 30 seconds.
	ShutdownTimeout time.Duration

	// ReadinessInterval is the ticker cadence for background readiness passes.
	// Zero or negative selects 5 seconds.
	ReadinessInterval time.Duration

	// ReadinessTimeout bounds each readiness pass. Zero or negative selects
	// two fifths of ReadinessInterval (2 seconds by default).
	ReadinessTimeout time.Duration

	// HealthDetails includes failing check errors in verbose readiness and MCP
	// diagnostics. False reports "reason withheld" instead of backend error text.
	HealthDetails bool

	// LogHandler builds the log handler from the application's level
	// variable so that [Application.SetLogLevel] keeps working. Nil
	// selects text output with source locations for [Development] and
	// JSON otherwise.
	LogHandler func(level slog.Leveler) slog.Handler

	// Telemetry is the handle the application starts and stops. Nil supplies a
	// no-op handle unless Composer.MCPTools attaches diagnostic capture during
	// composition. Logger correlation does not require a started handle.
	Telemetry *telemetry.Telemetry
}

// Builder configures an application before its lifecycle is wired by [New].
// Settings replace previous values and return the same draft. Retained drafts
// cannot reconfigure an application; service pointers remain shared.
//
//	app := application.New(func(app *application.Builder) {
//		app.Name("billing")
//		app.Environment(application.Production)
//	})
type Builder struct {
	// settings is private construction state, copied before publication.
	settings settings
}

// newSettings applies construction policies once, then resolves defaults.
// It returns an independent root; dependencies and functions stay shared.
func newSettings(configure ...BuilderFunc) settings {
	application := &Builder{}

	for _, configure := range configure {
		configure(application)
	}

	config := application.settings

	if config.Environment == "" {
		config.Environment = Development
	}

	if config.Version == "" {
		config.Version = mainVersion(debug.ReadBuildInfo())
	}

	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}

	if config.ReadinessInterval <= 0 {
		config.ReadinessInterval = defaultReadinessInterval
	}

	if config.ReadinessTimeout <= 0 {
		config.ReadinessTimeout = config.ReadinessInterval * readinessTimeoutMultiplier / readinessTimeoutDivisor
	}

	return config
}

// Name sets the service name. It becomes the service log attribute and
// the telemetry resource's service name. The default is empty, which omits
// both. It returns the same draft.
//
//	app := application.New(func(app *application.Builder) {
//		app.Name("billing")
//	})
func (application *Builder) Name(name string) *Builder {
	application.settings.Name = name

	return application
}

// Version sets the service version. It becomes the version log attribute
// and the telemetry resource's version. The default is the main module
// version stamped into the binary by the go command, or empty when there is
// none (for example "(devel)" builds), which omits both.
//
//	app := application.New(func(app *application.Builder) {
//		app.Version("1.2.3")
//	})
func (application *Builder) Version(version string) *Builder {
	application.settings.Version = version

	return application
}

// Telemetry enables telemetry with handle, built with [telemetry.New] from a
// driver such as the in-process memory driver or a [telemetry.Compose]d one.
// The application starts handle after composition, before Startup callbacks,
// and stops it after cleanup and the final lifecycle log and event. The same
// handle is returned by Application.Telemetry; callers must not start or stop
// it separately. Nil leaves recording disabled unless Composer.MCPTools attaches
// local diagnostic capture. Logger correlation needs only a valid span context,
// not this setting or application startup.
//
//	app := application.New(func(app *application.Builder) {
//		app.Telemetry(telemetry.New(nil))
//	})
func (application *Builder) Telemetry(handle *telemetry.Telemetry) *Builder {
	application.settings.Telemetry = handle

	return application
}

// ShutdownTimeout bounds the shutdown performed by [Application.Run].
// Zero or negative selects the default of 30 seconds. It returns the same draft.
//
//	app := application.New(func(app *application.Builder) {
//		app.ShutdownTimeout(time.Minute)
//	})
func (application *Builder) ShutdownTimeout(timeout time.Duration) *Builder {
	application.settings.ShutdownTimeout = timeout

	return application
}

// ReadinessInterval sets the ticker cadence for background readiness passes.
// The first pass starts immediately; later passes wait for a tick, not a full
// interval after completion. Passes never overlap, and slow checks can delay
// the next pass. Zero or negative selects 5 seconds. A result older than three
// intervals counts as not ready. It returns the same draft.
//
//	app := application.New(func(app *application.Builder) {
//		app.ReadinessInterval(time.Second)
//	})
func (application *Builder) ReadinessInterval(interval time.Duration) *Builder {
	application.settings.ReadinessInterval = interval

	return application
}

// ReadinessTimeout sets each background readiness pass's context deadline.
// Checks must honor cancellation: the application waits for all callbacks and
// cannot forcibly interrupt them. Zero or negative selects two fifths of the
// final interval. It returns the same draft.
//
//	app := application.New(func(app *application.Builder) {
//		app.ReadinessTimeout(time.Second)
//	})
func (application *Builder) ReadinessTimeout(timeout time.Duration) *Builder {
	application.settings.ReadinessTimeout = timeout

	return application
}

// HealthDetails includes failing check error text in verbose readiness and MCP
// health diagnostics when true. False reports "reason withheld". Error text can
// contain backend internals; enable it only when all affected diagnostic endpoints
// are operator-protected. It does not change trusted in-process check/event values.
//
//	app := application.New(func(app *application.Builder) {
//		app.HealthDetails(true)
//	})
func (application *Builder) HealthDetails(details bool) *Builder {
	application.settings.HealthDetails = details

	return application
}

// Environment sets the deployment environment. The default is
// [Development].
//
//	app := application.New(func(app *application.Builder) {
//		app.Environment(application.Production)
//	})
func (application *Builder) Environment(environment Environment) *Builder {
	application.settings.Environment = environment

	return application
}

// Debug enables debug level logging when true. The default is false,
// which logs at info level in every environment.
//
//	app := application.New(func(app *application.Builder) {
//		app.Debug(true)
//	})
func (application *Builder) Debug(enabled bool) *Builder {
	application.settings.Debug = enabled

	return application
}

// LogHandler replaces the default log handler. The factory receives
// the application's level variable so that [Application.SetLogLevel]
// keeps working; handlers should honor it. Nil restores the environment's
// default handler. The factory runs once after all construction callbacks,
// with the final log level; the method itself only stores it and returns the
// same draft. Factory panics propagate from New.
//
//	app := application.New(func(app *application.Builder) {
//		app.LogHandler(func(level slog.Leveler) slog.Handler {
//			return slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
//		})
//	})
func (application *Builder) LogHandler(build func(level slog.Leveler) slog.Handler) *Builder {
	application.settings.LogHandler = build

	return application
}

// newDefaultHandler returns the handler for environment: human-readable
// text with source locations for [Development], JSON otherwise.
func newDefaultHandler(environment Environment, level slog.Leveler) slog.Handler {
	if environment == Development {
		return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			AddSource: true,
			Level:     level,
		})
	}

	return slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
}
