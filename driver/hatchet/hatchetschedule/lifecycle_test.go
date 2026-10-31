package hatchetschedule_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/driver/hatchet"
	"arkoren.dev/driver/hatchet/hatchetschedule"
	"arkoren.dev/driver/hatchet/internal/protocoltest"
)

// TestRunUsesHostNamespace exercises public construction and the actual SDK
// REST protocol. Cron listing and creation must address the registered namespace.
func TestRunUsesHostNamespace(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	created := make(chan string, 1)
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			created <- r.URL.Path
			_, _ = fmt.Fprint(w, `{"metadata":{"id":"11111111-1111-1111-1111-111111111111"}}`)
			cancel()

			return
		}

		if got := r.URL.Query().Get("workflowName"); !strings.HasPrefix(got, "stage_") {
			t.Errorf("cron workflow filter = %q; want stage namespace", got)
		}

		_, _ = fmt.Fprint(w, `{"rows":[]}`)
	}), func(host *hatchet.Builder) {
		host.Namespace("stage")
	})
	driver, err := hatchetschedule.New(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	entry := schedule.NewEntry("daily", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
	})

	if err := driver.Run(ctx, []schedule.Entry{entry}, func(context.Context, string, []byte) error {
		return nil
	}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if got := <-created; got != "/api/v1/tenants/44444444-4444-4444-4444-444444444444/workflows/stage_arkoren-run/crons" {
		t.Fatalf("cron creation path = %q", got)
	}
}

// TestRunPreservesCronDeletionFailurePrecedence characterizes reconciliation at
// its public REST boundary: foreign rows survive and a stale owned row must be
// removed successfully before any replacement is created.
func TestRunPreservesCronDeletionFailurePrecedence(t *testing.T) {
	var created atomic.Bool
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodDelete {
			if !strings.HasSuffix(r.URL.Path, "11111111-1111-1111-1111-111111111111") {
				t.Errorf("attempted to delete foreign cron: %s", r.URL.Path)
			}

			http.Error(w, "deletion forbidden", http.StatusForbidden)

			return
		}

		if r.Method == http.MethodPost {
			created.Store(true)
		}

		if r.URL.Query().Get("workflowName") == "arkoren-run-exclusive" {
			_, _ = fmt.Fprint(w, `{"rows":[]}`)

			return
		}

		_, _ = fmt.Fprint(w, `{"rows":[{"name":"foreign","metadata":{"id":"22222222-2222-2222-2222-222222222222"},"additionalMetadata":{"arkoren_prefix":"other"}},{"name":"daily","metadata":{"id":"11111111-1111-1111-1111-111111111111"},"additionalMetadata":{"arkoren_prefix":"arkoren","arkoren_hash":"stale"}}]}`)
	}))
	driver, err := hatchetschedule.New(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	entry := schedule.NewEntry("daily", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
	})

	err = driver.Run(t.Context(), []schedule.Entry{entry}, func(context.Context, string, []byte) error {
		return nil
	})

	if err == nil || !strings.Contains(err.Error(), "delete cron") {
		t.Fatalf("Run = %v; want stale deletion failure", err)
	}

	if created.Load() {
		t.Fatal("reconciliation created replacement before stale deletion succeeded")
	}
}

// TestAssignmentBeforeRunWaitsForDispatch observes the native STARTED event
// before installing application dispatch. Startup must not lose that assignment
// merely because the scheduler runner begins after the shared worker starts.
func TestAssignmentBeforeRunWaitsForDispatch(t *testing.T) {
	worker := protocoltest.NewWorker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"rows":[]}`)
	}), "arkoren-run", `{"input":{"name":"daily"}}`)
	driver, err := hatchetschedule.New(worker.Host)

	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	<-worker.Started

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)
	var invoked atomic.Bool

	if err := driver.Run(ctx, nil, func(_ context.Context, name string, _ []byte) error {
		invoked.Store(name == "daily")
		cancel()

		return nil
	}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if !invoked.Load() {
		t.Fatal("pre-Run native assignment was lost before dispatch readiness")
	}
}

// TestNewRejectsNegativeMaxRunTime validates ordinary configuration without
// acquiring a client or retaining unusable native task registrations.
func TestNewRejectsNegativeMaxRunTime(t *testing.T) {
	t.Parallel()

	_, err := hatchetschedule.New(hatchet.New(), func(scheduler *hatchetschedule.Builder) {
		scheduler.MaxRunTime(-time.Second)
	})

	if !errors.Is(err, hatchet.ErrInvalidConfig) {
		t.Fatalf("New = %v; want ErrInvalidConfig", err)
	}
}

// TestCanceledRunCannotDispatchLaterAssignments orders native delivery after
// Run has returned. A surviving shared worker must not retain the old callback.
func TestCanceledRunCannotDispatchLaterAssignments(t *testing.T) {
	worker := protocoltest.NewWorker(t, http.NotFoundHandler(), "arkoren-run", `{"input":{"name":"daily"}}`)
	deliver := make(chan struct{})
	worker.Deliver = deliver
	driver, err := hatchetschedule.New(worker.Host)

	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var invoked atomic.Bool

	_ = driver.Run(ctx, nil, func(context.Context, string, []byte) error {
		invoked.Store(true)

		return nil
	})

	close(deliver)

	select {
	case <-worker.Outcomes:
	case <-time.After(5 * time.Second):
		t.Fatal("native task did not finish after Run ended")
	}

	if invoked.Load() {
		t.Fatal("worker invoked dispatch after Run returned")
	}
}

// TestRunRejectsFailedCronListing distinguishes unavailable inventory from an
// empty successful list through the public driver and actual SDK REST client.
func TestRunRejectsFailedCronListing(t *testing.T) {
	host := protocoltest.Host(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "cron inventory forbidden", http.StatusForbidden)
	}))
	driver, err := hatchetschedule.New(host)

	if err != nil {
		t.Fatal(err)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	if err := driver.Run(ctx, nil, func(context.Context, string, []byte) error {
		return nil
	}); err == nil {
		t.Fatal("Run accepted a failed cron inventory request")
	}
}
