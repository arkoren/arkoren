package natsqueue_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/driver/nats/natsqueue"

	"github.com/nats-io/nats.go"
)

// TestNativeDeliveryMetadataIsDetached obtains the real delivery implementation
// through Consume while a private native peer supplies one fetched envelope.
func TestNativeDeliveryMetadataIsDetached(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		option := newPipeServer(t, nativeQueueResponse)
		driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
			"work": {Subject: "jobs.work", Durable: "work"},
		}, func(consumer *natsqueue.Builder) {
			consumer.Options(option)
		})

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			stopPipeDriver(t, driver)
		})

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		err = driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
			message := delivery.Message()
			message.Metadata["source"] = "changed"

			if delivery.Message().Metadata["source"] != "original" {
				t.Error("Message exposed internal metadata")
			}

			err := delivery.Acknowledge(ctx)
			cancel()

			return err
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

// TestRetryBlocksHeartbeatsBeforeNativeSettlement holds the native delayed-NAK
// reply while virtual time advances through a heartbeat tick. No progress
// frame may follow the NAK and overwrite the server's requested retry deadline.
func TestRetryBlocksHeartbeatsBeforeNativeSettlement(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		nakReceived := make(chan struct{})
		releaseNAK := make(chan struct{})
		var nakOnce sync.Once
		var progress atomic.Int64
		nakSeen := false
		option := newPipeServer(t, func(request *nats.Msg) *nats.Msg {
			if strings.HasPrefix(string(request.Data), "-NAK") {
				nakSeen = true
				close(nakReceived)
				<-releaseNAK
			}

			if string(request.Data) == "+WPI" && nakSeen {
				progress.Add(1)
			}

			if request.Reply == "" {
				return nil
			}

			return nativeQueueResponse(request)
		})
		driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
			"work": {Subject: "jobs.work", Durable: "work"},
		}, func(consumer *natsqueue.Builder) {
			consumer.Options(option)
		})

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			stopPipeDriver(t, driver)
		})

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
		settled := make(chan error, 1)
		handled := make(chan struct{})
		var handlerOnce sync.Once
		var group sync.WaitGroup
		result := make(chan error, 1)
		group.Go(func() {
			result <- driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
				settled <- delivery.Retry(ctx, time.Hour)
				<-handled

				return nil
			})
		})
		t.Cleanup(func() {
			nakOnce.Do(func() {
				close(releaseNAK)
			})
			handlerOnce.Do(func() {
				close(handled)
			})
			cancel()
			group.Wait()
		})

		<-nakReceived
		time.Sleep(20 * time.Second) // Virtual time, beyond the 15-second heartbeat tick.
		nakOnce.Do(func() {
			close(releaseNAK)
		})

		if err := <-settled; err != nil {
			t.Fatal(err)
		}

		if err := driver.Connection().FlushWithContext(ctx); err != nil {
			t.Fatal(err)
		}

		if progress.Load() != 0 {
			t.Error("heartbeat followed delayed NAK")
		}

		cancel()
		handlerOnce.Do(func() {
			close(handled)
		})

		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

