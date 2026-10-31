// Package lifecycle owns application execution and composition admission.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"
)

// Runtime owns one startup, runner group, and reverse-order cleanup stack.
type Runtime struct {
	// logger receives execution outcomes before telemetry finalization.
	logger *slog.Logger
	// signal publishes an execution outcome through the application's bus.
	signal func(context.Context, string, error)
	// startTelemetry activates application telemetry before startup callbacks.
	startTelemetry func(context.Context) error
	// shutdownTelemetry finalizes telemetry after all releases and notifications.
	shutdownTelemetry func(context.Context) error
	// monitor supplies the optional supervised health runner after composition.
	monitor func() func(context.Context) error
	// discardChecks releases registrations after failed startup rollback.
	discardChecks func()
	// shutdownTimeout bounds the shutdown initiated by Run.
	shutdownTimeout time.Duration
	// mu guards every field below.
	mu sync.Mutex

	// startups holds callbacks executed after successful composition and telemetry.
	startups []func(context.Context) error

	// cleanups owns all registered releases, independent of startup progress.
	cleanups []func(context.Context) error

	// runners holds workers started once startup succeeds.
	runners []func(context.Context) error

	// state is the current lifecycle phase.
	state string

	// startedAt is when the application reached [StateRunning]; zero
	// before.
	startedAt time.Time

	// cancelStart ends the startup context, letting Shutdown abort Start.
	cancelStart context.CancelFunc

	// startDone is closed once Start has returned.
	startDone chan struct{}

	// cancel ends the context shared by all runners.
	cancel context.CancelFunc

	// done is closed once the runner group has finished. Without runners it
	// is closed when the runner context ends.
	done chan struct{}

	// err is the first unexpected runner failure.
	err error

	// stopped is closed once shutdown has finished.
	stopped chan struct{}

	// stopErr is the result of the single shutdown pass.
	stopErr error
}

// Execution phases are private to the owning application implementation.
const (
	StateNew      = "new"
	StateStarting = "starting"
	StateRunning  = "running"
	StateStopping = "stopping"
	StateStopped  = "stopped"
	StateFailed   = "failed"
)

// cleanupTimeout supplies a minimum uncanceled release budget.
const cleanupTimeout = 10 * time.Second

// New constructs an idle execution without running callbacks.
func New(logger *slog.Logger, timeout time.Duration,
	signal func(context.Context, string, error),
	start, stop func(context.Context) error,
	monitor func() func(context.Context) error,
	discardChecks func(),
) *Runtime {
	return &Runtime{logger: logger, shutdownTimeout: timeout, signal: signal,
		startTelemetry: start, shutdownTelemetry: stop, monitor: monitor,
		discardChecks: discardChecks, state: StateNew}
}

// Snapshot returns the phase and successful startup timestamp atomically.
func (runtime *Runtime) Snapshot() (string, time.Time) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	return runtime.state, runtime.startedAt
}

// Live reports whether the runner group is active and has not failed.
func (runtime *Runtime) Live() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	if runtime.state != StateRunning || runtime.err != nil {
		return false
	}

	select {
	case <-runtime.done:
		return false
	default:
		return true
	}
}

// Start composes dependencies synchronously in order, starts telemetry, then
// executes Startup callbacks in order and launches runners and health checks.
// All composers finish before any Startup callback runs. On failure or context
// cancellation every registered Cleanup runs once in reverse order with a
// bounded uncanceled context, followed by telemetry finalization.
// Failures are logged by the runtime before telemetry finalization; errors
// before telemetry starts reach the configured log handler only.
// Starting is dispatched after composition and telemetry startup, before user
// Startup callbacks, so its listeners can log through active telemetry.
//
// ctx supplies startup cancellation only; synchronous composition and startup
// callbacks must cooperate with it. Runners use a context that keeps ctx's values
// but is canceled solely by Shutdown or a runner failure. Composition
// programmer panics trigger best-effort rollback and are re-panicked; Startup,
// runner, and Cleanup panics retain the lifecycle panic-to-error behavior.
// Do not call Shutdown synchronously from composition or Startup.
func (runtime *Runtime) Start(ctx context.Context, compose ...func(*Scope)) error {
	runtime.mu.Lock()

	if runtime.state != StateNew {
		runtime.mu.Unlock()

		return fmt.Errorf("start: %w", ErrInvalidPhase)
	}

	ctx, cancel := context.WithCancel(ctx)
	runtime.state = StateStarting
	runtime.cancelStart = cancel
	runtime.startDone = make(chan struct{})
	runtime.mu.Unlock()

	defer close(runtime.startDone)
	defer cancel()

	startedAt := time.Now()

	composer := &Scope{runtime: runtime, ctx: ctx}

	if err := composer.compose(compose); err != nil {
		return runtime.rollback(ctx, err)
	}

	if err := runtime.startHooks(ctx, composer); err != nil {
		return runtime.rollback(ctx, err)
	}

	if err := runtime.launch(ctx); err != nil {
		return runtime.rollback(ctx, err)
	}

	runtime.logger.InfoContext(ctx, "app started", "duration", time.Since(startedAt))
	runtime.signal(ctx, "started", nil)

	return nil
}

