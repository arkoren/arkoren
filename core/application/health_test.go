package application_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/application"
	"arkoren.dev/core/event"
)

// TestFailedStartupDiscardsChecks preserves rollback ownership of registrations.
func TestFailedStartupDiscardsChecks(t *testing.T) {
	t.Parallel()

	app := application.New()
	cause := errors.New("startup failed")

	err := app.Start(t.Context(), func(compose *application.Composer) {
		compose.Check("database", func(context.Context) error {
			t.Fatal("failed startup retained a runnable check")

			return nil
		})
		compose.Startup(func(context.Context) error {
			return cause
		})
	})

	if !errors.Is(err, cause) {
		t.Fatalf("startup error = %v, want %v", err, cause)
	}

	if results := app.RunChecks(t.Context()); results != nil {
		t.Fatalf("checks after rollback = %+v, want nil", results)
	}
}

// TestCheckHealthPassing checks a successful named dependency check.
// A passing callback must not produce an aggregate health error.
func TestCheckHealthPassing(t *testing.T) {
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Check("ready", func(context.Context) error {
			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	mustNil(t, app.CheckHealth(context.Background()))
}

// TestCheckHealthJoinsFailuresInOrder checks aggregate error identity and text.
// Only failed checks appear, in registration order despite concurrent execution.
func TestCheckHealthJoinsFailuresInOrder(t *testing.T) {
	first := errors.New("first down")
	second := errors.New("second down")
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Check("ok", func(context.Context) error {
			return nil
		})
		scope.Check("a", func(context.Context) error {
			return first
		})
		scope.Check("b", func(context.Context) error {
			return second
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	err := app.CheckHealth(context.Background())

	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("error = %v, want both failures", err)
	}

	if got := err.Error(); got != "health a: first down\nhealth b: second down" {
		t.Fatalf("error = %q", got)
	}

	if strings.Contains(err.Error(), "ok") {
		t.Fatal("passing check reported")
	}
}

// TestCheckDuplicateNamePanics checks identity uniqueness at registration.
// Reusing a callback does not permit registering its name twice.
func TestCheckDuplicateNamePanics(t *testing.T) {
	healthy := func(context.Context) error {
		return nil
	}

	app := application.New()

	mustNil(t, app.Start(context.Background(), func(scope *application.Composer) {
		scope.Check("db", healthy)

		mustPanic(t, application.ErrDuplicateCheck, func() {
			scope.Check("db", healthy)
		})
	}))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})
}

// TestCheckHealthRecoversPanic checks recovery around a health callback.
// The resulting error must identify the check and recovered panic value.
func TestCheckHealthRecoversPanic(t *testing.T) {
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			panic("boom")
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	err := app.CheckHealth(context.Background())

	if err == nil || !strings.Contains(err.Error(), "health db: panic: boom") {
		t.Fatalf("error = %v, want recovered panic", err)
	}
}

// TestRunChecksReportsPassingAndFailingChecksInOrder checks fresh reporting.
// Results retain registration order and completion times, but no cached Since.
func TestRunChecksReportsPassingAndFailingChecksInOrder(t *testing.T) {
	down := errors.New("down")
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Check("ok", func(context.Context) error {
			return nil
		})
		scope.Check("bad", func(context.Context) error {
			return down
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	statuses := app.RunChecks(context.Background())

	if len(statuses) != 2 || statuses[0].Name != "ok" || statuses[0].Err != nil || statuses[1].Name != "bad" || !errors.Is(statuses[1].Err, down) {
		t.Fatalf("statuses = %+v", statuses)
	}

	if statuses[0].CheckedAt.IsZero() || !statuses[0].Since.IsZero() {
		t.Fatalf("times = %+v", statuses[0])
	}
}

// TestRunChecksWithoutChecksIsNil checks the absence-of-checks representation.
// A fresh run with no registrations must not manufacture a result slice.
func TestRunChecksWithoutChecksIsNil(t *testing.T) {
	if statuses := application.New().RunChecks(context.Background()); statuses != nil {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestRunChecksIsBoundedByReadinessTimeout checks cancellation of a slow pass.
// The configured budget must reach the callback as a deadline error.
func TestRunChecksIsBoundedByReadinessTimeout(t *testing.T) {
	app := application.New(func(app *application.Builder) {
		app.ReadinessTimeout(10 * time.Millisecond)
	})

	compose := func(scope *application.Composer) {
		scope.Check("slow", func(ctx context.Context) error {
			<-ctx.Done()

			return ctx.Err()
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	statuses := app.RunChecks(context.Background())

	if len(statuses) != 1 || !errors.Is(statuses[0].Err, context.DeadlineExceeded) {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// readinessInterval is short so background passes finish quickly in tests.
const readinessInterval = 10 * time.Millisecond

// readyStatus serves the ready handler once and returns its status code.
func readyStatus(t *testing.T, app *application.Application) int {
	t.Helper()

	status, _, _ := get(t, app.ReadyHandler(), http.MethodGet)

	return status
}

// waitForReady polls the ready handler until it answers want.
func waitForReady(t *testing.T, app *application.Application, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for readyStatus(t, app) != want {
		if time.Now().After(deadline) {
			t.Fatalf("ready never became %d", want)
		}

		time.Sleep(time.Millisecond)
	}
}

// startReadiness starts an application with fast readiness options.
func startReadiness(t *testing.T, opts ...func(*application.Builder)) *application.Application {
	t.Helper()

	app := application.New(append([]func(*application.Builder){func(app *application.Builder) {
		app.ReadinessInterval(readinessInterval)
	}}, opts...)...)
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	return app
}

// TestReadyAfterFirstPassingPass checks the initial readiness barrier.
// A blocked first check keeps readiness unavailable until it completes.
func TestReadyAfterFirstPassingPass(t *testing.T) {
	app := startReadiness(t)
	release := make(chan struct{})
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			<-release

			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))

	if status := readyStatus(t, app); status != http.StatusServiceUnavailable {
		t.Fatalf("ready before first pass = %d, want 503", status)
	}

	close(release)
	waitForReady(t, app, http.StatusOK)
}

// TestReadyTurnsUnreadyWhenCheckFails checks both directions of a dependency
// transition on one running application, including subsequent recovery.
func TestReadyTurnsUnreadyWhenCheckFails(t *testing.T) {
	var failing atomic.Bool
	app := startReadiness(t)
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			if failing.Load() {
				return errors.New("down")
			}

			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForReady(t, app, http.StatusOK)

	failing.Store(true)
	waitForReady(t, app, http.StatusServiceUnavailable)

	failing.Store(false)
	waitForReady(t, app, http.StatusOK)
}

// TestReadyHandlerDoesNotRunChecks checks cached probe reads under repetition.
// Fifty requests must leave the background callback count unchanged.
func TestReadyHandlerDoesNotRunChecks(t *testing.T) {
	var calls atomic.Int64
	app := startReadiness(t, func(app *application.Builder) {
		app.ReadinessInterval(time.Hour)
	})

	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			calls.Add(1)

			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForReady(t, app, http.StatusOK)

	before := calls.Load()

	for range 50 {
		readyStatus(t, app)
	}

	if after := calls.Load(); after != before || before != 1 {
		t.Fatalf("check calls = %d then %d, want 1 and unchanged", before, after)
	}
}

// TestReadinessLoopStopsOnShutdown checks that shutdown stops background work.
// Callback counts must settle and readiness must become unavailable.
func TestReadinessLoopStopsOnShutdown(t *testing.T) {
	var calls atomic.Int64
	app := startReadiness(t)
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			calls.Add(1)

			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForReady(t, app, http.StatusOK)

	mustNil(t, app.Shutdown(context.Background()))

	settled := calls.Load()
	time.Sleep(10 * readinessInterval)

	if calls.Load() != settled {
		t.Fatal("readiness loop kept running after Shutdown")
	}

	if status := readyStatus(t, app); status != http.StatusServiceUnavailable {
		t.Fatalf("ready after shutdown = %d, want 503", status)
	}
}

// TestReadyStaleResultIsUnready checks expiry while a later pass is blocked.
// A once-passing result must not keep the application ready indefinitely.
func TestReadyStaleResultIsUnready(t *testing.T) {
	var calls atomic.Int64
	app := startReadiness(t, func(app *application.Builder) {
		app.ReadinessTimeout(time.Minute)
	})

	compose := func(scope *application.Composer) {
		scope.Check("db", func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				return nil
			}

			<-ctx.Done()

			return ctx.Err()
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForReady(t, app, http.StatusOK)

	waitForReady(t, app, http.StatusServiceUnavailable)
}

// TestReadinessPassIsBoundedByTimeout checks the background pass's deadline.
// A callback waiting for cancellation must finish within the configured budget.
func TestReadinessPassIsBoundedByTimeout(t *testing.T) {
	deadlines := make(chan time.Duration, 1)
	app := startReadiness(t, func(app *application.Builder) {
		app.ReadinessTimeout(20 * time.Millisecond)
	})

	compose := func(scope *application.Composer) {
		scope.Check("slow", func(ctx context.Context) error {
			started := time.Now()
			<-ctx.Done()

			select {
			case deadlines <- time.Since(started):
			default:
			}

			return ctx.Err()
		})
	}

	mustNil(t, app.Start(context.Background(), compose))

	select {
	case elapsed := <-deadlines:
		if elapsed > time.Second {
			t.Fatalf("pass ran %v, want about 20ms", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pass was never bounded")
	}

	if status := readyStatus(t, app); status != http.StatusServiceUnavailable {
		t.Fatalf("ready = %d, want 503", status)
	}
}

// TestReadyWithoutChecksEqualsLive checks readiness with no dependencies.
// A running application need not wait for a nonexistent health pass.
func TestReadyWithoutChecksEqualsLive(t *testing.T) {
	app := startReadiness(t)

	mustNil(t, app.Start(context.Background()))

	if status := readyStatus(t, app); status != http.StatusOK {
		t.Fatalf("ready = %d, want 200", status)
	}
}

// TestReadinessDispatchesTransitions checks ordered check and application
// events across the initial success, dependency failure, and recovery.
func TestReadinessDispatchesTransitions(t *testing.T) {
	var failing atomic.Bool
	app := startReadiness(t)
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			if failing.Load() {
				return errors.New("down")
			}

			return nil
		})
	}

	seen := make(chan string, 16)
	bus := app.Events()

	must(bus.Listen(application.Ready, func(context.Context, event.Void) error {
		seen <- "ready"

		return nil
	}))
	must(bus.Listen(application.Unready, func(_ context.Context, payload application.UnreadyEvent) error {
		seen <- "unready:" + payload.Failing[0]

		return nil
	}))
	must(bus.Listen(application.CheckFailed, func(_ context.Context, payload application.CheckEvent) error {
		seen <- "failed:" + payload.Name

		return nil
	}))
	must(bus.Listen(application.CheckRecovered, func(_ context.Context, payload application.CheckEvent) error {
		seen <- "recovered:" + payload.Name

		return nil
	}))
	mustNil(t, app.Start(context.Background(), compose))
	expectEvents(t, seen, "ready")

	failing.Store(true)
	expectEvents(t, seen, "failed:db", "unready:db")

	failing.Store(false)
	expectEvents(t, seen, "recovered:db", "ready")
}

// expectEvents waits for want, in order, on seen.
func expectEvents(t *testing.T, seen <-chan string, want ...string) {
	t.Helper()

	for _, name := range want {
		select {
		case got := <-seen:
			if got != name {
				t.Fatalf("event = %q, want %q", got, name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no %q event", name)
		}
	}
}

// TestChecksNilBeforeFirstPass checks the distinction between registration
// and observation: a registered check has no cached status before startup.
func TestChecksNilBeforeFirstPass(t *testing.T) {
	app := startReadiness(t)
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			return nil
		})

		if checks := app.Checks(); checks != nil {
			t.Fatalf("checks during composition = %+v, want nil", checks)
		}
	}

	mustNil(t, app.Start(context.Background(), compose))
}

// TestChecksReportCachedStateAndSince checks transition timestamps and order.
// Only a changed check advances Since; an unchanged sibling keeps its value.
func TestChecksReportCachedStateAndSince(t *testing.T) {
	var failing atomic.Bool
	app := startReadiness(t)
	compose := func(scope *application.Composer) {
		scope.Check("db", func(context.Context) error {
			if failing.Load() {
				return errors.New("down")
			}

			return nil
		})
		scope.Check("cache", func(context.Context) error {
			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	waitForReady(t, app, http.StatusOK)

	first := app.Checks()

	if len(first) != 2 || first[0].Name != "db" || first[1].Name != "cache" {
		t.Fatalf("checks = %+v, want db then cache", first)
	}

	if first[0].Err != nil || first[0].Since.IsZero() || first[0].CheckedAt.IsZero() {
		t.Fatalf("passing check = %+v", first[0])
	}

	failing.Store(true)
	waitForReady(t, app, http.StatusServiceUnavailable)

	failed := app.Checks()

	if failed[0].Err == nil || failed[0].Err.Error() != "down" || !failed[0].Since.After(first[0].Since) {
		t.Fatalf("failed check = %+v, want Err down and a later Since than %v", failed[0], first[0].Since)
	}

	if !failed[1].Since.Equal(first[1].Since) {
		t.Fatalf("unchanged check Since moved: %v to %v", first[1].Since, failed[1].Since)
	}
}
