package gcpsecret_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/driver/gcp/gcpsecret"
)

// syntheticRead is public test data returned by the in-memory SDK server.
// It is never obtained from ADC, the environment, or a cloud resource.
const syntheticRead = "synthetic-read-not-a-secret"

// syntheticWrite is public test data sent through the SDK's request logger.
// Its base64 form is checked because protobuf JSON encodes bytes that way.
const syntheticWrite = "synthetic-write-not-a-secret"

// loggingSecretServer implements only the two payload-bearing RPCs needed to
// exercise the pinned SDK's request and response logging over an in-memory pipe.
type loggingSecretServer struct {
	// UnimplementedSecretManagerServiceServer rejects unused RPCs locally.
	secretmanagerpb.UnimplementedSecretManagerServiceServer
}

// AccessSecretVersion returns deterministic test bytes under the requested name.
// No credentials, external state, or real secret values enter the response.
func (loggingSecretServer) AccessSecretVersion(
	_ context.Context,
	request *secretmanagerpb.AccessSecretVersionRequest,
) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	return &secretmanagerpb.AccessSecretVersionResponse{
		Name: request.Name,
		Payload: &secretmanagerpb.SecretPayload{
			Data: []byte(syntheticRead),
		},
	}, nil
}

// AddSecretVersion acknowledges a version without retaining the supplied data.
// The native SDK still processes the request, including its logging path.
func (loggingSecretServer) AddSecretVersion(
	_ context.Context,
	request *secretmanagerpb.AddSecretVersionRequest,
) (*secretmanagerpb.SecretVersion, error) {
	return &secretmanagerpb.SecretVersion{Name: request.Parent + "/versions/1"}, nil
}

// localSecretConnection owns a gRPC connection whose dialer can only reach an
// in-memory server. Cleanup closes the connection and joins the serving worker;
// SDK clients using this connection need no separate connection cleanup.
func localSecretConnection(t *testing.T) *grpc.ClientConn {
	t.Helper()

	// bufferSize bounds each in-memory connection's transport buffer in bytes.
	const bufferSize = 1024 * 1024
	listener := bufconn.Listen(bufferSize)
	server := grpc.NewServer()
	secretmanagerpb.RegisterSecretManagerServiceServer(server, loggingSecretServer{})

	var workers sync.WaitGroup
	workers.Go(func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("serve local SDK transport: %v", err)
		}
	})

	t.Cleanup(func() {
		server.Stop()
		workers.Wait()
	})

	connection, err := grpc.NewClient("passthrough:///gcpsecret-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})

	return connection
}

// captureSDKStderr redirects the SDK's ambient log destination to a local file.
// Call only from a nonparallel test; cleanup restores stderr before closing it.
func captureSDKStderr(t *testing.T) *os.File {
	t.Helper()

	output, err := os.CreateTemp(t.TempDir(), "sdk-log-*")

	if err != nil {
		t.Fatal(err)
	}

	previous := os.Stderr
	os.Stderr = output
	t.Cleanup(func() {
		os.Stderr = previous

		if err := output.Close(); err != nil {
			t.Error(err)
		}
	})

	return output
}

// requireSecretValue checks the public payload and version returned by the
// driver. Failure messages omit payload contents, even for synthetic test data.
func requireSecretValue(t *testing.T, value secret.Value, data, version string) {
	t.Helper()

	if string(value.Data) != data || value.Version != version {
		t.Fatal("driver returned an unexpected synthetic payload or version")
	}
}

// exerciseSDKPayloads verifies public driver writes and reads through a supplied
// native client. Both payload directions use only fixed synthetic test data.
func exerciseSDKPayloads(t *testing.T, native *secretmanager.Client) {
	t.Helper()

	driver := gcpsecret.NewFrom(native)

	value, err := driver.Update(t.Context(), "projects/test/secrets/example", []byte(syntheticWrite))

	if err != nil {
		t.Fatal(err)
	}

	requireSecretValue(t, value, syntheticWrite, "1")

	value, err = driver.Get(t.Context(), secret.Reference{
		Name:    "projects/test/secrets/example",
		Version: "1",
	})

	if err != nil {
		t.Fatal(err)
	}

	requireSecretValue(t, value, syntheticRead, "1")
}

// TestNewReportsExplicitADCFileErrorWithoutLogging checks public construction
// with ambient debug enabled. An explicit missing local credential file prevents
// fallback to real ADC or transport dialing. This covers the constructor error
// path, not credential-token or RPC payload logging. Process state is restored
// by cleanup, so this test must remain nonparallel.
func TestNewReportsExplicitADCFileErrorWithoutLogging(t *testing.T) {
	t.Setenv("GOOGLE_SDK_GO_LOGGING_LEVEL", "debug")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing-credentials.json"))
	t.Setenv("GOOGLE_API_USE_CLIENT_CERTIFICATE", "false")
	t.Setenv("EXPERIMENTAL_GOOGLE_API_USE_S2A", "false")

	output := captureSDKStderr(t)

	_, err := gcpsecret.New(t.Context())

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("New error = %v, want explicit credential-file error", err)
	}

	logs, err := os.ReadFile(output.Name())

	if err != nil {
		t.Fatal(err)
	}

	if len(logs) != 0 {
		t.Fatalf("public construction error path emitted %d SDK log bytes", len(logs))
	}
}

// TestNewFromPreservesSDKLoggingPolicy checks that a supplied SDK client keeps
// its explicit logger. Synthetic payloads prove that both logging paths ran;
// this intentionally unsafe logger is confined to an in-memory test buffer.
func TestNewFromPreservesSDKLoggingPolicy(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	native, err := secretmanager.NewClient(t.Context(),
		option.WithGRPCConn(localSecretConnection(t)), option.WithLogger(logger))

	if err != nil {
		t.Fatal(err)
	}

	exerciseSDKPayloads(t, native)

	for _, payload := range []string{syntheticWrite, syntheticRead} {
		encoded := base64.StdEncoding.EncodeToString([]byte(payload))

		if !strings.Contains(output.String(), encoded) {
			t.Fatal("supplied SDK client's logger did not retain its payload logging policy")
		}
	}
}