// startHooks starts telemetry only after composition succeeds, then executes
// Starting listeners and the immutable startup list. Only Cleanup and Defer
// registration is admitted from an executing Startup callback; it is sealed
// before rollback or launch.
func (runtime *Runtime) startHooks(ctx context.Context, composer *Scope) error {
	if err := runtime.startTelemetry(ctx); err != nil {
		return err
	}

	runtime.signal(ctx, "starting", nil)

	for _, startup := range runtime.startups {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := composer.startup(startup); err != nil {
			return err
		}
	}

	return ctx.Err()
}

// rollback releases every registered resource, including those whose Startup
// was never reached. Terminal state is published only after telemetry ends.
func (runtime *Runtime) rollback(ctx context.Context, cause error) error {
	err := runtime.cleanup(ctx)
	runtime.logger.ErrorContext(context.WithoutCancel(ctx), "app failed", "err", errors.Join(cause, err))
	runtime.signal(context.WithoutCancel(ctx), "failed", cause)

	err = errors.Join(err, runtime.shutdownTelemetry(ctx))
	runtime.discardChecks()

	runtime.mu.Lock()

	runtime.runners = nil
	runtime.state = StateFailed
	runtime.mu.Unlock()

	if err == nil {
		return cause
	}

	return errors.Join(cause, err)
}

// runStops attempts every cleanup in reverse registration order even after
// another cleanup fails or the shared context expires, and joins all errors.
func runStops(ctx context.Context, stops []func(context.Context) error) error {
	errs := make([]error, 0, len(stops))

	for _, item := range slices.Backward(stops) {
		errs = append(errs, Protect(func() error {
			return item(ctx)
		}))
	}

	return errors.Join(errs...)
}

// cleanup takes ownership of all acquired releases and runs each exactly once in
// reverse registration order.
func (runtime *Runtime) cleanup(ctx context.Context) error {
	runtime.mu.Lock()

	stops := runtime.cleanups
	runtime.cleanups = nil
	runtime.mu.Unlock()

	budget := cleanupTimeout

	if deadline, ok := ctx.Deadline(); ok {
		budget = max(budget, time.Until(deadline))
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()

	return runStops(ctx, stops)
}

// launch publishes the running state and starts every runner.
func (runtime *Runtime) launch(ctx context.Context) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	runContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	runtime.cancel = cancel
	runtime.done = make(chan struct{})
	runtime.state = StateRunning
	runtime.startedAt = time.Now()
	runners := slices.Clone(runtime.runners)

	if monitor := runtime.monitor(); monitor != nil {
		runners = append(runners, monitor)
	}

	var group sync.WaitGroup

	for _, run := range runners {
		group.Go(func() {
			runtime.runOne(runContext, run)
		})
	}

	go func() {
		group.Wait()

		if len(runners) == 0 {
			<-runContext.Done()
		}

		close(runtime.done)
	}()

	return nil
}

// runOne runs one worker and records the first failure. Any result after
// the shared context ends is a normal stop. A panic counts as a failure.
func (runtime *Runtime) runOne(ctx context.Context, run func(context.Context) error) {
	err := Protect(func() error {
		return run(ctx)
	})

	if err == nil || ctx.Err() != nil {
		return
	}

	runtime.mu.Lock()

	first := runtime.err == nil

	if first {
		runtime.err = err
		runtime.cancel()
	}

	runtime.mu.Unlock()

	if first {
		runtime.logger.ErrorContext(context.WithoutCancel(ctx), "app failed", "err", err)
		runtime.signal(context.WithoutCancel(ctx), "failed", err)
	}
}

