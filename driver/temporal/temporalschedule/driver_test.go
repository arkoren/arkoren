package temporalschedule_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/scheduletest"
	"arkoren.dev/driver/temporal"
	"arkoren.dev/driver/temporal/internal/itest"
	"arkoren.dev/driver/temporal/temporalschedule"
)

// runID returns a name that does not collide with earlier runs, because the
// server keeps its schedules.
func runID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// startStack starts a host and a running driver on a task queue of its own, so
// a schedule from an earlier run does not fire into this one.
func startStack(t *testing.T, queue string, dispatch schedule.Dispatch, entries ...schedule.Entry) *temporalschedule.Driver {
	t.Helper()

	ctx := t.Context()
	host := temporal.New(func(host *temporal.Builder) {
		host.HostPort(itest.HostPort(t))
	}, func(host *temporal.Builder) {
		host.TaskQueue(queue)
	})

	driver, err := temporalschedule.New(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = host.Stop(context.Background())
	})

	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	go func() {
		_ = driver.Run(runCtx, entries, dispatch)
	}()

	return driver
}

// TestIntegrationEntryFires verifies that the real engine dispatches a
// recurring entry through the registered worker.
func TestIntegrationEntryFires(t *testing.T) {
	var fired atomic.Int32
	dispatch := func(_ context.Context, name string, _ []byte) error {
		if name == "tick" {
			fired.Add(1)
		}

		return nil
	}

	entry := schedule.NewEntry("tick", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Every(time.Second))
	})

	startStack(t, runID("tick"), dispatch, entry)

	deadline := time.Now().Add(30 * time.Second)

	for fired.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}

	if fired.Load() == 0 {
		t.Fatal("entry did not fire")
	}
}

// TestIntegrationDeferFires verifies that the real engine delivers a delayed
// one-shot job with its opaque payload intact.
func TestIntegrationDeferFires(t *testing.T) {
	payloads := make(chan string, 1)
	dispatch := func(_ context.Context, name string, payload []byte) error {
		if name == "later" {
			payloads <- string(payload)
		}

		return nil
	}

	driver := startStack(t, runID("defer"), dispatch)
	time.Sleep(time.Second)

	id, err := driver.Defer(t.Context(), schedule.Job{
		Name:    "later",
		Payload: []byte("x"),
		At:      time.Now().Add(time.Second),
	})

	if err != nil {
		t.Fatal(err)
	}

	if id == "" {
		t.Fatal("empty id")
	}

	select {
	case got := <-payloads:
		if got != "x" {
			t.Fatalf("payload = %q", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("deferred job did not fire")
	}
}

// TestIntegrationConformance verifies that the real engine satisfies the
// scheduler contract through an isolated test driver.
func TestIntegrationConformance(t *testing.T) {
	scheduletest.Run(t, func(t *testing.T) schedule.Driver {
		// A task queue of its own keeps schedules of earlier subtests silent.
		host := temporal.New(func(host *temporal.Builder) {
			host.HostPort(itest.HostPort(t))
		}, func(host *temporal.Builder) {
			host.TaskQueue(runID("conformance"))
		})

		driver, err := temporalschedule.New(host)

		if err != nil {
			t.Fatal(err)
		}

		if err := host.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			_ = host.Stop(context.Background())
		})

		return driver
	})
}

// TestRunBeforeConnectionFails verifies that an unconnected host is reported as
// a lifecycle error, rather than a nil-client panic from the native SDK adapter.
func TestRunBeforeConnectionFails(t *testing.T) {
	t.Parallel()

	driver, err := temporalschedule.New(temporal.New())

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Run(t.Context(), nil, func(context.Context, string, []byte) error {
		return nil
	})

	if !errors.Is(err, temporalschedule.ErrNotStarted) {
		t.Fatalf("Run = %v; want ErrNotStarted", err)
	}
}

// TestDeferBeforeConnectionFails verifies the public deferred-job command fails
// without dereferencing the missing host client. Defer before Run remains valid
// once the host has a client, as required by the schedule contract.
func TestDeferBeforeConnectionFails(t *testing.T) {
	t.Parallel()

	driver, err := temporalschedule.New(temporal.New())

	if err != nil {
		t.Fatal(err)
	}

	_, err = driver.Defer(t.Context(), schedule.Job{Name: "later", At: time.Now()})

	if !errors.Is(err, temporalschedule.ErrNotStarted) {
		t.Fatalf("Defer = %v; want ErrNotStarted", err)
	}
}

// TestCancelBeforeConnectionFails verifies cancellation has the same explicit
// connection prerequisite as submission, including an unknown identifier.
func TestCancelBeforeConnectionFails(t *testing.T) {
	t.Parallel()

	driver, err := temporalschedule.New(temporal.New())

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Cancel(t.Context(), "missing"); !errors.Is(err, temporalschedule.ErrNotStarted) {
		t.Fatalf("Cancel = %v; want ErrNotStarted", err)
	}
}

// TestCanceledRunNeedsNoConnection verifies cancellation is successful even
// before startup; no reconciliation or dispatch registration is needed.
func TestCanceledRunNeedsNoConnection(t *testing.T) {
	t.Parallel()

	driver, err := temporalschedule.New(temporal.New())

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Run(ctx, nil, nil); err != nil {
		t.Fatalf("Run with canceled context = %v", err)
	}
}
