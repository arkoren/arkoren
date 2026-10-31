package application_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/application"
	"arkoren.dev/core/event"
	"arkoren.dev/core/internal/mcptest"
	"arkoren.dev/core/mcp"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// mustNil stops the calling test when a lifecycle operation fails.
// Helper attribution keeps failures at the operation being checked.
func mustNil(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

// mustPanic fails the test unless fn panics with an error matching
// target under errors.Is.
func mustPanic(t *testing.T, target error, fn func()) {
	t.Helper()

	defer func() {
		recovered := recover()

		err, ok := recovered.(error)

		if !ok || !errors.Is(err, target) {
			t.Fatalf("panic = %v, want error matching %v", recovered, target)
		}
	}()

	fn()
}

// TestStartRunsHooksInOrder checks the registration order of startup hooks.
// Both hooks must complete before Start reports success.
func TestStartRunsHooksInOrder(t *testing.T) {
	var order []int
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			order = append(order, 1)

			return nil
		})
		scope.Startup(func(context.Context) error {
			order = append(order, 2)

			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("order = %v, want [1 2]", order)
	}
}

// TestStartTwice checks that a running application rejects another start.
// Rejection must preserve the invalid-phase sentinel for callers.
func TestStartTwice(t *testing.T) {
	app := application.New()

	mustNil(t, app.Start(context.Background()))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	if err := app.Start(context.Background()); !errors.Is(err, application.ErrInvalidPhase) {
		t.Fatalf("error = %v, want ErrInvalidPhase", err)
	}
}

// TestStartCanceledBeforeFirstHook checks cancellation before startup work.
// No hook may run, and the application must enter its failed state.
func TestStartCanceledBeforeFirstHook(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			called = true

			return nil
		})
	}

	if err := app.Start(ctx, compose); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}

	if called {
		t.Fatal("hook ran after cancellation")
	}

	if app.State() != application.StateFailed {
		t.Fatalf("state = %v, want StateFailed", app.State())
	}
}

// TestStartFailureRunsAllCleanup checks that cleanup is active as soon as
// composition registers it, independently of which startup hooks completed.
func TestStartFailureRunsAllCleanup(t *testing.T) {
	var order []int
	stop := func(value int) func(context.Context) error {
		return func(context.Context) error {
			order = append(order, value)

			return nil
		}
	}

	failure := errors.New("startup failed")
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			return nil
		})
		scope.Cleanup(stop(1))
		scope.Startup(func(context.Context) error {
			return nil
		})
		scope.Cleanup(stop(2))
		scope.Startup(func(context.Context) error {
			return failure
		})
		scope.Cleanup(stop(3))
	}

	if err := app.Start(context.Background(), compose); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want %v", err, failure)
	}

	if !slices.Equal(order, []int{3, 2, 1}) {
		t.Fatalf("cleanup order = %v, want [3 2 1]", order)
	}

	if app.State() != application.StateFailed {
		t.Fatalf("state = %v, want StateFailed", app.State())
	}
}

// TestStartFailureRunsStopRegisteredByFailingHook checks cleanup registered
// during startup itself, including cleanup added by the hook that fails.
func TestStartFailureRunsStopRegisteredByFailingHook(t *testing.T) {
	cleaned := false
	failure := errors.New("startup failed")
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			scope.Cleanup(func(context.Context) error {
				cleaned = true

				return nil
			})

			return failure
		})
	}

	if err := app.Start(context.Background(), compose); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want %v", err, failure)
	}

	if !cleaned {
		t.Fatal("stop registered by failing hook did not run")
	}
}

// TestStartFailureDoesNotLaunchRunners checks that workers wait for all
// startup hooks; a hook failure must leave their launch channel untouched.
func TestStartFailureDoesNotLaunchRunners(t *testing.T) {
	started := make(chan struct{}, 1)
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Run(func(context.Context) error {
			started <- struct{}{}

			return nil
		})
		scope.Startup(func(context.Context) error {
			return errors.New("boom")
		})
	}

	if err := app.Start(context.Background(), compose); err == nil {
		t.Fatal("expected error")
	}

	select {
	case <-started:
		t.Fatal("runner launched after failed start")
	case <-time.After(20 * time.Millisecond):
	}
}

// TestStartRecoversPanic checks that a startup-hook panic becomes an error.
// Recovery must also leave the application in the failed state.
func TestStartRecoversPanic(t *testing.T) {
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			panic("boom")
		})
	}

	if err := app.Start(context.Background(), compose); err == nil {
		t.Fatal("expected panic error")
	}

	if app.State() != application.StateFailed {
		t.Fatalf("state = %v, want StateFailed", app.State())
	}
}

// TestRegistrationPhases checks that a retained composer cannot mutate a
// running application, including cleanup and diagnostic registration.
func TestRegistrationPhases(t *testing.T) {
	noop := func(context.Context) error { return nil }

	app := application.New()
	var retained *application.Composer

	mustNil(t, app.Start(context.Background(), func(scope *application.Composer) {
		retained = scope
	}))
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	mustPanic(t, application.ErrInvalidPhase, func() { retained.Startup(noop) })
	mustPanic(t, application.ErrInvalidPhase, func() { retained.Cleanup(noop) })
	mustPanic(t, application.ErrInvalidPhase, func() { retained.Run(noop) })
	mustPanic(t, application.ErrInvalidPhase, func() { retained.Check("x", noop) })
}

// TestRegistrationNilFunc checks nil-function rejection while the composer
// is active, without converting wiring mistakes into lifecycle errors.
func TestRegistrationNilFunc(t *testing.T) {
	app := application.New()

	mustNil(t, app.Start(context.Background(), func(scope *application.Composer) {
		mustPanic(t, application.ErrNilFunc, func() { scope.Startup(nil) })
		mustPanic(t, application.ErrNilFunc, func() { scope.Cleanup(nil) })
		mustPanic(t, application.ErrNilFunc, func() { scope.Run(nil) })
		mustPanic(t, application.ErrNilFunc, func() { scope.Check("x", nil) })
	}))
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
}

// TestCheckEmptyName isolates the check-name validation from nil-function
// validation during synchronous composition.
func TestCheckEmptyName(t *testing.T) {
	app := application.New()

	mustNil(t, app.Start(context.Background(), func(scope *application.Composer) {
		mustPanic(t, application.ErrNilFunc, func() {
			scope.Check("", func(context.Context) error { return nil })
		})
	}))
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
}

// TestStartupMayRegisterCleanup checks resources acquired during startup.
// Other registrations are sealed before the first startup hook executes.
func TestStartupMayRegisterCleanup(t *testing.T) {
	stopped := false
	app := application.New()

	mustNil(t, app.Start(context.Background(), func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			noop := func(context.Context) error { return nil }

			mustPanic(t, application.ErrInvalidPhase, func() { scope.Startup(noop) })
			mustPanic(t, application.ErrInvalidPhase, func() { scope.Run(noop) })
			mustPanic(t, application.ErrInvalidPhase, func() { scope.Check("late", noop) })
			scope.Cleanup(func(context.Context) error {
				stopped = true

				return nil
			})

			return nil
		})
	}))
	mustNil(t, app.Shutdown(context.Background()))

	if !stopped {
		t.Fatal("startup cleanup did not run")
	}
}

