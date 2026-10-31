package hatchet_test

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/hatchet-dev/hatchet/sdks/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"arkoren.dev/driver/hatchet"
	"arkoren.dev/driver/hatchet/internal/itest"
	"arkoren.dev/driver/hatchet/internal/protocoltest"
)

// TestIntegrationStartObservesRegistration pins the registration record that
// Start waits for to the SDK in go.mod: an SDK that rewords or drops it
// makes Start fail with ErrNotRegistered instead of returning.
func TestIntegrationStartObservesRegistration(t *testing.T) {
	host := hatchet.New(itest.Options(t, func(host *hatchet.Builder) {
		host.RegisterTimeout(15 * time.Second)
	})...)

	err := host.Register(func(client *sdk.Client) (sdk.WorkflowBase, error) {
		return client.NewStandaloneTask("arkoren-it-host-register", func(sdk.Context, struct{}) (struct{}, error) {
			return struct{}{}, nil
		}), nil
	})

	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	start := time.Now()

	if err := host.Start(t.Context()); err != nil {
		if errors.Is(err, hatchet.ErrNotRegistered) {
			t.Fatalf("Start() error = %v; the SDK no longer writes the registration record Start waits for", err)
		}

		t.Fatalf("Start() error = %v", err)
	}

	t.Cleanup(func() {
		_ = host.Stop(context.Background())
	})

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Start() took %s", elapsed)
	}
}

// TestRegisterRejectsRegistrationDuringStart holds startup at its public native
// registration callback. Successful late registration would be lost from the
// worker's already-captured declaration set.
func TestRegisterRejectsRegistrationDuringStart(t *testing.T) {
	host := protocoltest.Host(t, http.NotFoundHandler(), func(host *hatchet.Builder) {
		host.NoWorker(false)
	})

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() {
		once.Do(func() {
			close(release)
		})
	}

	if err := host.Register(func(*sdk.Client) (sdk.WorkflowBase, error) {
		close(entered)
		<-release

		return nil, errors.New("registration intentionally failed")
	}); err != nil {
		t.Fatal(err)
	}

	started := make(chan error, 1)

	go func() {
		started <- host.Start(t.Context())
	}()
	t.Cleanup(func() {
		unblock()
		<-started
	})

	<-entered

	if err := host.Register(func(*sdk.Client) (sdk.WorkflowBase, error) {
		return nil, nil
	}); !errors.Is(err, hatchet.ErrStarted) {
		t.Fatalf("Register during Start = %v; want ErrStarted", err)
	}
}

// TestStartRejectsOverflowingSlots keeps a Go int from wrapping the SDK's int32
// worker registration field. Validation must precede even token parsing.
func TestStartRejectsOverflowingSlots(t *testing.T) {
	t.Parallel()

	host := hatchet.New(func(host *hatchet.Builder) {
		host.Slots(math.MaxInt32 + 1)
		host.Token("not-a-token")
	})

	if err := host.Start(t.Context()); !errors.Is(err, hatchet.ErrInvalidConfig) {
		t.Fatalf("Start = %v; want ErrInvalidConfig before native narrowing", err)
	}
}

// recordWriter adapts a test-owned log sink without retaining SDK state or
// reading an unsynchronized buffer while native callbacks are running.
type recordWriter func([]byte) (int, error)

// Write forwards one record to the test sink. The sink owns any retained bytes.
func (recordwriter recordWriter) Write(data []byte) (int, error) {
	return recordwriter(data)
}

