package hatchet

import (
	"log/slog"
	"slices"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

// Default configuration values.
const (
	// DefaultWorkerName is the worker name used when [Builder.WorkerName] is empty.
	//
	// Example:
	//
	//	component := New(func(host *Builder) {
	//		host.WorkerName(DefaultWorkerName)
	//	})
	//	_ = component
	DefaultWorkerName = "arkoren"

	// DefaultSlots is the slot count used when [Builder.Slots] is zero.
	//
	// Example:
	//
	//	component := New(func(host *Builder) {
	//		host.Slots(DefaultSlots)
	//	})
	//	_ = component
	DefaultSlots = 100

	// DefaultRegisterTimeout is the registration wait used when
	// [Builder.RegisterTimeout] is zero.
	//
	// Example:
	//
	//	component := New(func(host *Builder) {
	//		host.RegisterTimeout(DefaultRegisterTimeout)
	//	})
	//	_ = component
	DefaultRegisterTimeout = 30 * time.Second
)

// Config is a redacted reporting record for host settings, not a constructor
// input. Configure [New] through [Builder]. String, GoString, and LogValue
// replace a nonempty Token with a redaction marker; empty tokens stay empty.
//
// Example:
//
//	record := Config{Token: "secret"}
//	slog.Info("host settings", "config", record)
type Config struct {
	// Token is the Hatchet API token. Empty reads HATCHET_CLIENT_TOKEN.
	Token string

	// HostPort is the gRPC address as "host:port". Empty uses the SDK default
	// or the HATCHET_CLIENT_HOST_PORT environment variable.
	HostPort string

	// TLSStrategy is "none" to connect without TLS, or "tls" or empty to use
	// the SDK default, which reads HATCHET_CLIENT_TLS_* variables. For mutual
	// TLS, pass hatchet.WithTLSConfig in [Builder.ClientOptions].
	TLSStrategy string

	// Namespace prefixes workflow and event names in Hatchet. Empty reads
	// HATCHET_CLIENT_NAMESPACE. Set it here, not through ClientOptions: the
	// drivers must know it to match durable event waits.
	Namespace string

	// WorkerName is the name the worker registers under. Empty uses
	// [DefaultWorkerName].
	WorkerName string

	// Slots is the number of tasks the worker runs at once. Zero uses
	// [DefaultSlots]; Start requires a nonnegative int32 value.
	//
	// Example:
	//
	//	record := Config{Slots: DefaultSlots}
	//	_ = record.String()
	Slots int

	// DurableSlots is the number of durable tasks the worker keeps at once.
	// Zero uses the SDK default; Start requires a nonnegative int32 value.
	//
	// Example:
	//
	//	record := Config{DurableSlots: 10}
	//	_ = record.String()
	DurableSlots int

	// RegisterTimeout budgets registration-log observation after native startup.
	// It does not bound earlier SDK RPCs or cleanup; zero/negative uses the default.
	//
	// Example:
	//
	//	record := Config{RegisterTimeout: DefaultRegisterTimeout}
	//	_ = record.String()
	RegisterTimeout time.Duration

	// NoWorker makes [Host.Start] connect the client only: no registration
	// runs and no worker starts. Use it in processes that only start and
	// query runs.
	NoWorker bool

	// Logger receives host log records and those of the Hatchet SDK. Nil
	// uses [slog.Default].
	Logger *slog.Logger

	// ClientOptions are extra options passed to hatchet.NewClient after the
	// options derived from the other fields. A client logger among them is
	// replaced: the SDK logs to Logger.
	ClientOptions []hatchet.ClientOpt
}

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
	config Config
}

// buildSettings applies construction callbacks directly and publishes a detached
// settings root. An empty list preserves defaults; nil entries panic.
func buildSettings(configure ...BuilderFunc) Config {
	config := Config{
		WorkerName:      DefaultWorkerName,
		Slots:           DefaultSlots,
		RegisterTimeout: DefaultRegisterTimeout,
	}

	host := &Builder{
		config: config,
	}

	for _, configure := range configure {
		configure(host)
	}

	config = host.config
	config.ClientOptions = slices.Clone(config.ClientOptions)

	return config
}

