package temporal

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// TestHostSettingsDefaults verifies that construction supplies the expected
// native connection and worker defaults without startup I/O.
func TestHostSettingsDefaults(t *testing.T) {
	t.Parallel()

	config := buildSettings()

	if config.HostPort == "" || config.Namespace != "default" || config.TaskQueue != "arkoren" {
		t.Fatalf("config = %+v", config)
	}
}

// TestOptionsApply verifies that explicit host connection and worker settings
// replace their construction defaults.
func TestOptionsApply(t *testing.T) {
	t.Parallel()

	config := buildSettings(func(host *Builder) {
		host.HostPort("h:1")
	}, func(host *Builder) {
		host.Namespace("n")
	}, func(host *Builder) {
		host.TaskQueue("q")
	})

	if config.HostPort != "h:1" || config.Namespace != "n" || config.TaskQueue != "q" {
		t.Fatalf("config = %+v", config)
	}
}

// TestNewIsPure verifies that host construction creates neither a native client
// nor worker resources.
func TestNewIsPure(t *testing.T) {
	t.Parallel()

	host := New(func(host *Builder) {
		host.TaskQueue("q")
	})

	if host.Client() != nil || host.TaskQueue() != "q" {
		t.Fatal("unexpected host state")
	}
}

// TestRegisterBeforeStartSucceeds verifies that a native registration callback
// can be stored before startup without being invoked.
func TestRegisterBeforeStartSucceeds(t *testing.T) {
	t.Parallel()

	if err := New().Register(func(worker.Registry) {
		// No worker definitions are needed to test registration acceptance.
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRegisterNilFails verifies that nil registration callbacks are rejected
// before worker startup.
func TestRegisterNilFails(t *testing.T) {
	t.Parallel()

	if err := New().Register(nil); err == nil {
		t.Fatal("expected error")
	}
}

// TestPingBeforeStartFails verifies that readiness does not succeed when no
// native client has been connected.
func TestPingBeforeStartFails(t *testing.T) {
	t.Parallel()

	if err := New().Ping(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// TestStopBeforeStartSucceeds verifies that stopping an unstarted host is a
// successful no-op.
func TestStopBeforeStartSucceeds(t *testing.T) {
	t.Parallel()

	if err := New().Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestStartFailsWithBadAddress verifies that a native connection failure is
// returned without being mistaken for repeated startup.
func TestStartFailsWithBadAddress(t *testing.T) {
	t.Parallel()

	host := New(func(host *Builder) {
		host.HostPort("127.0.0.1:1")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2_000_000_000)
	defer cancel()

	err := host.Start(ctx)

	if err == nil {
		t.Fatal("expected error")
	}

	if errors.Is(err, ErrStarted) {
		t.Fatal("unexpected ErrStarted")
	}
}

// TestWorkerStopTimeoutDefault verifies that worker shutdown uses the
// documented default and retains an explicit override.
func TestWorkerStopTimeoutDefault(t *testing.T) {
	t.Parallel()

	if got := buildSettings().WorkerStopTimeout; got != 10*time.Second {
		t.Fatalf("WorkerStopTimeout = %v; want 10s", got)
	}

	if got := buildSettings(func(host *Builder) {
		host.WorkerStopTimeout(time.Second)
	}).WorkerStopTimeout; got != time.Second {
		t.Fatalf("WorkerStopTimeout = %v; want 1s", got)
	}
}

// TestNoWorker verifies that worker creation is enabled by default and can be
// explicitly disabled.
func TestNoWorker(t *testing.T) {
	t.Parallel()

	if buildSettings().NoWorker || !buildSettings(func(host *Builder) {
		host.NoWorker(true)
	}).NoWorker {
		t.Fatal("NoWorker did not disable the worker")
	}
}

// borrowedClient supplies only the native lifecycle operation exercised here.
// Embedding the interface keeps unrelated SDK operations outside this test seam.
type borrowedClient struct {
	// Client supplies unimplemented SDK methods; unexpected calls fail through the
	// nil embedded interface.
	client.Client
	// closed records an ownership violation by Host.Stop.
	closed bool
}

// Close records an attempted release of a caller-owned native client.
func (connection *borrowedClient) Close() {
	connection.closed = true
}

// TestNewFromBorrowsClientAndDoesNotForceWorker verifies once-only callbacks,
// retained-draft isolation, and the no-registrations permission rule.
func TestNewFromBorrowsClientAndDoesNotForceWorker(t *testing.T) {
	t.Parallel()

	connection := &borrowedClient{}
	calls := 0
	var retained *Builder
	host := NewFrom(connection, func(host *Builder) {
		calls++
		retained = host

		host.TaskQueue("borrowed")
		host.NoWorker(true)
		host.NoWorker(false)
	})

	retained.TaskQueue("changed")

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := host.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if calls != 1 || host.Client() != connection || connection.closed {
		t.Fatal("NewFrom replayed callbacks or took client ownership")
	}

	if host.worker != nil || host.TaskQueue() != "borrowed" {
		t.Fatal("empty registrations forced a worker or publication shared its draft")
	}
}

// TestNewFromNoWorkerSkipsRegistration verifies that native registration hooks
// stay runtime hooks and are never called when worker permission is disabled.
func TestNewFromNoWorkerSkipsRegistration(t *testing.T) {
	t.Parallel()

	host := NewFrom(&borrowedClient{}, func(host *Builder) {
		host.NoWorker(true)
		host.WorkerStopTimeout(3 * time.Second)
	})

	if err := host.Register(func(worker.Registry) {
		t.Fatal("worker registration ran on a client-only host")
	}); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !host.WorkerDisabled() || host.workerOptions().WorkerStopTimeout != 3*time.Second {
		t.Fatal("NewFrom ignored worker permission or host shutdown timeout")
	}
}

// TestNativeOptionsPreservePrecedence verifies value-copy capture, host address
// overrides, and the native nonzero worker shutdown timeout precedence.
func TestNativeOptionsPreservePrecedence(t *testing.T) {
	t.Parallel()

	clientOptions := client.Options{
		HostPort:  "native:7233",
		Namespace: "native",
	}
	workerOptions := worker.Options{
		WorkerStopTimeout: 7 * time.Second,
	}

	host := New(func(host *Builder) {
		host.ClientOptions(clientOptions)
		host.WorkerOptions(workerOptions)
		host.HostPort("host:7233")
		host.Namespace("host")
		host.WorkerStopTimeout(time.Second)
	})

	clientOptions.Namespace = "changed"
	workerOptions.WorkerStopTimeout = 0

	if host.clientOptions().HostPort != "host:7233" || host.clientOptions().Namespace != "host" {
		t.Fatal("native address fields overrode host settings")
	}

	if host.workerOptions().WorkerStopTimeout != 7*time.Second {
		t.Fatal("host timeout overrode a nonzero native timeout")
	}
}

// TestNativeClientLoggerWins verifies SDK logger precedence without dialing.
// A host logger is adapted only when the native client record has none.
func TestNativeClientLoggerWins(t *testing.T) {
	t.Parallel()

	nativeLogger := newLogAdapter(slog.Default())
	hostLogger := slog.Default().With("owner", "host")

	host := New(func(host *Builder) {
		host.Logger(hostLogger)
		host.ClientOptions(client.Options{
			Logger: nativeLogger,
		})
	})

	if host.clientOptions().Logger != nativeLogger {
		t.Fatal("host logger replaced the native client logger")
	}

	withoutNative := New(func(host *Builder) {
		host.Logger(hostLogger)
	})

	if withoutNative.clientOptions().Logger != newLogAdapter(hostLogger) {
		t.Fatal("host logger was not adapted for a client without a logger")
	}
}

// TestNativeWorkerTimeoutNormalization verifies the unchanged host fallback:
// zero selects ten seconds and a negative host timeout stops immediately.
func TestNativeWorkerTimeoutNormalization(t *testing.T) {
	t.Parallel()

	if New().workerOptions().WorkerStopTimeout != 10*time.Second {
		t.Fatal("unexpected default worker stop timeout")
	}

	host := New(func(host *Builder) {
		host.WorkerStopTimeout(-time.Second)
	})

	if host.workerOptions().WorkerStopTimeout != 0 {
		t.Fatal("negative host timeout must stop immediately")
	}
}

// TestHostNormalizesEmptySettings verifies defaults follow completed callbacks
// and NoWorker remains false without forcing worker creation.
func TestHostNormalizesEmptySettings(t *testing.T) {
	t.Parallel()

	host := New(func(host *Builder) {
		host.HostPort("")
		host.Namespace("")
		host.TaskQueue("")
	})

	if host.config.HostPort != client.DefaultHostPort || host.config.Namespace != client.DefaultNamespace {
		t.Fatal("empty connection settings did not use defaults")
	}

	if host.TaskQueue() != "arkoren" || host.WorkerDisabled() {
		t.Fatal("empty task queue or worker permission did not use defaults")
	}
}
