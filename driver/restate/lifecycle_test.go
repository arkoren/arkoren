package restate_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/driver/restate"
	"arkoren.dev/driver/restate/internal/protocoltest"
)

// TestStopBeforeServeReleasesListener covers startup rollback: another component
// can fail after this host listens but before its serving runner starts.
func TestStopBeforeServeReleasesListener(t *testing.T) {
	t.Parallel()

	host := restate.New(func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
		host.AllowInsecure(true)
	})
	bindPing(t, host)

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := host.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	address := host.Addr().String()

	if err := host.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", address)

	if err != nil {
		t.Fatalf("listener remains owned after Stop: %v", err)
	}

	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	})
}

// TestExpiredStopCancelsActiveRequest verifies release of the HTTP connection,
// not forced termination of an arbitrary handler. This handler cooperates with
// request cancellation, and the test joins both client and serving goroutines.
func TestExpiredStopCancelsActiveRequest(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	released := make(chan struct{})
	host := restate.New(func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
		host.AllowInsecure(true)
	})
	service := restatesdk.NewService("Blocked").Handler("Run", restatesdk.NewServiceHandler(
		func(ctx restatesdk.Context, _ restatesdk.Void) (restatesdk.Void, error) {
			close(started)
			<-ctx.Done()
			close(released)

			return restatesdk.Void{}, ctx.Err()
		}))

	if err := host.Bind(service); err != nil {
		t.Fatal(err)
	}

	protocoltest.Start(t, host)

	ctx, cancel := context.WithCancel(t.Context())
	transport := &http.Transport{Protocols: new(http.Protocols)}
	transport.Protocols.SetUnencryptedHTTP2(true)
	t.Cleanup(transport.CloseIdleConnections)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+host.Addr().String()+"/invoke/Blocked/Run",
		bytes.NewReader(protocoltest.Invocation{}.Bytes()))

	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Content-Type", "application/vnd.restate.invocation.v5")
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := (&http.Client{Transport: transport}).Do(request)

		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	<-started
	stopped, stop := context.WithCancel(t.Context())
	stop()

	if err := host.Stop(stopped); !errors.Is(err, context.Canceled) {
		t.Fatalf("expired Stop = %v", err)
	}

	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("active request context was not canceled by Stop")
	}
}