// TestRunnerFailureFailsApplication checks propagation of the first worker
// failure through Wait, cancellation of its sibling, and loss of liveness.
func TestRunnerFailureFailsApplication(t *testing.T) {
	failure := errors.New("runner failed")
	canceled := make(chan struct{})
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Run(func(context.Context) error {
			return failure
		})
		scope.Run(func(ctx context.Context) error {
			<-ctx.Done()
			close(canceled)

			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	if err := app.Wait(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want %v", err, failure)
	}

	<-canceled

	if app.Live() {
		t.Fatal("application live after runner failure")
	}
}

// TestRunnerPanicFailsApplication checks recovery at the worker boundary.
// Wait must return an error instead of allowing a goroutine panic to escape.
func TestRunnerPanicFailsApplication(t *testing.T) {
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Run(func(context.Context) error {
			panic("boom")
		})
	}

	mustNil(t, app.Start(context.Background(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	if err := app.Wait(context.Background()); err == nil {
		t.Fatal("expected panic error")
	}
}

// TestRunnersUseContextDetachedFromStart checks that canceling the startup
// caller does not cancel workers after startup has completed.
func TestRunnersUseContextDetachedFromStart(t *testing.T) {
	startCtx, cancel := context.WithCancel(context.Background())
	seen := make(chan error, 1)
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Run(func(ctx context.Context) error {
			time.Sleep(20 * time.Millisecond)
			seen <- ctx.Err()
			<-ctx.Done()

			return nil
		})
	}

	mustNil(t, app.Start(startCtx, compose))
	cancel()
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	if err := <-seen; err != nil {
		t.Fatalf("runner ctx error = %v, want nil", err)
	}
}

// TestShutdownRunsStopsInReverseOnce checks reverse stop registration order.
// Repeated shutdown must not repeat effects and must retain the stopped state.
func TestShutdownRunsStopsInReverseOnce(t *testing.T) {
	var mu sync.Mutex
	var order []int
	stop := func(value int) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, value)

			return nil
		}
	}

	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Cleanup(stop(1))
		scope.Cleanup(stop(2))
	}

	mustNil(t, app.Start(context.Background(), compose))

	mustNil(t, app.Shutdown(context.Background()))
	mustNil(t, app.Shutdown(context.Background()))

	if len(order) != 2 || order[0] != 2 || order[1] != 1 {
		t.Fatalf("order = %v, want [2 1]", order)
	}

	if app.State() != application.StateStopped {
		t.Fatalf("state = %v, want StateStopped", app.State())
	}
}

// TestShutdownReturnsStopErrors checks that stop errors remain discoverable.
// A second shutdown must return the same recorded failure.
func TestShutdownReturnsStopErrors(t *testing.T) {
	failure := errors.New("stop failed")
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Cleanup(func(context.Context) error {
			return failure
		})
	}

	mustNil(t, app.Start(context.Background(), compose))

	if err := app.Shutdown(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want %v", err, failure)
	}

	if err := app.Shutdown(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("second error = %v, want %v", err, failure)
	}
}

// TestShutdownTimeoutStillRunsStops checks cleanup after worker waiting times
// out; the deadline error must not prevent registered stops from running.
func TestShutdownTimeoutStillRunsStops(t *testing.T) {
	stopped := false
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Run(func(context.Context) error {
			select {}
		})
		scope.Cleanup(func(context.Context) error {
			stopped = true

			return nil
		})
	}

	mustNil(t, app.Start(context.Background(), compose))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(cancel)

	if err := app.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}

	if !stopped {
		t.Fatal("stop did not run after timeout")
	}
}

// TestShutdownBeforeStart checks the initial lifecycle boundary.
// A never-started application must return the invalid-phase sentinel.
func TestShutdownBeforeStart(t *testing.T) {
	app := application.New()

	if err := app.Shutdown(context.Background()); !errors.Is(err, application.ErrInvalidPhase) {
		t.Fatalf("error = %v, want ErrInvalidPhase", err)
	}
}

// TestShutdownAfterFailedStart checks shutdown after startup rollback.
// The completed cleanup must permit shutdown without a second failure.
func TestShutdownAfterFailedStart(t *testing.T) {
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			return errors.New("boom")
		})
	}

	_ = app.Start(context.Background(), compose)

	mustNil(t, app.Shutdown(context.Background()))
}

// TestShutdownAbortsStart checks cancellation of an in-flight startup hook.
// Shutdown must finish while Start returns the cancellation error.
func TestShutdownAbortsStart(t *testing.T) {
	entered := make(chan struct{})
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Startup(func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()

			return ctx.Err()
		})
	}

	done := make(chan error, 1)

	go func() {
		done <- app.Start(context.Background(), compose)
	}()

	<-entered

	mustNil(t, app.Shutdown(context.Background()))

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("start error = %v, want context.Canceled", err)
	}
}

// TestWaitBeforeStart checks that waiting requires an active lifecycle.
// It must reject the new state rather than block indefinitely.
func TestWaitBeforeStart(t *testing.T) {
	app := application.New()

	if err := app.Wait(context.Background()); !errors.Is(err, application.ErrInvalidPhase) {
		t.Fatalf("error = %v, want ErrInvalidPhase", err)
	}
}

// TestRunStopsOnContextCancel checks the combined lifecycle convenience call.
// Caller cancellation must invoke stops and leave a stopped application.
func TestRunStopsOnContextCancel(t *testing.T) {
	stopped := false
	app := application.New(func(app *application.Builder) {
		app.ShutdownTimeout(time.Second)
	})

	compose := func(scope *application.Composer) {
		scope.Cleanup(func(context.Context) error {
			stopped = true

			return nil
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	mustNil(t, app.Run(ctx, compose))

	if !stopped || app.State() != application.StateStopped {
		t.Fatalf("stopped = %v, state = %v", stopped, app.State())
	}
}

// TestRunReturnsRunnerFailure checks that Run preserves the worker error.
// Automatic shutdown must not replace the original cause with cancellation.
func TestRunReturnsRunnerFailure(t *testing.T) {
	failure := errors.New("runner failed")
	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Run(func(context.Context) error {
			return failure
		})
	}

	if err := app.Run(context.Background(), compose); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want %v", err, failure)
	}
}

// TestLifecycleEvents checks lifecycle signals from one complete run.
// Their synchronous delivery must preserve start-then-stop ordering.
func TestLifecycleEvents(t *testing.T) {
	app := application.New()
	bus := app.Events()
	var mu sync.Mutex
	var names []string
	record := func(name string) func(context.Context, event.Void) error {
		return func(context.Context, event.Void) error {
			mu.Lock()
			defer mu.Unlock()
			names = append(names, name)

			return nil
		}
	}

	must(bus.Listen(application.Starting, record("starting")))
	must(bus.Listen(application.Started, record("started")))
	must(bus.Listen(application.Stopping, record("stopping")))
	must(bus.Listen(application.Stopped, record("stopped")))

	mustNil(t, app.Start(context.Background()))
	mustNil(t, app.Shutdown(context.Background()))

	want := []string{"starting", "started", "stopping", "stopped"}

	if len(names) != len(want) {
		t.Fatalf("events = %v, want %v", names, want)
	}

	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("events = %v, want %v", names, want)
		}
	}
}