// TestStartWarnsForUnparseableEngineVersion observes the configured public log
// sink before an intentional native registration failure. No worker is started.
func TestStartWarnsForUnparseableEngineVersion(t *testing.T) {
	var warned atomic.Bool
	logger := slog.New(slog.NewTextHandler(recordWriter(func(data []byte) (int, error) {
		if strings.Contains(string(data), "hatchet engine version is not semver") {
			warned.Store(true)
		}

		return len(data), nil
	}), nil))

	address := protocoltest.GRPC(t, func(_ context.Context, method, _ string) (string, error) {
		if strings.HasSuffix(method, "/GetVersion") {
			return `{"version":"dev"}`, nil
		}

		return "", status.Error(codes.PermissionDenied, "registration intentionally blocked")
	})

	host := protocoltest.Host(t, http.NotFoundHandler(), func(host *hatchet.Builder) {
		host.NoWorker(false)
		host.Logger(logger)
		host.HostPort(address)
	})

	if err := host.Register(func(client *sdk.Client) (sdk.WorkflowBase, error) {
		return client.NewStandaloneDurableTask("native-task", func(sdk.DurableContext, string) (string, error) {
			return "unused", nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err == nil {
		t.Fatal("expected the deliberate native registration failure")
	}

	if !warned.Load() {
		t.Fatal("Start did not warn that the engine version cannot be parsed")
	}
}

// TestStartRejectsOutOfRangePort preserves the existing Start-time validation
// boundary while rejecting numeric ports outside the TCP range before the SDK.
func TestStartRejectsOutOfRangePort(t *testing.T) {
	t.Parallel()

	host := hatchet.New(func(host *hatchet.Builder) {
		host.HostPort("localhost:65536")
		host.Token("not-a-token")
	})

	if err := host.Start(t.Context()); !errors.Is(err, hatchet.ErrInvalidConfig) {
		t.Fatalf("Start = %v; want ErrInvalidConfig before SDK construction", err)
	}
}

// TestStopCancelsAndJoinsCooperativeNativeActions exercises a real SDK worker
// callback. Stop must cancel this host's work and join its body, not merely wait
// for the SDK's intake-cancellation function to return.
func TestStopCancelsAndJoinsCooperativeNativeActions(t *testing.T) {
	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "native-task", `{"input":{}}`)
	entered, finished, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		close(release)
		<-finished
	})

	if err := worker.Host.Register(func(client *sdk.Client) (sdk.WorkflowBase, error) {
		return client.NewStandaloneTask("native-task", func(ctx sdk.Context, input map[string]any) (map[string]any, error) {
			close(entered)

			select {
			case <-ctx.Done():
			case <-release:
			}

			close(finished)

			return input, ctx.Err()
		}), nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	<-entered

	if err := worker.Host.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-finished:
	default:
		t.Fatal("Stop returned before its cooperative native action ended")
	}
}

// TestStartAfterStopCannotAcquireResources prevents Stop-before-Start from
// consuming cleanup and then allowing a later unreleasable client acquisition.
func TestStartAfterStopCannotAcquireResources(t *testing.T) {
	host := protocoltest.Host(t, http.NotFoundHandler())

	if err := host.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); !errors.Is(err, hatchet.ErrStopped) {
		t.Fatalf("Start after terminal Stop = %v; want ErrStopped", err)
	}
}

// TestFailedStartClosesOwnedEmbeddedClient verifies rollback after acquisition
// through a public registration failure. No worker or embedded engine is run.
func TestFailedStartClosesOwnedEmbeddedClient(t *testing.T) {
	closed := make(chan struct{}, 1)
	sdk.RegisterEmbeddedBackend(func(context.Context, sdk.EmbeddedConfig) (func(context.Context) error, error) {
		return func(context.Context) error {
			closed <- struct{}{}

			return nil
		}, nil
	})
	t.Cleanup(func() {
		sdk.RegisterEmbeddedBackend(nil)
	})

	host := protocoltest.Host(t, http.NotFoundHandler(), func(host *hatchet.Builder) {
		host.NoWorker(false)
		host.ClientOptions(sdk.WithEmbedded())
	})

	failure := errors.New("registration failed")

	if err := host.Register(func(*sdk.Client) (sdk.WorkflowBase, error) {
		return nil, failure
	}); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("Start = %v; want registration failure", err)
	}

	select {
	case <-closed:
	default:
		t.Fatal("failed Start did not release its acquired native client")
	}
}

