package hatchet

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"strconv"
	"sync"

	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

// ErrStarted reports registration or repeated startup while Start is acquiring
// resources or after successful startup. Register every declaration beforehand.
//
// Example:
//
//	if err := host.Register(registration); errors.Is(err, ErrStarted) {
//		return fmt.Errorf("register before starting the host: %w", err)
//	}
var ErrStarted = errors.New("hatchet host is started")

// ErrNotStarted reports an operation needing an acquired native client. It also
// applies after Stop removes that client; HasWorker is not a readiness check.
//
// Example:
//
//	if err := New().Ping(context.Background()); errors.Is(err, ErrNotStarted) {
//		// Configure and start the host before using its drivers.
//	}
var ErrNotStarted = errors.New("hatchet host is not started")

// ErrStopped reports construction-time registration or startup after terminal
// [Host.Stop]. Construct a new host instead of restarting released resources.
//
// Example:
//
//	host := New()
//	_ = host.Stop(context.Background())
//	if err := host.Start(context.Background()); errors.Is(err, ErrStopped) {
//		// Create a new host for a new lifecycle.
//	}
var ErrStopped = errors.New("hatchet host is stopped")

// ErrNotRegistered reports a worker whose registration [Host.Start] did not
// observe in time. It is not a bound on the SDK's earlier registration RPCs.
//
// Example:
//
//	if err := host.Start(ctx); errors.Is(err, ErrNotRegistered) {
//		return fmt.Errorf("worker registration was not observed: %w", err)
//	}
var ErrNotRegistered = errors.New("hatchet worker not registered")

// ErrEngineUnsupported reports a Hatchet engine too old for the registered
// durable tasks. Use an engine with durable eviction before starting workflows.
//
// Example:
//
//	if err := host.Start(ctx); errors.Is(err, ErrEngineUnsupported) {
//		return fmt.Errorf("engine lacks durable eviction: %w", err)
//	}
var ErrEngineUnsupported = errors.New("hatchet engine unsupported")

// ErrInvalidConfig reports adapter-owned settings rejected before native client
// acquisition. Host.New retains its pointer-only API, so Host.Start validates.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.HostPort("localhost:65536")
//	})
//	if err := host.Start(context.Background()); errors.Is(err, ErrInvalidConfig) {
//		// Correct the configuration and construct a new host.
//	}
var ErrInvalidConfig = errors.New("invalid hatchet config")

// Registration builds one native workflow during Start, after client acquisition.
// Return a nonnil declaration or an error. The host converts callback panics to
// startup errors; callbacks must not reenter Start or Stop on the same host.
//
// Example:
//
//	registration := Registration(func(client *hatchet.Client) (hatchet.WorkflowBase, error) {
//		return client.NewStandaloneTask("ping", func(hatchet.Context, string) (string, error) {
//			return "pong", nil
//		}), nil
//	})
//	if err := New().Register(registration); err != nil {
//		return err
//	}
type Registration = func(*hatchet.Client) (hatchet.WorkflowBase, error)

// Host owns the native client and optional worker shared by this module's drivers.
// Construct with New, register before Start, and release once with Stop. Stop is
// terminal. Lifecycle state is synchronized, but Start/Stop must not be called
// recursively from registrations or task bodies that Stop must join.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.NoWorker(true)
//	})
//	_ = host.Client() // Nil before Start and after Stop.
type Host struct {
	// config retains the completed construction settings used by this instance.
	config Config
	// logger receives operational records without changing the engine's execution
	// state.
	logger *slog.Logger

	// mutex serializes access to the mutable state below it.
	mutex sync.Mutex
	// registrations retains workflow declarations until Start creates the native
	// client.
	registrations []Registration
	// client provides the native connection used for engine operations.
	client *hatchet.Client
	// started records successful startup so later registration and repeated starts
	// can be rejected.
	started bool
	// starting is nonnil during acquisition and closes at publication. Stop waits
	// for this handoff before releasing resources; registration stays closed.
	starting chan struct{}
	// stopped prevents acquisition or registration after terminal Stop begins.
	stopped bool
	// cleanup releases the worker resources created during startup.
	cleanup func() error
	// stopOnce ensures repeated Stop calls share one cleanup attempt.
	stopOnce sync.Once
	// stopErr retains the cleanup result returned to subsequent Stop callers.
	stopErr error
	// actions joins callbacks admitted by this host's native worker middleware.
	// Admission and lifetime cancellation share mutex so Wait never races Add.
	actions sync.WaitGroup
}