// Token is the Hatchet API token. Empty reads HATCHET_CLIENT_TOKEN.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.Token("example")
//	})
//	_ = component
func (host *Builder) Token(token string) *Builder {
	host.config.Token = token

	return host
}

// HostPort is the gRPC address as "host:port". Empty uses the SDK default
// or the HATCHET_CLIENT_HOST_PORT environment variable.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.HostPort("localhost:7077")
//	})
//	_ = component
func (host *Builder) HostPort(hostPort string) *Builder {
	host.config.HostPort = hostPort

	return host
}

// TLSStrategy is "none" to connect without TLS, or "tls" or empty to use
// the SDK default, which reads HATCHET_CLIENT_TLS_* variables. For mutual
// TLS, pass hatchet.WithTLSConfig in [Builder.ClientOptions].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.TLSStrategy("none")
//	})
//	_ = component
func (host *Builder) TLSStrategy(strategy string) *Builder {
	host.config.TLSStrategy = strategy

	return host
}

// WorkerName is the name the worker registers under. Empty uses
// [DefaultWorkerName].
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.WorkerName("example")
//	})
//	_ = component
func (host *Builder) WorkerName(name string) *Builder {
	host.config.WorkerName = name

	return host
}

// Slots is the number of tasks the worker runs at once. Zero uses
// [DefaultSlots]; Start requires a nonnegative int32 value or ErrInvalidConfig.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.Slots(4)
//	})
//	_ = component
func (host *Builder) Slots(slots int) *Builder {
	host.config.Slots = slots

	return host
}

// DurableSlots is the number of durable tasks the worker keeps at once.
// Zero uses the SDK default. Start rejects values outside the nonnegative int32
// range with ErrInvalidConfig before native registration narrows the integer.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.DurableSlots(4)
//	})
//	_ = component
func (host *Builder) DurableSlots(slots int) *Builder {
	host.config.DurableSlots = slots

	return host
}

// RegisterTimeout budgets registration-log observation after the SDK constructs
// and starts its worker. Missing observation returns ErrNotRegistered after
// cleanup. It does not bound earlier SDK RPCs, cleanup, or task draining. If ctx
// ends during observation, Start waits for the same handoff before cleanup.
// Zero or negative uses DefaultRegisterTimeout.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.RegisterTimeout(time.Second)
//	})
//	_ = component
func (host *Builder) RegisterTimeout(timeout time.Duration) *Builder {
	host.config.RegisterTimeout = timeout

	return host
}

// NoWorker makes [Host.Start] connect the client only: no registration
// runs and no worker starts. Use it in processes that only start and
// query runs.
// It defaults to false. A later false restores permission; it never forces a
// worker when no registrations exist. [Host.HasWorker] reports permission.
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

// Logger receives host log records and those of the Hatchet SDK. Nil
// uses [slog.Default].
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

// ClientOptions are extra options passed to hatchet.NewClient after the
// options derived from the other fields. A client logger among them is
// replaced: the SDK logs to Logger.
// It returns the same draft for subsequent settings.
//
// Example:
//
//	component := New(func(host *Builder) {
//		host.ClientOptions()
//	})
//	_ = component
func (host *Builder) ClientOptions(opts ...hatchet.ClientOpt) *Builder {
	host.config.ClientOptions = slices.Clone(opts)

	return host
}

// Namespace prefixes workflow and event names in Hatchet. Empty reads
// HATCHET_CLIENT_NAMESPACE. Set it here, not through ClientOptions: the
// drivers must know it to match durable event waits.
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

// BuilderFunc edits one construction draft before New publishes host settings.
// Policies run synchronously in order; a nil policy panics. SDK hooks and their
// captures remain shared even though the option slice is copied.
//
// Example:
//
//	policies := []BuilderFunc{func(host *Builder) {
//		host.WorkerName("orders")
//	}}
//	component := New(policies...)
//	_ = component.HasWorker()
type BuilderFunc = func(*Builder)