// TestStopClosesOwnedEmbeddedClient uses the SDK's public embedded-backend
// registration seam, without starting an engine. Client-only hosts still own
// the native shutdown returned by client acquisition.
func TestStopClosesOwnedEmbeddedClient(t *testing.T) {
	closed := make(chan struct{}, 1)
	sdk.RegisterEmbeddedBackend(func(context.Context, sdk.EmbeddedConfig) (func(context.Context) error, error) {
		return func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Errorf("release received expired context: %v", ctx.Err())
			}

			closed <- struct{}{}

			return nil
		}, nil
	})
	t.Cleanup(func() {
		sdk.RegisterEmbeddedBackend(nil)
	})

	host := protocoltest.Host(t, http.NotFoundHandler(), func(host *hatchet.Builder) {
		host.ClientOptions(sdk.WithEmbedded())
	})

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := host.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case <-closed:
	default:
		t.Fatal("Stop did not close the owned native client")
	}
}

// TestNativeStartupPanicEscapesCaller isolates the pinned SDK's background panic
// in a child test process. Caller-side recovery cannot intercept it; the parent
// requires that exact panic rather than treating a timeout as reproduction.
func TestNativeStartupPanicEscapesCaller(t *testing.T) {
	if os.Getenv("ARKOREN_HATCHET_PANIC_PROBE") == "1" {
		nativeStartupPanic(t)

		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeStartupPanicEscapesCaller$")
	command.Env = append(os.Environ(), "ARKOREN_HATCHET_PANIC_PROBE=1")

	output, err := command.CombinedOutput()

	if ctx.Err() != nil {
		t.Fatalf("panic probe timed out, not a reproduced panic: %s", output)
	}

	if err == nil || !strings.Contains(string(output), "panic:") {
		t.Fatalf("native startup did not panic: %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "native registration denied") {
		t.Fatalf("unexpected subprocess failure: %s", output)
	}
}

// nativeStartupPanic uses public native worker options, including the task panic
// hook, to show that startup-loop failures remain outside that hook and the
// caller's recover boundary. All protocol traffic stays on synthetic localhost.
func nativeStartupPanic(t *testing.T) {
	address := protocoltest.GRPC(t, func(_ context.Context, method, _ string) (string, error) {
		if strings.HasSuffix(method, "/GetVersion") {
			return `{"version":"v0.114.11"}`, nil
		}

		if strings.HasSuffix(method, "/Register") {
			return "", status.Error(codes.PermissionDenied, "native registration denied")
		}

		return `{}`, nil
	})

	host := protocoltest.Host(t, http.NotFoundHandler(), func(host *hatchet.Builder) {
		host.HostPort(address)
	})

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	client := host.Client()
	task := client.NewStandaloneTask("panic-probe", func(sdk.Context, string) (string, error) {
		return "unused", nil
	})

	worker, err := client.NewWorker("panic-probe", sdk.WithWorkflows(task), sdk.WithPanicHandler(func(sdk.Context, any) {
		t.Error("task panic handler received startup-loop failure")
	}))

	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("caller recovered startup panic: %v", recovered)
		}
	}()

	if err := worker.StartBlocking(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestNativeStartupIgnoresCallerCancellation keeps the version RPC blocked until
// after caller cancellation, then fails registration without starting intake.
// The real SDK call is joined; there is no abandoned bounded-wait goroutine.
func TestNativeStartupIgnoresCallerCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	address := protocoltest.GRPC(t, func(_ context.Context, method, _ string) (string, error) {
		if strings.HasSuffix(method, "/GetVersion") {
			close(entered)
			<-release

			return `{"version":"v0.114.11"}`, nil
		}

		return "", status.Error(codes.PermissionDenied, "native workflow registration denied")
	})

	host := protocoltest.Host(t, http.NotFoundHandler(), func(host *hatchet.Builder) {
		host.NoWorker(false)
		host.HostPort(address)
	})

	if err := host.Register(func(client *sdk.Client) (sdk.WorkflowBase, error) {
		return client.NewStandaloneTask("cancel-probe", func(sdk.Context, string) (string, error) {
			return "unused", nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	finished := make(chan error, 1)

	go func() {
		finished <- host.Start(ctx)
	}()

	<-entered
	cancel()
	close(release)

	err := <-finished

	if errors.Is(err, context.Canceled) || err == nil {
		t.Fatalf("native startup unexpectedly honored caller cancellation: %v", err)
	}

	if !strings.Contains(err.Error(), "native workflow registration denied") {
		t.Fatalf("native startup failure = %v", err)
	}
}
