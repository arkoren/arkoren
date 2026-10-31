package temporal

import (
	"log/slog"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// settings retains the optional construction policy for a [Host].
// Construction publishes this private value after applying its role callbacks;
// live dependencies and SDK internals retain their existing ownership.
type settings struct {
	// HostPort is the address of the Temporal frontend. The default is
	// [client.DefaultHostPort]. [NewFrom] ignores it.
	HostPort string

	// Namespace is the Temporal namespace. The default is "default". [NewFrom]
	// ignores it.
	Namespace string

	// TaskQueue is the task queue the worker polls. The default is "arkoren".
	TaskQueue string

	// Logger supplies the dialed native client's logger. Nil uses slog.Default;
	// a native ClientOptions.Logger wins. NewFrom keeps the supplied client's
	// logger, so this setting does not apply there.
	Logger *slog.Logger

	// ClientOptions is the base set of client options, for TLS, credentials,
	// and interceptors. HostPort and Namespace always override the matching
	// fields, including when host defaults supply them. NewFrom ignores it.
	ClientOptions client.Options

	// WorkerOptions is passed to [worker.New].
	WorkerOptions worker.Options

	// WorkerStopTimeout supplies the SDK's graceful-stop wait. Zero uses ten
	// seconds; a negative value disables the grace wait. A nonzero native value
	// wins. This is not a total shutdown budget or forced callback termination.
	WorkerStopTimeout time.Duration

	// NoWorker makes [Host.Start] dial without creating a worker, even when
	// drivers registered workflows or activities. A Host with no
	// registrations never creates a worker.
	NoWorker bool
}

// defaultWorkerStopTimeout is the default [Builder.WorkerStopTimeout].
const defaultWorkerStopTimeout = 10 * time.Second

// Builder edits the construction settings of [New]. Callbacks share one
// fresh draft and run once in order; a nil callback panics. Retaining a draft
// does not permit changes to the published component. Dependencies and native
// SDK internals retain their existing ownership.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.Logger(slog.Default())
//	})
//	_ = component
type Builder struct {
	// config holds this draft's optional settings until publication.
	config settings
}

// buildSettings applies construction callbacks directly and publishes a detached
// settings root. An empty list preserves defaults; nil entries panic.
func buildSettings(configure ...BuilderFunc) settings {
	config := settings{
		HostPort:  client.DefaultHostPort,
		Namespace: client.DefaultNamespace,
		TaskQueue: "arkoren",

		WorkerStopTimeout: defaultWorkerStopTimeout,
	}
	host := &Builder{
		config: config,
	}

	for _, configure := range configure {
		configure(host)
	}

	return host.config
}

// HostPort is the address of the Temporal frontend. The default is
// [client.DefaultHostPort]. [NewFrom] ignores it.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.HostPort("localhost:7233")
//	})
//	_ = component
func (host *Builder) HostPort(hostPort string) *Builder {
	host.config.HostPort = hostPort

	return host
}

// Namespace is the Temporal namespace. The default is "default". [NewFrom]
// ignores it.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.Namespace("example")
//	})
//	_ = component
func (host *Builder) Namespace(namespace string) *Builder {
	host.config.Namespace = namespace

	return host
}

// TaskQueue is the task queue the worker polls. The default is "arkoren".
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.TaskQueue("example")
//	})
//	_ = component
func (host *Builder) TaskQueue(taskQueue string) *Builder {
	host.config.TaskQueue = taskQueue

	return host
}

// Logger supplies the logger for a client dialed by Host.Start. Nil adapts
// slog.Default at startup; an explicit native ClientOptions.Logger wins.
// NewFrom ignores this setting because SDK workers inherit the caller-owned
// client's logger, which this host cannot replace.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.Logger(slog.Default())
//	})
//	_ = component
func (host *Builder) Logger(logger *slog.Logger) *Builder {
	host.config.Logger = logger

	return host
}

// ClientOptions is the base set of client options, for TLS, credentials,
// and interceptors. HostPort and Namespace always override the matching fields,
// including host defaults when no explicit setting is supplied. NewFrom ignores
// this record; configure the native client before wrapping it.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.ClientOptions(client.Options{})
//	})
//	_ = component
func (host *Builder) ClientOptions(options client.Options) *Builder {
	host.config.ClientOptions = options

	return host
}

// WorkerOptions is passed to [worker.New].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.WorkerOptions(worker.Options{})
//	})
//	_ = component
func (host *Builder) WorkerOptions(options worker.Options) *Builder {
	host.config.WorkerOptions = options

	return host
}

// WorkerStopTimeout supplies Temporal's native graceful-stop wait. Zero uses
// ten seconds; a negative value disables that wait. A nonzero native
// WorkerOptions.WorkerStopTimeout wins. The SDK can apply the wait separately
// to internal workers and then cancel activity contexts; this is not a total
// shutdown deadline. Callbacks must cooperate, and SDK shutdown hooks must return.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.WorkerStopTimeout(time.Second)
//	})
//	_ = component
func (host *Builder) WorkerStopTimeout(timeout time.Duration) *Builder {
	host.config.WorkerStopTimeout = timeout

	return host
}

// NoWorker makes [Host.Start] dial without creating a worker, even when
// drivers registered workflows or activities. A Host with no
// registrations never creates a worker.
// It defaults to false. A later false restores permission without forcing an
// empty worker. [Host.WorkerDisabled] reports the configured flag.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.NoWorker(true)
//	})
//	_ = component
func (host *Builder) NoWorker(disabled bool) *Builder {
	host.config.NoWorker = disabled

	return host
}

// BuilderFunc configures a fresh host construction draft. Callbacks run directly
// once, synchronously in order; nil entries panic and publication detaches the
// draft settings, not native clients or SDK internals.
//
// Example:
//
//	policies := []BuilderFunc{
//		func(host *Builder) {
//			host.TaskQueue("orders")
//		},
//	}
//	host := New(policies...)
//	_ = host.TaskQueue()
type BuilderFunc = func(*Builder)
