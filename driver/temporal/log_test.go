package temporal_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"go.temporal.io/sdk/client"
	sdklog "go.temporal.io/sdk/log"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	"arkoren.dev/driver/temporal"
)

// TestHostSuppliesDefaultSlogLoggerBeforeNativeConstruction verifies an omitted
// host logger is adapted before the SDK can substitute its unrelated default.
func TestHostSuppliesDefaultSlogLoggerBeforeNativeConstruction(t *testing.T) {
	t.Parallel()

	var logger sdklog.Logger
	options := protocolOptions(emptyProtocolResponse)
	plugin, err := sdktemporal.NewSimplePlugin(sdktemporal.SimplePluginOptions{
		Name: "logger-probe",
		ConfigureClient: func(_ context.Context, options client.PluginConfigureClientOptions) error {
			logger = options.ClientOptions.Logger

			return nil
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	options.Plugins = []client.Plugin{plugin}

	host := temporal.New(func(host *temporal.Builder) {
		host.ClientOptions(options)
	})

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := host.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if logger == nil {
		t.Fatal("host left default logging to the SDK instead of adapting slog.Default")
	}
}

// TestBorrowedClientKeepsNativeLogger verifies NewFrom's worker inherits the
// supplied client's logger; the host cannot replace shared native internals.
func TestBorrowedClientKeepsNativeLogger(t *testing.T) {
	t.Parallel()

	native := &nativeLogCapture{
		Logger:  sdklog.NewStructuredLogger(slog.New(slog.DiscardHandler)),
		message: "Stopped Worker",
		records: make(chan string, 1),
	}
	options := protocolOptions(emptyProtocolResponse)
	options.Logger = native
	probe := &activityProbe{environment: (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()}
	options.Plugins = []client.Plugin{probe}

	connection, err := client.NewLazyClient(options)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(connection.Close)

	var output bytes.Buffer
	host := temporal.NewFrom(connection, func(host *temporal.Builder) {
		host.Logger(slog.New(slog.NewTextHandler(&output, nil)))
	})

	if err := host.Register(func(worker.Registry) {
		// Registration presence requests worker construction without a service.
	}); err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := host.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-native.records:
	default:
		t.Fatal("native worker logger was replaced or did not receive cleanup")
	}

	if output.Len() != 0 {
		t.Fatalf("NewFrom wrote to inapplicable host logger: %s", output.String())
	}
}