// TestFailedEventCarriesError checks the failure topic's typed payload.
// Listeners must be able to match the original startup cause with errors.Is.
func TestFailedEventCarriesError(t *testing.T) {
	app := application.New()
	bus := app.Events()
	var got error

	must(bus.Listen(application.Failed, func(_ context.Context, payload application.FailedEvent) error {
		got = payload.Err

		return nil
	}))

	failure := errors.New("startup failed")
	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			return failure
		})
	}

	_ = app.Start(context.Background(), compose)

	if !errors.Is(got, failure) {
		t.Fatalf("event error = %v, want %v", got, failure)
	}
}

// TestListenerErrorsDoNotAffectLifecycle checks the observer-only boundary.
// Neither listener errors nor panics may change startup or shutdown results.
func TestListenerErrorsDoNotAffectLifecycle(t *testing.T) {
	app := application.New()
	bus := app.Events()

	must(bus.Listen(application.Starting, func(context.Context, event.Void) error {
		return errors.New("listener")
	}))
	must(bus.Listen(application.Stopped, func(context.Context, event.Void) error {
		panic("listener")
	}))

	mustNil(t, app.Start(context.Background()))
	mustNil(t, app.Shutdown(context.Background()))
}

// TestStateJSONUsesStrings locks down the lifecycle values exposed to callers.
// Decoding preserves the same state without a custom JSON codec.
func TestStateJSONUsesStrings(t *testing.T) {
	t.Parallel()

	states := []application.State{application.StateNew, application.StateStarting, application.StateRunning,
		application.StateStopping, application.StateStopped, application.StateFailed}

	data, err := json.Marshal(states)

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["new","starting","running","stopping","stopped","failed"]` {
		t.Fatalf("states JSON = %s", data)
	}

	var decoded []application.State

	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded[2] != application.StateRunning {
		t.Fatalf("decoded state = %q", decoded[2])
	}
}

// TestStateEmptyStringMeansNew preserves the original zero state's diagnostic
// meaning without making an unknown nonempty value look like a valid state.
func TestStateEmptyStringMeansNew(t *testing.T) {
	t.Parallel()

	var state application.State

	if state.String() != "new" {
		t.Fatalf("zero State.String() = %q", state.String())
	}
}

// lockedBuffer is a goroutine-safe buffer for capturing log output.
type lockedBuffer struct {
	// mu serializes log writes and snapshots of the captured bytes.
	mu sync.Mutex
	// buf holds the handler output for assertions.
	buf bytes.Buffer
}

// Write appends a handler write while excluding concurrent readers.
// It implements io.Writer so logging tests can share one capture buffer.
func (buffer *lockedBuffer) Write(p []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	return buffer.buf.Write(p)
}

// String returns a consistent snapshot of all captured handler output.
// The returned string remains safe to inspect after the lock is released.
func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	return buffer.buf.String()
}

// captureLogging returns a policy logging text to buf and recording the
// leveler it was given in got.
func captureLogging(buf *lockedBuffer, got *slog.Leveler) func(*application.Builder) {
	return func(app *application.Builder) {
		app.LogHandler(func(level slog.Leveler) slog.Handler {
			*got = level

			return slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})
		})
	}
}

// TestDefaultsDevelopment checks the default text handler and INFO threshold.
// Development must not implicitly enable debug logging.
func TestDefaultsDevelopment(t *testing.T) {
	app := application.New()

	if app.Environment() != application.Development {
		t.Fatalf("environment = %q", app.Environment())
	}

	if app.Debug() {
		t.Fatal("debug = true, want false")
	}

	if got := wrappedHandlerType(app.Logger()); got != reflect.TypeFor[*slog.TextHandler]() {
		t.Fatalf("wrapped handler = %v, want *slog.TextHandler", got)
	}

	ctx := context.Background()

	if app.Logger().Enabled(ctx, slog.LevelDebug) {
		t.Fatal("debug enabled by default")
	}

	if !app.Logger().Enabled(ctx, slog.LevelInfo) {
		t.Fatal("info disabled by default")
	}
}

// TestDefaultsStaging checks that staging selects structured JSON output.
// The configured environment must remain available through the application.
func TestDefaultsStaging(t *testing.T) {
	app := application.New(func(app *application.Builder) {
		app.Environment(application.Staging)
	})

	if app.Environment() != application.Staging {
		t.Fatalf("environment = %q", app.Environment())
	}

	if got := wrappedHandlerType(app.Logger()); got != reflect.TypeFor[*slog.JSONHandler]() {
		t.Fatalf("wrapped handler = %v, want *slog.JSONHandler", got)
	}
}

// TestDefaultsProduction checks that production selects a JSON handler.
// This guards the environment-specific default without configuring a factory.
func TestDefaultsProduction(t *testing.T) {
	app := application.New(func(app *application.Builder) {
		app.Environment(application.Production)
	})

	if got := wrappedHandlerType(app.Logger()); got != reflect.TypeFor[*slog.JSONHandler]() {
		t.Fatalf("wrapped handler = %v, want *slog.JSONHandler", got)
	}
}

// TestDefaultsUnknownEnvironment checks the conservative JSON fallback.
// Custom environment names must not accidentally select development output.
func TestDefaultsUnknownEnvironment(t *testing.T) {
	app := application.New(func(app *application.Builder) {
		app.Environment("qa")
	})

	if got := wrappedHandlerType(app.Logger()); got != reflect.TypeFor[*slog.JSONHandler]() {
		t.Fatalf("wrapped handler = %v, want *slog.JSONHandler", got)
	}
}

// TestDebugLevel checks that explicit debug mode enables DEBUG records.
// The override must work even with production's normal handler defaults.
func TestDebugLevel(t *testing.T) {
	app := application.New(func(app *application.Builder) {
		app.Environment(application.Production)
		app.Debug(true)
	})

	if !app.Debug() {
		t.Fatal("debug = false, want true")
	}

	if !app.Logger().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug level disabled")
	}
}

// TestSetLogLevel checks that existing loggers observe threshold changes.
// Records before and after each change distinguish filtering from buffering.
func TestSetLogLevel(t *testing.T) {
	buf := &lockedBuffer{}
	var level slog.Leveler
	app := application.New(captureLogging(buf, &level))
	ctx := context.Background()

	app.Logger().DebugContext(ctx, "before")
	app.SetLogLevel(slog.LevelDebug)
	app.Logger().DebugContext(ctx, "after")
	app.SetLogLevel(slog.LevelError)
	app.Logger().InfoContext(ctx, "hidden")

	out := buf.String()

	if strings.Contains(out, "before") || strings.Contains(out, "hidden") {
		t.Fatalf("unexpected output: %s", out)
	}

	if !strings.Contains(out, "after") {
		t.Fatalf("missing debug output after SetLogLevel: %s", out)
	}
}

// TestCustomHandlerReceivesLevel checks the factory's shared level control.
// Later threshold changes and application identity must reach its output.
func TestCustomHandlerReceivesLevel(t *testing.T) {
	buf := &lockedBuffer{}
	var level slog.Leveler
	app := application.New(func(app *application.Builder) {
		app.Debug(true)
	}, captureLogging(buf, &level))

	if level == nil {
		t.Fatal("builder not called")
	}

	if level.Level() != slog.LevelDebug {
		t.Fatalf("level = %v, want debug", level.Level())
	}

	app.SetLogLevel(slog.LevelWarn)

	if level.Level() != slog.LevelWarn {
		t.Fatalf("level = %v, want warn", level.Level())
	}

	app.Logger().WarnContext(t.Context(), "hello")

	if !strings.Contains(buf.String(), "env=development") {
		t.Fatalf("missing env attr: %s", buf.String())
	}
}

// TestLifecycleLogs checks the successful startup and shutdown messages.
// Timing metadata must accompany the normal lifecycle observations.
func TestLifecycleLogs(t *testing.T) {
	buf := &lockedBuffer{}
	var level slog.Leveler
	app := application.New(captureLogging(buf, &level))

	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	out := buf.String()

	for _, want := range []string{"app started", "duration=", "app stopping", "app stopped"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in: %s", want, out)
		}
	}

	if strings.Contains(out, "app starting") {
		t.Fatalf("unexpected pre-start log: %s", out)
	}
}

// TestLifecycleLogsFailed checks that a failed startup emits an ERROR record.
// Hook errors must not be reported as a successful application start.
func TestLifecycleLogsFailed(t *testing.T) {
	buf := &lockedBuffer{}
	var level slog.Leveler
	app := application.New(captureLogging(buf, &level))
	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			return context.DeadlineExceeded
		})
	}

	if err := app.Start(context.Background(), compose); err == nil {
		t.Fatal("expected start error")
	}

	out := buf.String()

	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "app failed") {
		t.Fatalf("missing failure log: %s", out)
	}
}

// timelessText returns a text handler on buf without the time attribute, so
// two outputs can be compared byte for byte.
func timelessText(buf *lockedBuffer) func(slog.Leveler) slog.Handler {
	return func(level slog.Leveler) slog.Handler {
		return slog.NewTextHandler(buf, &slog.HandlerOptions{
			Level: level,
			ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
				if len(groups) == 0 && attr.Key == slog.TimeKey {
					return slog.Attr{}
				}

				return attr
			},
		})
	}
}

// TestLoggerWithoutTelemetryWritesTheHandlersOwnOutput compares wrapped and
// plain handlers byte for byte, including attributes and nested groups.
func TestLoggerWithoutTelemetryWritesTheHandlersOwnOutput(t *testing.T) {
	got, want := &lockedBuffer{}, &lockedBuffer{}
	app := application.New(func(app *application.Builder) {
		app.Name("shop")
		app.LogHandler(timelessText(got))
	})

	var level slog.LevelVar
	plain := slog.New(timelessText(want)(&level)).With("env", "development").With("service", "shop")

	for _, logger := range []*slog.Logger{app.Logger(), plain} {
		logger.InfoContext(t.Context(), "hello", "k", "v", slog.Group("g", "n", 1))
		logger.With("a", 1).WithGroup("grp").WarnContext(t.Context(), "nested", "b", 2)
	}

	if got.String() == "" || got.String() != want.String() {
		t.Fatalf("output = %q, want %q", got.String(), want.String())
	}
}

// wrappedHandlerType returns the type of the handler the telemetry handler of
// logger wraps, since the application always wraps its handler.
func wrappedHandlerType(logger *slog.Logger) reflect.Type {
	return reflect.ValueOf(logger.Handler()).Elem().FieldByName("next").Elem().Type()
}

// orderLog is a goroutine-safe ordered list of lifecycle observations.
type orderLog struct {
	// mu serializes lifecycle observations from workers and the caller.
	mu sync.Mutex
	// items holds observations in their actual arrival order.
	items []string
}

// add appends one observation under the log's mutex.
// The ordered list lets tests compare hooks, signals, logs, and driver calls.
func (log *orderLog) add(item string) {
	log.mu.Lock()
	defer log.mu.Unlock()

	log.items = append(log.items, item)
}

// list returns a detached snapshot of the observations collected so far.
// Assertions may inspect it without racing later worker observations.
func (log *orderLog) list() []string {
	log.mu.Lock()
	defer log.mu.Unlock()

	return slices.Clone(log.items)
}

// index returns the position of item, failing the test when absent.
func (log *orderLog) index(t *testing.T, item string) int {
	t.Helper()

	position := slices.Index(log.list(), item)

	if position < 0 {
		t.Fatalf("%q not observed in %v", item, log.list())
	}

	return position
}

// orderHandler is a log handler that records messages in an orderLog.
type orderHandler struct {
	// order collects log messages alongside other lifecycle observations.
	order *orderLog
}

// Enabled accepts every record so tests can observe lifecycle logging order.
// The test handler performs no level filtering of its own.
func (handler orderHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

// Handle records only the message, prefixed to distinguish log observations.
// Attributes are irrelevant to this handler's lifecycle-order assertions.
func (handler orderHandler) Handle(_ context.Context, record slog.Record) error {
	handler.order.add("log: " + record.Message)

	return nil
}

// WithAttrs keeps the same observer because it records messages, not attributes.
// Returning the handler preserves its shared ordered destination.
func (handler orderHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

// WithGroup keeps the same observer because group names do not affect messages.
// The resulting handler still writes to the original ordered destination.
func (handler orderHandler) WithGroup(string) slog.Handler {
	return handler
}

// observedDriver wraps a Fake and records when the application starts and
// shuts it down.
type observedDriver struct {
	*telemetrytest.Fake

	// order receives "driver start" and "driver shutdown".
	order *orderLog

	// failStart and failStop inject errors.
	failStart, failStop bool

	// setup captures the resource and logger supplied during startup.
	setup telemetry.Setup
	// shutdownContext captures the cleanup budget passed to Stop.
	shutdownContext context.Context
	// shutdownErr records cancellation at Stop entry, before the context expires.
	shutdownErr error
}

// Start records startup arguments before forwarding to the telemetry fake.
// failStart injects an error without starting the wrapped driver.
func (driver *observedDriver) Start(ctx context.Context, setup telemetry.Setup) error {
	driver.order.add("driver start")

	driver.setup = setup

	if driver.failStart {
		return errors.New("start failed")
	}

	return driver.Fake.Start(ctx, setup)
}

// Stop records its call and context before forwarding cleanup to the fake.
// failStop injects a cleanup error for repeated-shutdown assertions.
func (driver *observedDriver) Stop(ctx context.Context) error {
	driver.order.add("driver shutdown")

	driver.shutdownContext = ctx
	driver.shutdownErr = ctx.Err()

	if driver.failStop {
		return errors.New("shutdown failed")
	}

	return driver.Fake.Stop(ctx)
}

// newObserved returns an application whose logs, events, hooks, and driver
// calls are recorded in one ordered list.
func newObserved(t *testing.T, opts ...func(*application.Builder)) (*application.Application, *observedDriver, *orderLog) {
	t.Helper()

	order := &orderLog{}
	driver := &observedDriver{Fake: telemetrytest.New(), order: order}

	opts = append([]func(*application.Builder){
		func(app *application.Builder) {
			app.Telemetry(telemetry.New(driver))
			app.LogHandler(func(slog.Leveler) slog.Handler {
				return orderHandler{order}
			})
		},
	}, opts...)

	app := application.New(opts...)

	must(app.Events().Listen(application.Stopped, func(context.Context, event.Void) error {
		order.add("event: stopped")

		return nil
	}))
	must(app.Events().Listen(application.Failed, func(context.Context, application.FailedEvent) error {
		order.add("event: failed")

		return nil
	}))

	return app, driver, order
}

// TestTelemetryDisabledByDefault checks the application-owned no-op service.
// Its lifecycle remains usable without enabling recording.
func TestTelemetryDisabledByDefault(t *testing.T) {
	app := application.New()

	if app.Telemetry() == nil {
		t.Fatal("Telemetry() = nil, want a no-op")
	}

	if _, span := app.Telemetry().Tracer("t").Start(t.Context(), "x"); span.IsRecording() {
		t.Fatal("default telemetry records")
	}

	mustNil(t, app.Start(t.Context()))
	mustNil(t, app.Shutdown(t.Context()))
}

// TestTelemetryWrapsLogger checks wiring of an explicit telemetry driver.
// Logging must pass through telemetry while retaining the configured driver.
func TestTelemetryWrapsLogger(t *testing.T) {
	fake := telemetrytest.New()
	app := application.New(func(app *application.Builder) {
		app.Telemetry(telemetry.New(fake))
	})

	if _, ok := app.Logger().Handler().(*slog.TextHandler); ok {
		t.Fatal("logger handler is not wrapped")
	}

	if app.Telemetry().Driver() != telemetry.Lifecycle(fake) {
		t.Fatal("Telemetry() does not wrap the configured driver")
	}
}

// checkLoggerIdentity builds an application with opts, logs one record, and
// fails the test unless every want substring appears in the output and no
// missing substring does.
func checkLoggerIdentity(t *testing.T, opts []func(*application.Builder), want, missing []string) {
	t.Helper()

	buf := &lockedBuffer{}
	var level slog.Leveler

	app := application.New(append(opts, captureLogging(buf, &level))...)
	app.Logger().InfoContext(t.Context(), "hello")

	for _, w := range want {
		if !strings.Contains(buf.String(), w) {
			t.Errorf("missing %q in %s", w, buf.String())
		}
	}

	for _, m := range missing {
		if strings.Contains(buf.String(), m) {
			t.Errorf("unexpected %q in %s", m, buf.String())
		}
	}
}

// TestLoggerIdentityAttributesNone checks identity defaults in captured logs.
// Environment appears even when service name and version are absent.
func TestLoggerIdentityAttributesNone(t *testing.T) {
	t.Parallel()

	checkLoggerIdentity(t, nil, []string{"env=development"}, []string{"service=", "version="})
}

// TestLoggerIdentityAttributesName checks service-name tagging without version.
// The default environment must remain alongside the explicit name.
func TestLoggerIdentityAttributesName(t *testing.T) {
	t.Parallel()

	checkLoggerIdentity(t, []func(*application.Builder){func(app *application.Builder) {
		app.Name("billing")
	}},
		[]string{"env=development", "service=billing"}, []string{"version="})
}

// TestLoggerIdentityAttributesBoth checks combined name and version tagging.
// Both explicit identity fields must reach the same logger output.
func TestLoggerIdentityAttributesBoth(t *testing.T) {
	t.Parallel()

	checkLoggerIdentity(t, []func(*application.Builder){func(app *application.Builder) {
		app.Name("billing")
		app.Version("1.2.3")
	}},
		[]string{"service=billing", "version=1.2.3"}, nil)
}

// TestLoggerIdentityAttributesVersionOnly checks version tagging without a name.
// The missing service must not suppress the independently configured version.
func TestLoggerIdentityAttributesVersionOnly(t *testing.T) {
	t.Parallel()

	checkLoggerIdentity(t, []func(*application.Builder){func(app *application.Builder) {
		app.Version("1.2.3")
	}},
		[]string{"version=1.2.3"}, []string{"service="})
}

// TestTelemetryStartsFirstWithSetup checks driver startup before user hooks.
// Resource identity and the unwrapped handler must reach the setup record.
func TestTelemetryStartsFirstWithSetup(t *testing.T) {
	app, driver, order := newObserved(t,
		func(app *application.Builder) {
			app.Name("billing")
			app.Version("1.2.3")
			app.Environment(application.Production)
		})

	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			order.add("hook")

			return nil
		})
	}

	mustNil(t, app.Start(t.Context(), compose))
	t.Cleanup(func() {
		_ = app.Shutdown(context.WithoutCancel(t.Context()))
	})

	if order.index(t, "driver start") > order.index(t, "hook") {
		t.Fatalf("driver did not start first: %v", order.list())
	}

	resource := driver.setup.Resource

	if resource.Service != "billing" || resource.Version != "1.2.3" || resource.Environment != "production" {
		t.Fatalf("resource = %+v", resource)
	}

	if driver.setup.Logger == nil {
		t.Fatal("setup logger is nil")
	}

	if _, wrapped := driver.setup.Logger.Handler().(orderHandler); !wrapped {
		t.Fatalf("setup logger handler = %T, want the unwrapped application handler", driver.setup.Logger.Handler())
	}
}

// TestStartingListenerLogsAfterTelemetryStarts checks listeners registered
// during composition run with active telemetry before user startup callbacks.
func TestStartingListenerLogsAfterTelemetryStarts(t *testing.T) {
	t.Parallel()

	app, driver, order := newObserved(t)
	logger := app.Logger()

	mustNil(t, app.Start(t.Context(), func(scope *application.Composer) {
		must(app.Events().Listen(application.Starting, func(ctx context.Context, _ event.Void) error {
			order.add("event: starting")
			logger.InfoContext(ctx, "starting listener")

			return nil
		}))
		scope.Startup(func(context.Context) error {
			order.add("hook")

			return nil
		})
	}))
	t.Cleanup(func() {
		mustNil(t, app.Shutdown(context.WithoutCancel(t.Context())))
	})

	driver.AssertLog(t, slog.LevelInfo, "starting listener")

	if order.index(t, "driver start") > order.index(t, "event: starting") ||
		order.index(t, "log: starting listener") > order.index(t, "hook") {
		t.Fatalf("unexpected startup order: %v", order.list())
	}
}

// TestTelemetryShutsDownLast checks telemetry's final lifecycle position.
// Stops, final logs, and final signals precede its single shutdown call.
func TestTelemetryShutsDownLast(t *testing.T) {
	app, _, order := newObserved(t)
	compose := func(scope *application.Composer) {
		scope.Cleanup(func(context.Context) error {
			order.add("stop hook")

			return nil
		})
	}

	mustNil(t, app.Start(t.Context(), compose))
	mustNil(t, app.Shutdown(t.Context()))

	items := order.list()

	if items[len(items)-1] != "driver shutdown" {
		t.Fatalf("driver shutdown is not last: %v", items)
	}

	for _, before := range []string{"stop hook", "log: app stopped", "event: stopped"} {
		if order.index(t, before) > order.index(t, "driver shutdown") {
			t.Fatalf("%q came after driver shutdown: %v", before, items)
		}
	}

	if count := strings.Count(strings.Join(items, "|"), "driver shutdown"); count != 1 {
		t.Fatalf("driver shut down %d times, want once", count)
	}

	// Later calls return the recorded result without shutting down again.
	mustNil(t, app.Shutdown(t.Context()))

	if len(order.list()) != len(items) {
		t.Fatalf("second Stop did more work: %v", order.list())
	}
}

// TestTelemetryShutsDownAfterStartFailure checks failed-start cleanup order.
// Failure observations must be recorded before telemetry stops, exactly once.
func TestTelemetryShutsDownAfterStartFailure(t *testing.T) {
	app, _, order := newObserved(t)
	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			return errors.New("hook failed")
		})
	}

	if err := app.Start(t.Context(), compose); err == nil {
		t.Fatal("expected start error")
	}

	items := order.list()

	if items[len(items)-1] != "driver shutdown" {
		t.Fatalf("driver shutdown is not last: %v", items)
	}

	for _, before := range []string{"log: app failed", "event: failed"} {
		if order.index(t, before) > order.index(t, "driver shutdown") {
			t.Fatalf("%q came after driver shutdown: %v", before, items)
		}
	}

	mustNil(t, app.Shutdown(t.Context()))

	if len(order.list()) != len(items) {
		t.Fatalf("Stop after failed start shut telemetry down again: %v", order.list())
	}
}

// TestTelemetryShutsDownAfterRunnerFailure checks cleanup through Run.
// A worker failure must still leave telemetry after the final application log.
func TestTelemetryShutsDownAfterRunnerFailure(t *testing.T) {
	app, driver, order := newObserved(t)
	compose := func(scope *application.Composer) {
		scope.Run(func(context.Context) error {
			return errors.New("runner failed")
		})
	}

	if err := app.Run(t.Context(), compose); err == nil {
		t.Fatal("expected runner error")
	}

	items := order.list()

	if items[len(items)-1] != "driver shutdown" || order.index(t, "log: app stopped") > order.index(t, "driver shutdown") {
		t.Fatalf("unexpected order: %v", items)
	}

	driver.AssertLog(t, slog.LevelError, "app failed")
}

// TestRunReportsCleanupFailureBeforeTelemetryStops checks that the executable
// can return an exit status without logging after Run has finalized telemetry.
func TestRunReportsCleanupFailureBeforeTelemetryStops(t *testing.T) {
	t.Parallel()

	app, driver, order := newObserved(t)
	cause := errors.New("cleanup failed")

	err := app.Run(t.Context(), func(scope *application.Composer) {
		scope.Cleanup(func(context.Context) error {
			return cause
		})
		scope.Run(func(context.Context) error {
			return nil
		})
	})

	if !errors.Is(err, cause) {
		t.Fatalf("Run = %v, want cleanup failure", err)
	}

	driver.AssertLog(t, slog.LevelError, "app shutdown failed")

	if order.index(t, "log: app shutdown failed") > order.index(t, "driver shutdown") {
		t.Fatalf("failure reported after telemetry stopped: %v", order.list())
	}

	before := len(order.list())

	if err := app.Shutdown(t.Context()); !errors.Is(err, cause) {
		t.Fatalf("repeated Shutdown = %v", err)
	}

	if len(order.list()) != before {
		t.Fatal("repeated shutdown logged the failure again")
	}
}

// TestRollbackReportsCleanupFailureWithStartupFailure preserves both causes in
// the one failure record delivered to telemetry before rollback finalization.
func TestRollbackReportsCleanupFailureWithStartupFailure(t *testing.T) {
	t.Parallel()

	app, driver, order := newObserved(t)
	startup := errors.New("startup failed")
	cleanup := errors.New("cleanup failed")

	err := app.Run(t.Context(), func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			return startup
		})
		scope.Cleanup(func(context.Context) error {
			return cleanup
		})
	})

	if !errors.Is(err, startup) || !errors.Is(err, cleanup) {
		t.Fatalf("Run = %v, want startup and cleanup failures", err)
	}

	driver.AssertLog(t, slog.LevelError, "app failed")

	for _, record := range driver.Logs() {
		if record.Message == "app failed" {
			cause, ok := record.Attrs["err"].Any().(error)

			if !ok || !errors.Is(cause, startup) || !errors.Is(cause, cleanup) {
				t.Fatalf("logged error = %v, want both causes", cause)
			}
		}
	}

	if order.index(t, "log: app failed") > order.index(t, "driver shutdown") {
		t.Fatalf("failure reported after telemetry stopped: %v", order.list())
	}
}

// TestShutdownReportsRunnerTimeoutBeforeTelemetryStops checks the final report
// even when a runner exhausts the waiting budget and cleanup must proceed.
func TestShutdownReportsRunnerTimeoutBeforeTelemetryStops(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		app, driver, order := newObserved(t)
		release := make(chan struct{})
		t.Cleanup(func() {
			close(release)
		})

		mustNil(t, app.Start(t.Context(), func(scope *application.Composer) {
			scope.Run(func(context.Context) error {
				<-release

				return nil
			})
		}))

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		t.Cleanup(cancel)

		if err := app.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown = %v, want runner-wait timeout", err)
		}

		driver.AssertLog(t, slog.LevelError, "app shutdown failed")

		if order.index(t, "log: app shutdown failed") > order.index(t, "driver shutdown") {
			t.Fatalf("failure reported after telemetry stopped: %v", order.list())
		}
	})
}

// TestCompositionFailureReportsWithoutStartedTelemetry keeps early failures
// visible to the configured handler even though export has not started.
func TestCompositionFailureReportsWithoutStartedTelemetry(t *testing.T) {
	t.Parallel()

	app, driver, order := newObserved(t)

	must(app.Events().Listen(application.Starting, func(context.Context, event.Void) error {
		order.add("event: starting")

		return nil
	}))

	cause := errors.New("composition failed")

	err := app.Run(t.Context(), func(scope *application.Composer) {
		scope.Require(cause)
	})

	if !errors.Is(err, cause) {
		t.Fatalf("Run = %v, want composition failure", err)
	}

	order.index(t, "log: app failed")

	if slices.Contains(order.list(), "driver start") || len(driver.Logs()) != 0 {
		t.Fatal("composition failure started telemetry")
	}

	if slices.Contains(order.list(), "event: starting") {
		t.Fatal("Starting dispatched before composition succeeded")
	}
}

// TestTelemetryStartFailureFailsStartup checks the first startup dependency.
// A driver error must prevent user hooks and put the application in failure.
func TestTelemetryStartFailureFailsStartup(t *testing.T) {
	app, driver, order := newObserved(t)
	driver.failStart = true

	must(app.Events().Listen(application.Starting, func(context.Context, event.Void) error {
		order.add("event: starting")

		return nil
	}))

	compose := func(scope *application.Composer) {
		scope.Startup(func(context.Context) error {
			order.add("hook")

			return nil
		})
	}

	err := app.Start(t.Context(), compose)

	if err == nil || !strings.Contains(err.Error(), "start failed") {
		t.Fatalf("Start = %v, want the driver error", err)
	}

	if slices.Contains(order.list(), "hook") {
		t.Fatalf("a hook ran after telemetry failed to start: %v", order.list())
	}

	if app.State() != application.StateFailed {
		t.Fatalf("state = %v, want failed", app.State())
	}

	if slices.Contains(order.list(), "event: starting") {
		t.Fatal("Starting dispatched before telemetry succeeded")
	}
}

// TestTelemetryStopErrorIsReturned checks cleanup error propagation.
// Repeated shutdown must retain the recorded telemetry failure.
func TestTelemetryStopErrorIsReturned(t *testing.T) {
	app, driver, order := newObserved(t)
	driver.failStop = true

	mustNil(t, app.Start(t.Context()))

	err := app.Shutdown(t.Context())

	if err == nil || !strings.Contains(err.Error(), "shutdown failed") {
		t.Fatalf("Stop = %v, want the driver error", err)
	}

	if again := app.Shutdown(t.Context()); again == nil || again.Error() != err.Error() {
		t.Fatalf("second Stop = %v, want %v", again, err)
	}

	if order.index(t, "log: telemetry shutdown failed") < order.index(t, "driver shutdown") {
		t.Fatalf("flush failure logged before Stop: %v", order.list())
	}

	for _, record := range driver.Logs() {
		if record.Message == "telemetry shutdown failed" {
			t.Fatal("flush failure sent back to the failed exporter")
		}
	}
}

// TestTelemetryStopBudgetCanceledContext checks independent cleanup time.
// A canceled caller must still give the driver a live context and budget.
func TestTelemetryStopBudgetCanceledContext(t *testing.T) {
	t.Parallel()

	app, driver, _ := newObserved(t)

	mustNil(t, app.Start(t.Context()))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_ = app.Shutdown(ctx)

	if driver.shutdownErr != nil {
		t.Fatalf("driver shutdown context error = %v, want a live context", driver.shutdownErr)
	}

	if deadline, ok := driver.shutdownContext.Deadline(); !ok || time.Until(deadline) < 5*time.Second {
		t.Fatalf("deadline = %v, %v; want at least the cleanup floor", deadline, ok)
	}
}

// TestTelemetryStopBudgetShortBudgetRaisedToFloor checks the cleanup minimum.
// A short caller deadline must not reduce the telemetry cleanup floor.
func TestTelemetryStopBudgetShortBudgetRaisedToFloor(t *testing.T) {
	t.Parallel()

	app, driver, _ := newObserved(t)

	mustNil(t, app.Start(t.Context()))

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	mustNil(t, app.Shutdown(ctx))

	if deadline, ok := driver.shutdownContext.Deadline(); !ok || time.Until(deadline) < 5*time.Second {
		t.Fatalf("deadline = %v, %v; want at least the cleanup floor", deadline, ok)
	}
}

// TestTelemetryExportsAndCorrelatesLogs checks identity and trace propagation.
// Captured records must retain custom attributes and the active span's trace ID.
func TestTelemetryExportsAndCorrelatesLogs(t *testing.T) {
	app, driver, _ := newObserved(t, func(app *application.Builder) {
		app.Name("billing")
		app.Version("1.2.3")
	})

	mustNil(t, app.Start(t.Context()))
	t.Cleanup(func() {
		_ = app.Shutdown(context.WithoutCancel(t.Context()))
	})

	ctx, span := app.Telemetry().Tracer("test").Start(t.Context(), "work")
	defer span.End()

	app.Logger().InfoContext(ctx, "hello", "k", "v")

	driver.AssertLog(t, slog.LevelInfo, "hello",
		slog.String("env", "development"), slog.String("service", "billing"),
		slog.String("version", "1.2.3"), slog.String("k", "v"))
	driver.AssertLog(t, slog.LevelInfo, "app started")

	logs := driver.Logs()
	last := logs[len(logs)-1]

	if last.TraceID != span.Context().TraceID.String() {
		t.Fatalf("exported log trace id = %q, want %s", last.TraceID, span.Context().TraceID)
	}
}

// TestSetDefaults checks explicit publication of application global defaults.
// Cleanup restores slog and clears telemetry so later tests remain isolated.
func TestSetDefaults(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		telemetry.SetDefault(nil)
	})

	app := application.New(func(app *application.Builder) {
		app.Telemetry(telemetry.New(telemetrytest.New()))
	})
	app.SetDefaults()

	if slog.Default().Handler() != app.Logger().Handler() {
		t.Fatal("slog default is not the application logger")
	}

	if telemetry.Default() != app.Telemetry() {
		t.Fatal("telemetry default is not the application telemetry")
	}
}

// TestLoggerCorrelatesBeforeApplicationStartup preserves local log correlation
// independently of application telemetry activation or diagnostic capture.
func TestLoggerCorrelatesBeforeApplicationStartup(t *testing.T) {
	t.Parallel()

	output := &lockedBuffer{}
	app := application.New(func(app *application.Builder) {
		app.LogHandler(func(level slog.Leveler) slog.Handler {
			return slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})
		})
	})

	ctx := telemetry.ContextWithSpanContext(t.Context(), telemetry.SpanContext{
		TraceID: telemetry.TraceID{1},
		SpanID:  telemetry.SpanID{2},
	})
	app.Logger().InfoContext(ctx, "configured")

	var record map[string]any

	if err := json.Unmarshal([]byte(output.String()), &record); err != nil {
		t.Fatal(err)
	}

	if record["trace_id"] != "01000000000000000000000000000000" {
		t.Fatalf("trace correlation missing before startup: %v", record)
	}

	if record["span_id"] != "0200000000000000" {
		t.Fatalf("span correlation missing before startup: %v", record)
	}

	if app.Telemetry().Enabled() {
		t.Fatal("local correlation enabled application recording")
	}
}

// TestStoppedListenerRunsBeforeTelemetryFinalization protects event ordering
// without interpreting the private lifecycle implementation or driver state.
func TestStoppedListenerRunsBeforeTelemetryFinalization(t *testing.T) {
	t.Parallel()

	app := application.New(func(app *application.Builder) {
		app.Telemetry(telemetry.New(memorytelemetry.New()))
	})

	var observed application.State
	active := false

	unsubscribe, err := app.Events().Listen(application.Stopped, func(context.Context, event.Void) error {
		observed = app.State()
		active = app.Telemetry().Enabled()

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(unsubscribe)

	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := app.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	if observed != application.StateStopping {
		t.Fatalf("Stopped listener observed %q, want stopping", observed)
	}

	if !active {
		t.Fatal("Stopped listener could not emit its final telemetry")
	}

	if app.State() != application.StateStopped {
		t.Fatal("Shutdown did not publish the terminal state")
	}
}

// TestStaleHealthAgreesAcrossPublicDiagnostics preserves stale cached health
// and passive HTTP/MCP reads while a deliberately uncooperative check is stuck.
func TestStaleHealthAgreesAcrossPublicDiagnostics(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		app := application.New(func(app *application.Builder) {
			app.ReadinessInterval(time.Second)
		})

		release := make(chan struct{})
		var calls atomic.Int64
		var tools []mcp.Tool

		if err := app.Start(t.Context(), func(compose *application.Composer) {
			tools = compose.Must(compose.MCPTools("app"))
			compose.Check("database", func(context.Context) error {
				if calls.Add(1) > 1 {
					<-release
				}

				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			close(release)

			if err := app.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})

		synctest.Wait()
		time.Sleep(4 * time.Second)

		response := app.ReadyHandler().Record(httptest.NewRequest(http.MethodGet, "/readyz?verbose", nil))
		t.Cleanup(func() {
			if err := response.Body.Close(); err != nil {
				t.Error(err)
			}
		})

		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("stale HTTP readiness = %d", response.StatusCode)
		}

		var report map[string]any

		if err := json.UnmarshalRead(response.Body, &report); err != nil {
			t.Fatal(err)
		}

		if report["reason"] != "health result stale" {
			t.Fatalf("stale HTTP readiness report = %v", report)
		}

		health := mcptest.Call[map[string]any](t, mcptest.Find(t, tools, "app.health"), `{}`)

		if health["status"] != "stale" {
			t.Fatalf("cached MCP health = %v", health)
		}

		ready := mcptest.Call[map[string]any](t, mcptest.Find(t, tools, "app.ready"), `{}`)

		if ready["ready"] != false || ready["reason"] != "health result stale" {
			t.Fatalf("cached MCP readiness = %v", ready)
		}

		if calls.Load() != 2 {
			t.Fatalf("passive reads ran checks: %d calls", calls.Load())
		}
	})
}

// TestReadinessIntervalIncludesPassExecutionTime distinguishes ticker cadence
// from a delay scheduled after each completed background pass.
func TestReadinessIntervalIncludesPassExecutionTime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		app := application.New(func(app *application.Builder) {
			app.ReadinessInterval(time.Second)
			app.ReadinessTimeout(2 * time.Second)
		})

		starts := make(chan time.Time, 2)

		if err := app.Start(t.Context(), func(compose *application.Composer) {
			compose.Check("database", func(ctx context.Context) error {
				starts <- time.Now()
				timer := time.NewTimer(400 * time.Millisecond)
				defer timer.Stop()

				select {
				case <-timer.C:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			if err := app.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})

		firstAt := <-starts
		secondAt := <-starts

		if secondAt.Sub(firstAt) != time.Second {
			t.Fatalf("readiness start interval = %v, want 1s", secondAt.Sub(firstAt))
		}
	})
}

// TestEventCursorPreservesWireFields protects public page and event metadata
// names without inspecting the ring or relying on private diagnostic records.
func TestEventCursorPreservesWireFields(t *testing.T) {
	t.Parallel()

	app := application.New()
	var tools []mcp.Tool

	if err := app.Start(t.Context(), func(compose *application.Composer) {
		tools = compose.Must(compose.MCPTools("app"))
	}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	app.Events().Emit(t.Context(), event.NewTopic[string]("billing.created"), "private payload")

	tool := mcptest.Find(t, tools, "app.events")

	encoded, err := tool.Call(t.Context(), []byte(`{"name":"billing.*"}`))

	if err != nil {
		t.Fatal(err)
	}

	var page map[string]any

	if err := json.Unmarshal(encoded, &page); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"items", "truncated", "cursor", "dropped", "has_more", "instance_changed"} {
		if _, present := page[field]; !present {
			t.Fatalf("event page omitted %q: %s", field, encoded)
		}
	}

	if !strings.Contains(string(encoded), `"time":`) {
		t.Fatalf("event metadata omitted its stable timestamp field: %s", encoded)
	}

	if strings.Contains(string(encoded), "private payload") {
		t.Fatalf("event capture retained payload data: %s", encoded)
	}
}

// TestStateString checks the initial state's public spelling.
// Diagnostics and inspection responses use this lowercase name.
func TestStateString(t *testing.T) {
	if got := application.StateNew.String(); got != "new" {
		t.Fatalf("String() = %q, want new", got)
	}
}

// TestStateStringStarting checks the startup transition's diagnostic spelling.
// It must remain distinct from both new and running.
func TestStateStringStarting(t *testing.T) {
	if got := application.StateStarting.String(); got != "starting" {
		t.Fatalf("String() = %q, want starting", got)
	}
}

// TestStateStringRunning checks the active lifecycle state's spelling.
// Inspection clients consume the lowercase value rather than an enum number.
func TestStateStringRunning(t *testing.T) {
	if got := application.StateRunning.String(); got != "running" {
		t.Fatalf("String() = %q, want running", got)
	}
}

// TestStateStringStopping checks the in-progress shutdown spelling.
// It must not be confused with completed shutdown.
func TestStateStringStopping(t *testing.T) {
	if got := application.StateStopping.String(); got != "stopping" {
		t.Fatalf("String() = %q, want stopping", got)
	}
}

// TestStateStringStopped checks the completed-shutdown spelling.
// The final state remains a stable lowercase diagnostic value.
func TestStateStringStopped(t *testing.T) {
	if got := application.StateStopped.String(); got != "stopped" {
		t.Fatalf("String() = %q, want stopped", got)
	}
}

// TestStateStringFailed checks the failure state's diagnostic spelling.
// It remains distinct from a normal stopped
func TestStateStringFailed(t *testing.T) {
	if got := application.StateFailed.String(); got != "failed" {
		t.Fatalf("String() = %q, want failed", got)
	}
}

// TestStateStringUnknown checks diagnostic formatting outside known states.
// The original value must remain available instead of yielding an empty name.
func TestStateStringUnknown(t *testing.T) {
	if got := application.State("paused").String(); got != "paused" {
		t.Fatalf("String() = %q, want paused", got)
	}
}