// New returns a host without performing I/O. Construction callbacks run once,
// synchronously in order on a fresh [Builder]; nil entries panic. Owned native
// option slices are detached, but SDK functions and their captures stay shared.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.WorkerName("orders")
//		host.NoWorker(true)
//	})
//	_ = host.Client() // Nil until Start.
func New(configure ...BuilderFunc) *Host {
	config := buildSettings(configure...)
	config.WorkerName = cmp.Or(config.WorkerName, DefaultWorkerName)
	config.Slots = cmp.Or(config.Slots, DefaultSlots)

	if config.RegisterTimeout <= 0 {
		config.RegisterTimeout = DefaultRegisterTimeout
	}

	config.Namespace = cmp.Or(config.Namespace, os.Getenv("HATCHET_CLIENT_NAMESPACE"))

	return &Host{
		config: config,
		logger: cmp.Or(config.Logger, slog.Default()),
	}
}

// Namespace returns the prefix that the SDK puts before event keys and
// workflow names: the configured namespace and an underscore, or empty. It is
// immutable and available before Start; set it on Builder, not ClientOptions.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.Namespace("stage")
//	})
//	_ = host.Namespace() // "stage_"
func (host *Host) Namespace() string {
	if host.config.Namespace == "" {
		return ""
	}

	return host.config.Namespace + "_"
}

// HasWorker reports whether the host permits a worker to run workflows.
// It reports permission, not a running worker; an empty host starts no worker.
//
// Example:
//
//	host := New(func(host *Builder) {
//		host.NoWorker(false)
//	})
//	_ = host.HasWorker() // True even before Start.
func (host *Host) HasWorker() bool {
	return !host.config.NoWorker
}

// clientOptions derives hatchet.NewClient options from the configuration.
func (host *Host) clientOptions() ([]hatchet.ClientOpt, error) {
	var options []hatchet.ClientOpt

	if host.config.Slots < 0 || host.config.DurableSlots < 0 {
		return nil, fmt.Errorf("%w: Slots and DurableSlots must not be negative", ErrInvalidConfig)
	}

	if max(host.config.Slots, host.config.DurableSlots) > math.MaxInt32 {
		return nil, fmt.Errorf("%w: Slots and DurableSlots must fit int32", ErrInvalidConfig)
	}

	if host.config.Token != "" {
		options = append(options, hatchet.WithToken(host.config.Token))
	}

	if host.config.HostPort != "" {
		option, err := addressOption(host.config.HostPort)

		if err != nil {
			return nil, err
		}

		options = append(options, option)
	}

	switch host.config.TLSStrategy {
	case "", "tls":
	case "none":
		options = append(options, hatchet.WithTLSConfig(nil))
	default:
		return nil, fmt.Errorf("%w: TLS strategy %q: use \"none\", \"tls\", or ClientOptions", ErrInvalidConfig, host.config.TLSStrategy)
	}

	if host.config.Namespace != "" {
		options = append(options, hatchet.WithNamespace(host.config.Namespace))
	}

	return append(options, host.config.ClientOptions...), nil
}

// addressOption validates an explicit TCP address before native construction.
// SDK endpoint and environment fallbacks are untouched when no address is set.
func addressOption(address string) (hatchet.ClientOpt, error) {
	name, text, err := net.SplitHostPort(address)

	if err != nil {
		return nil, fmt.Errorf("%w: host port %q: %w", ErrInvalidConfig, address, err)
	}

	port, err := strconv.Atoi(text)

	if err != nil {
		return nil, fmt.Errorf("%w: host port %q: %w", ErrInvalidConfig, address, err)
	}

	// maxPort is the largest TCP port representable on the wire.
	const maxPort = 65535

	if port < 1 || port > maxPort {
		return nil, fmt.Errorf("%w: host port %q is outside 1-%d", ErrInvalidConfig, address, maxPort)
	}

	return hatchet.WithHostPort(name, port), nil
}

// Ping lists one workflow to check native API reachability after Start. It does
// not prove worker liveness, schedule Run readiness, or future task delivery.
//
// Example:
//
//	if err := host.Ping(ctx); err != nil {
//		return fmt.Errorf("Hatchet API unavailable: %w", err)
//	}
func (host *Host) Ping(ctx context.Context) error {
	client := host.Client()

	if client == nil {
		return fmt.Errorf("ping hatchet: %w", ErrNotStarted)
	}

	limit := 1

	if _, err := client.Workflows().List(ctx, &rest.WorkflowListParams{
		Limit: &limit,
	}); err != nil {
		return fmt.Errorf("ping hatchet: %w", err)
	}

	return nil
}

// Client returns the native Hatchet client. It is nil until [Host.Start]
// succeeds and after Stop. Borrow it without closing it; Host owns release.
// Native operations bypass portable driver identity and payload guarantees.
//
// Example:
//
//	if client := host.Client(); client != nil {
//		_, err := client.GetEngineVersion(ctx)
//		if err != nil {
//			return err
//		}
//	}
func (host *Host) Client() *hatchet.Client {
	host.mutex.Lock()
	defer host.mutex.Unlock()

	return host.client
}
