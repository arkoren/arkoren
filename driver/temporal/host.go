package temporal

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// ErrStarted reports a registration attempted after [Host.Start].
// Successful startup closes registration; failed startup leaves it available.
//
// Example:
//
//	if errors.Is(err, ErrStarted) {
//		return fmt.Errorf("register drivers before starting the host: %w", err)
//	}
var ErrStarted = errors.New("temporal host is started")

// Host owns one Temporal client and one worker. Drivers register workflows
// and activities with [Host.Register] before [Host.Start].
// NewFrom borrows its client instead; serialize Start and Stop calls.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.TaskQueue("orders")
//	})
//	_ = host.TaskQueue()
type Host struct {
	// config is the validated configuration.
	config settings

	// owned records that the Host dialed the client and must close it.
	owned bool

	// mu guards the fields below.
	mu sync.Mutex

	// client is the Temporal client, set by NewFrom or Start.
	client client.Client

	// worker is the running worker, set by Start.
	worker worker.Worker

	// registrations are applied to the worker before it starts.
	registrations []func(worker.Registry)

	// started records that Start has run.
	started bool

	// stopOnce makes the worker stop run once.
	stopOnce sync.Once

	// closeOnce closes the client once.
	closeOnce sync.Once

	// stopped is closed when the worker has stopped.
	stopped chan struct{}
}

// New returns a Host that dials Temporal in [Host.Start]. It performs no I/O.
// Construction callbacks run once synchronously in order on a fresh [Builder];
// nil entries panic. SDK option records are copied by value, not recursively.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.TaskQueue("orders")
//	})
//	_ = host.Client() // Nil until Start.
func New(configure ...BuilderFunc) *Host {
	return newHost(buildSettings(configure...))
}

// newHost normalizes completed settings and publishes a host without invoking
// construction callbacks. Both constructors use this ownership boundary.
func newHost(config settings) *Host {
	config.HostPort = cmp.Or(config.HostPort, client.DefaultHostPort)
	config.Namespace = cmp.Or(config.Namespace, client.DefaultNamespace)
	config.TaskQueue = cmp.Or(config.TaskQueue, "arkoren")

	return &Host{
		config:  config,
		owned:   true,
		stopped: make(chan struct{}),
	}
}

// NewFrom returns a Host that wraps a caller-owned client. [Host.Stop] does
// not close it. TaskQueue, WorkerOptions, WorkerStopTimeout, and NoWorker apply;
// Logger and connection settings are ignored. SDK workers inherit the supplied
// client's logger, which must be configured before creating that client.
// Callbacks have the same once-only execution and publication rules as [New].
// A nil client is rejected by Start without dialing a replacement.
//
// Example:
//
//	wrap := func(connection client.Client) *Host {
//		return NewFrom(connection, func(host *Builder) {
//			host.TaskQueue("orders")
//			host.NoWorker(true)
//		})
//	}
//	_ = wrap // The caller retains responsibility for closing the connection.
func NewFrom(conn client.Client, configure ...BuilderFunc) *Host {
	host := newHost(buildSettings(configure...))
	host.owned = false
	host.client = conn

	return host
}

// clientOptions derives the SDK dial settings without mutating native options
// supplied by the caller. Host address and namespace win; a native logger wins
// over the host logger. Native internals remain shared, not recursively copied.
func (host *Host) clientOptions() client.Options {
	options := host.config.ClientOptions
	options.HostPort = host.config.HostPort
	options.Namespace = host.config.Namespace

	if options.Logger == nil {
		options.Logger = newLogAdapter(cmp.Or(host.config.Logger, slog.Default()))
	}

	return options
}

// workerOptions derives worker shutdown policy without changing the caller's
// SDK record. A nonzero native timeout wins, including a negative value; only
// an absent native timeout uses the normalized host timeout.
func (host *Host) workerOptions() worker.Options {
	options := host.config.WorkerOptions

	if options.WorkerStopTimeout == 0 {
		options.WorkerStopTimeout = max(cmp.Or(host.config.WorkerStopTimeout, defaultWorkerStopTimeout), 0)
	}

	return options
}

// WorkerDisabled reports whether the Host was built with [Builder.NoWorker].
// It reports stored permission, not whether a worker is currently running.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.NoWorker(true)
//	})
//	_ = host.WorkerDisabled() // True even before Start.
func (host *Host) WorkerDisabled() bool {
	return host.config.NoWorker
}

// Ping checks native server health through the host's current client. It returns
// an error without a client; a supplied NewFrom client is available immediately.
// This observes server reachability, not dispatcher or worker callback readiness.
//
// Example:
//
//	if err := host.Ping(ctx); err != nil {
//		return fmt.Errorf("Temporal is not ready: %w", err)
//	}
func (host *Host) Ping(ctx context.Context) error {
	conn := host.Client()

	if conn == nil {
		return errors.New("ping temporal: host is not started")
	}

	if _, err := conn.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		return fmt.Errorf("ping temporal: %w", err)
	}

	return nil
}

// Client returns the Temporal client. It is nil before [Host.Start] unless the
// Host was built with NewFrom. Native access does not transfer ownership or
// replace Start/Stop; callers must not close a client owned by the host.
//
// Example:
//
//	native := host.Client()
//	if native == nil {
//		return errors.New("start the Temporal host before native access")
//	}
//	_ = native.ScheduleClient()
func (host *Host) Client() client.Client {
	host.mu.Lock()
	defer host.mu.Unlock()

	return host.client
}

// TaskQueue returns the task queue the worker polls.
// This immutable value also routes client submissions to matching workers.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.TaskQueue("orders")
//	})
//	queue := host.TaskQueue()
//	_ = queue // "orders", usable before startup.
func (host *Host) TaskQueue() string {
	return host.config.TaskQueue
}