// Wait blocks until the runner group finishes or ctx ends, then returns the
// first runner failure, if any. Without runners it blocks until ctx ends or
// Shutdown begins. Caller cancellation alone is a normal wait completion and
// does not return ctx.Err or stop the runtime. Call Shutdown afterwards,
// or use Run to combine startup, waiting, and shutdown.
func (runtime *Runtime) Wait(ctx context.Context) error {
	runtime.mu.Lock()

	state, done := runtime.state, runtime.done
	runtime.mu.Unlock()

	if state != StateRunning && state != StateStopping && state != StateStopped {
		return fmt.Errorf("wait: %w", ErrInvalidPhase)
	}

	select {
	case <-done:
	case <-ctx.Done():
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	return runtime.err
}

// Run starts the runtime, waits for runners or ctx, then shuts down with
// the configured shutdown timeout. Composition has the same ordering, failure,
// and panic semantics as [Runtime.Start].
// Lifecycle failures are already logged when Run returns. Telemetry has been
// finalized, so callers should use the result for exit status or recovery rather
// than logging it again. Telemetry finalization failures reach the log handler
// directly because export is no longer available.
func (runtime *Runtime) Run(ctx context.Context, compose ...func(*Scope)) error {
	if err := runtime.Start(ctx, compose...); err != nil {
		return err
	}

	err := runtime.Wait(ctx)

	shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtime.shutdownTimeout)
	defer cancel()

	return errors.Join(err, runtime.Shutdown(shutdownContext))
}

// Shutdown cancels the runners, waits for them until ctx ends, and then runs
// every cleanup callback exactly once in reverse order, even if waiting
// timed out. Cleanup receives an uncanceled context with the remaining budget
// of ctx but at least ten seconds. The runtime ends in [StateStopped] only
// after telemetry finalization. Concurrent and
// later calls return the result of that single pass. Shutdown after a failed
// Start returns nil because Start already cleaned up.
//
// When telemetry is configured it is shut down last, after the "app stopped"
// log and the [Stopped] event, with the remaining budget of ctx but at least
// ten seconds, on every exit path; its error is joined into the result.
// These budgets are context deadlines, not forced interruption: a callback
// that ignores cancellation can keep cleanup or finalization from returning.
//
// While starting, Shutdown cancels startup and waits for its owner to roll back;
// ctx bounds that wait. Shutdown must not be called synchronously from
// composition, Startup, a runner, or a Cleanup callback.
func (runtime *Runtime) Shutdown(ctx context.Context) error {
	runtime.mu.Lock()

	switch runtime.state {
	case StateRunning:
		runtime.state = StateStopping
		runtime.stopped = make(chan struct{})
		runtime.mu.Unlock()

		return runtime.stop(ctx)
	case StateStarting:
		runtime.cancelStart()

		startDone := runtime.startDone
		runtime.mu.Unlock()

		return runtime.shutdownAfterStart(ctx, startDone)
	case StateStopping, StateStopped:
		stopped := runtime.stopped
		runtime.mu.Unlock()

		return runtime.awaitStop(ctx, stopped)
	case StateFailed:
		runtime.mu.Unlock()

		return nil
	default:
		runtime.mu.Unlock()

		return fmt.Errorf("shutdown: %w", ErrInvalidPhase)
	}
}

// shutdownAfterStart waits for startup rollback or publication before retrying
// shutdown admission. The caller has already canceled startup. A caller timeout
// stops waiting without interrupting Start's ownership of resource rollback.
func (runtime *Runtime) shutdownAfterStart(ctx context.Context, startDone <-chan struct{}) error {
	select {
	case <-startDone:
		return runtime.Shutdown(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stop performs the single shutdown pass.
func (runtime *Runtime) stop(ctx context.Context) error {
	runtime.logger.InfoContext(ctx, "app stopping")
	runtime.signal(ctx, "stopping", nil)
	runtime.cancel()

	var err error

	select {
	case <-runtime.done:
	case <-ctx.Done():
		err = fmt.Errorf("wait for runners: %w", ctx.Err())
	}

	err = errors.Join(err, runtime.cleanup(ctx))

	if err != nil {
		runtime.logger.ErrorContext(context.WithoutCancel(ctx), "app shutdown failed", "err", err)
	}

	runtime.logger.InfoContext(ctx, "app stopped")
	runtime.signal(context.WithoutCancel(ctx), "stopped", nil)

	err = errors.Join(err, runtime.shutdownTelemetry(ctx))

	runtime.mu.Lock()

	runtime.stopErr = err
	runtime.state = StateStopped
	close(runtime.stopped)
	runtime.mu.Unlock()

	return err
}

// awaitStop waits for the shutdown pass owned by another caller.
func (runtime *Runtime) awaitStop(ctx context.Context, stopped <-chan struct{}) error {
	select {
	case <-stopped:
	case <-ctx.Done():
		return ctx.Err()
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	return runtime.stopErr
}

// Protect runs fn and converts a panic into an error reported with its
// stack.
func Protect(fn func() error) (err error) {
	defer func() {
		recovered := recover()

		if recovered == nil {
			return
		}

		err = fmt.Errorf("panic: %v\n%s", recovered, debug.Stack())
	}()

	return fn()
}
