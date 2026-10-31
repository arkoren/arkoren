package temporal_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"

	"arkoren.dev/driver/temporal"
)

// lifecycleClient exposes only the native client ownership boundary. Unexpected
// SDK calls fail through the embedded interface rather than passing silently.
type lifecycleClient struct {
	// Client keeps unused native methods outside this ownership-only seam.
	client.Client
	// closed records whether Host incorrectly released this borrowed resource.
	closed bool
}

// Close records a native release, which NewFrom must leave to its caller.
func (connection *lifecycleClient) Close() {
	connection.closed = true
}

// TestCanceledStartDoesNotPublishBorrowedHost verifies canceled startup leaves
// the host reusable and does not acquire ownership of the supplied native client.
func TestCanceledStartDoesNotPublishBorrowedHost(t *testing.T) {
	t.Parallel()

	connection := &lifecycleClient{}
	host := temporal.NewFrom(connection)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := host.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v; want context.Canceled", err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatalf("Start after canceled attempt = %v", err)
	}

	if err := host.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if connection.closed {
		t.Fatal("borrowed native client was closed")
	}
}

// TestNilBorrowedClientFailsStartup verifies NewFrom cannot publish a host that
// has neither a connected client nor permission to dial one.
func TestNilBorrowedClientFailsStartup(t *testing.T) {
	t.Parallel()

	host := temporal.NewFrom(nil)

	if err := host.Start(t.Context()); err == nil {
		t.Fatal("Start with nil native client succeeded")
	}
}

// protocolOptions installs a native gRPC interceptor without a reachable server.
// All requests pass through the pinned SDK's real conversion and policy code;
// the dialer prohibits network access even if gRPC starts a background dial.
func protocolOptions(intercept grpc.UnaryClientInterceptor) client.Options {
	return client.Options{
		HostPort: "passthrough:///temporal-test",
		ConnectionOptions: client.ConnectionOptions{
			DialOptions: []grpc.DialOption{
				grpc.WithUnaryInterceptor(intercept),
				grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
					return nil, errors.New("protocol test prohibits network access")
				}),
			},
		},
	}
}

// emptyProtocolResponse substitutes the SDK's transport with an empty successful
// protobuf response. Policy/registration fixtures use it only for methods whose
// zero response is sufficient; request-inspection fixtures install their own hook.
func emptyProtocolResponse(
	context.Context,
	string,
	any,
	any,
	*grpc.ClientConn,
	grpc.UnaryInvoker,
	...grpc.CallOption,
) error {
	return nil
}

// TestCancellationDuringNativeStartupRollsBack verifies that cancellation while
// the SDK performs non-contextual worker startup is returned once startup exits,
// rather than publishing a successfully started host on a canceled context.
func TestCancellationDuringNativeStartupRollsBack(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	options := protocolOptions(func(
		ctx context.Context,
		method string,
		_, _ any,
		_ *grpc.ClientConn,
		_ grpc.UnaryInvoker,
		_ ...grpc.CallOption,
	) error {
		if strings.HasSuffix(method, "/DescribeNamespace") {
			close(entered)
			<-release
		}

		if strings.Contains(method, "/Poll") {
			<-ctx.Done()

			return ctx.Err()
		}

		return nil
	})

	connection, err := client.NewLazyClient(options)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(connection.Close)

	host := temporal.NewFrom(connection, func(host *temporal.Builder) {
		host.WorkerStopTimeout(-time.Second)
	})
	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := host.Register(func(registry worker.Registry) {
		registry.RegisterWorkflow(func(workflow.Context) error {
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan error, 1)

	go func() {
		result <- host.Start(ctx)
	}()

	<-entered
	cancel()
	close(release)

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v; want context.Canceled", err)
	}
}

// TestRegistrationPanicReleasesOwnedNativeClient verifies a propagated user
// registration panic cannot leak the connection acquired earlier in Start.
func TestRegistrationPanicReleasesOwnedNativeClient(t *testing.T) {
	t.Parallel()

	connected := make(chan *grpc.ClientConn, 1)
	options := protocolOptions(func(
		_ context.Context,
		_ string,
		_, _ any,
		connection *grpc.ClientConn,
		_ grpc.UnaryInvoker,
		_ ...grpc.CallOption,
	) error {
		connected <- connection

		return nil
	})

	host := temporal.New(func(host *temporal.Builder) {
		host.ClientOptions(options)
	})
	marker := errors.New("registration panic")

	if err := host.Register(func(worker.Registry) {
		panic(marker)
	}); err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			got, ok := recover().(error)

			if !ok || !errors.Is(got, marker) {
				t.Errorf("Start panic = %v; want registration marker", got)
			}
		}()

		if err := host.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		t.Error("Start did not propagate the registration panic")
	}()

	if connection := <-connected; connection.GetState() != connectivity.Shutdown {
		t.Fatal("registration panic leaked the owned native connection")
	}
}

// stalledStop supplies a genuinely blocking SDK shutdown hook. Native worker
// grace waits cannot interrupt this callback; the test releases it explicitly.
type stalledStop struct {
	// activityProbe supplies registration capture and a no-poll worker startup.
	*activityProbe
	// entered signals that native shutdown reached the hook.
	entered chan struct{}
	// release allows native cleanup to continue and the owned client to close.
	release chan struct{}
}

// StopWorker waits for the test's release before forwarding native cleanup.
// This deliberately models an application hook that ignores cancellation.
func (probe *stalledStop) StopWorker(
	ctx context.Context,
	options worker.PluginStopWorkerOptions,
	next func(context.Context, worker.PluginStopWorkerOptions),
) {
	close(probe.entered)
	<-probe.release
	next(ctx, options)
}

// TestExpiredStopPreservesOwnedClientUntilNativeCleanupReturns validates the
// conditional shutdown guarantee: an expired caller can return while a native
// hook remains blocked, without closing the worker's client prematurely.
func TestExpiredStopPreservesOwnedClientUntilNativeCleanupReturns(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		probe := &stalledStop{
			activityProbe: &activityProbe{environment: (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()},
			entered:       make(chan struct{}),
			release:       make(chan struct{}),
		}
		var release sync.Once
		t.Cleanup(func() {
			release.Do(func() {
				close(probe.release)
			})
		})

		connected := make(chan *grpc.ClientConn, 1)
		options := protocolOptions(func(
			_ context.Context,
			_ string,
			_, _ any,
			connection *grpc.ClientConn,
			_ grpc.UnaryInvoker,
			_ ...grpc.CallOption,
		) error {
			connected <- connection

			return nil
		})
		options.Plugins = []client.Plugin{probe}

		host := temporal.New(func(host *temporal.Builder) {
			host.ClientOptions(options)
		})

		if err := host.Register(func(worker.Registry) {
			// Registration presence alone requests worker construction.
		}); err != nil {
			t.Fatal(err)
		}

		if err := host.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		connection := <-connected

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		if err := host.Stop(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Stop = %v; want context.Canceled", err)
		}

		<-probe.entered

		if connection.GetState() == connectivity.Shutdown {
			t.Fatal("owned client closed while native worker cleanup was blocked")
		}

		release.Do(func() {
			close(probe.release)
		})

		if err := host.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		if connection.GetState() != connectivity.Shutdown {
			t.Fatal("owned client was not closed after native cleanup returned")
		}
	})
}