// TestStopReportsNativeDrainTimeout holds a native callback past the SDK's short
// drain budget. This native timeout seam uses a loopback peer: SDK drain timers
// use a process-global pool that cannot safely enter a synctest bubble.
func TestStopReportsNativeDrainTimeout(t *testing.T) {
	t.Parallel()

	endpoint := newAPIServer(t, func(subject string) []byte {
		if subject == "signal" {
			return []byte("held")
		}

		return nativeQueueResponse(&nats.Msg{Subject: subject}).Data
	})
	driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
		"work": {Subject: "jobs.work", Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.URLs(endpoint)
		consumer.Options(nats.DrainTimeout(time.Millisecond))
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		stopPipeDriver(t, driver)
	})
	_, err = driver.Connection().Subscribe("held", func(*nats.Msg) {
		close(entered)
		<-release
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Connection().PublishRequest("signal", "held", nil); err != nil {
		t.Fatal(err)
	}

	<-entered

	if err := driver.Stop(context.Background()); !errors.Is(err, nats.ErrDrainTimeout) {
		t.Errorf("Stop = %v, want native drain timeout", err)
	}

	if !driver.Connection().IsClosed() {
		t.Error("drain timeout retained owned socket")
	}
}

// TestConsumerBindingCancellationDoesNotWaitForNativeTimeout checks elapsed
// virtual time, because the SDK can eventually return context.Canceled even
// after an uncanceled lookup has consumed its entire native request timeout.
func TestConsumerBindingCancellationDoesNotWaitForNativeTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		lookups := 0
		option := newPipeServer(t, func(request *nats.Msg) *nats.Msg {
			if strings.Contains(request.Subject, "CONSUMER.INFO") {
				lookups++

				if lookups == 2 {
					cancel()

					return nil
				}
			}

			return nativeQueueResponse(request)
		})
		driver, err := natsqueue.New("JOBS", map[string]natsqueue.Queue{
			"work": {Subject: "jobs.work", Durable: "work"},
		}, func(consumer *natsqueue.Builder) {
			consumer.Options(option)
		})

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			stopPipeDriver(t, driver)
		})

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		startedAt := time.Now()

		err = driver.Consume(ctx, "work", func(context.Context, queue.Delivery) error {
			t.Error("canceled binding invoked handler")

			return nil
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}

		if time.Since(startedAt) != 0 {
			t.Fatal("canceled binding waited for native timeout")
		}
	})
}

// TestHeartbeatFailureIsLoggedOnce closes a caller-owned native connection while
// a handler is active. Renewal failure must be observable without an endless
// stream of repeated failure records.
func TestHeartbeatFailureIsLoggedOnce(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		option := newPipeServer(t, nativeQueueResponse)
		conn, err := nats.Connect("nats://in-process:4222", option, nats.NoReconnect())

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(conn.Close)
		var logs bytes.Buffer
		driver, err := natsqueue.NewFrom(conn, "JOBS", map[string]natsqueue.Queue{
			"work": {Subject: "jobs.work", Durable: "work"},
		}, func(consumer *natsqueue.Builder) {
			consumer.Logger(slog.New(slog.NewTextHandler(&logs, nil)))
		})

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		entered := make(chan struct{})
		var group sync.WaitGroup
		result := make(chan error, 1)
		group.Go(func() {
			result <- driver.Consume(ctx, "work", func(ctx context.Context, _ queue.Delivery) error {
				close(entered)
				<-ctx.Done()

				return nil
			})
		})
		t.Cleanup(func() {
			cancel()
			group.Wait()
		})

		<-entered
		conn.Close()
		time.Sleep(40 * time.Second) // Virtual time covers more than two renewal ticks.
		synctest.Wait()

		if strings.Count(logs.String(), "JetStream heartbeat failed") != 1 {
			t.Error("renewal failure was not recorded exactly once")
		}

		cancel()

		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

// stopPipeDriver forces public teardown without entering native Drain's global
// timer pool. NATS timers must never be pooled across real/virtual clock domains;
// graceful draining is tested separately through ordinary loopback protocol I/O.
func stopPipeDriver(t *testing.T, driver *natsqueue.Driver) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := driver.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
}

// nativeQueueResponse supplies a compatible existing stream/consumer and one
// fetched envelope. It uses real SDK decoding and native acknowledgement frames;
// the driver is never substituted or accessed through private fields.
func nativeQueueResponse(request *nats.Msg) *nats.Msg {
	response := &nats.Msg{Subject: request.Reply}

	switch {
	case strings.Contains(request.Subject, "STREAM.INFO"):
		response.Data = []byte(`{"config":{"name":"JOBS","subjects":["jobs.work"]}}`)
	case strings.Contains(request.Subject, "CONSUMER.INFO"):
		response.Data = []byte(`{"stream_name":"JOBS","name":"work","config":{"durable_name":"work","filter_subject":"jobs.work","deliver_policy":"all","ack_policy":"explicit","ack_wait":30000000000,"max_ack_pending":1,"max_batch":1}}`)
	case strings.Contains(request.Subject, "CONSUMER.MSG.NEXT"):
		response.Reply = "$JS.ACK.JOBS.work.1.1.1.123456789.0"
		response.Data = []byte(`{"name":"work","queue":"work","payload":"e30=","metadata":{"source":"original"}}`)
	}

	return response
}
