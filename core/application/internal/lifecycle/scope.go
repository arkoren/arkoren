package lifecycle

import (
	"context"
	"fmt"
)

// Scope owns synchronous registration permissions for one Start execution.
// Registration is synchronous: do not retain the composer for use by workers.
// Cleanup and Defer may also be called inside an executing Startup callback;
// Require and child composition validation remain composition-only.
type Scope struct {
	// runtime owns the registered callbacks and guards composer state.
	runtime *Runtime

	// ctx carries startup cancellation and the caller's context values.
	ctx context.Context

	// phase admits registrations only during an executing callback.
	phase compositionPhase

	// cause remembers a composition abort even if runtime code recovers it.
	cause error
}

// ScopeFunc composes concrete runtime dependencies during Start or Run.
// Callbacks execute once, synchronously in order, before any Startup callback.
// Nil callbacks are programmer errors and panic after best-effort rollback.
type ScopeFunc = func(*Scope)

// compositionPhase is a registration permission, not a lifecycle state. A
// sealed composer permits no more additions, including during rollback.
type compositionPhase uint8

const (
	// compositionSealed rejects registrations between and after callbacks.
	compositionSealed compositionPhase = 0
	// compositionWiring allows all registrations while wiring dependencies.
	compositionWiring compositionPhase = 1 << iota
	// compositionStartup allows only releases for startup-acquired resources.
	compositionStartup
)

// compositionAbort carries an intentional composition error. Its owner scopes
// composition aborts to one execution, so replaying another execution's
// marker remains a programmer panic rather than a new composition failure.
type compositionAbort struct {
	// owner identifies the execution that recorded the failure.
	owner *Scope

	// err is returned unchanged when no cleanup or telemetry error is added.
	err error
}

// Context returns the startup context shared by composition and Startup.
// Shutdown cancels it while startup is in progress. Runners receive their own
// lifecycle context; do not retain this one for background work.
func (composer *Scope) Context() context.Context {
	return composer.ctx
}

// Require aborts composition with err; nil is a no-op. The first failure is
// recorded before raising an execution-specific private panic, so recovering
// that panic does not make Start succeed. Calling outside an executing composer
// panics with ErrInvalidPhase, even for nil. Ordinary panics still propagate.
func (composer *Scope) Require(err error) {
	composer.runtime.mu.Lock()
	defer composer.runtime.mu.Unlock()

	if composer.phase != compositionWiring {
		panic(fmt.Errorf("app: Require: %w", ErrInvalidPhase))
	}

	if err == nil {
		return
	}

	if composer.cause == nil {
		composer.cause = err
	}

	panic(&compositionAbort{owner: composer, err: composer.cause})
}

// register validates a lifecycle callback and appends it while holding the
// runtime's lock. Permissions describe the callback phases that admit the
// operation; nil functions and late registrations are programmer errors.
func (composer *Scope) register(name string, fn func(context.Context) error, allowed compositionPhase, add func()) {
	if fn == nil {
		panic(fmt.Errorf("app: %s: %w", name, ErrNilFunc))
	}

	composer.runtime.mu.Lock()
	defer composer.runtime.mu.Unlock()

	if composer.phase&allowed == 0 {
		panic(fmt.Errorf("app: %s: %w", name, ErrInvalidPhase))
	}

	add()
}

// Startup registers a callback run after every composer succeeds and telemetry
// starts. Callbacks run sequentially in registration order; an error prevents
// later startups and all runners. Panics become errors with a stack trace.
// Registration is permitted only during synchronous composition.
func (composer *Scope) Startup(fn func(context.Context) error) {
	composer.register("Startup", fn, compositionWiring, func() {
		composer.runtime.startups = append(composer.runtime.startups, fn)
	})
}

// Cleanup registers an immediately active release, independent of whether any
// Startup callback runs. All releases run once in reverse registration order on
// failure or shutdown, before telemetry finalization, with bounded uncanceled
// context. Errors and recovered panics are joined without skipping releases.
// Registration is allowed during composition and executing Startup callbacks.
func (composer *Scope) Cleanup(fn func(context.Context) error) {
	composer.register("Cleanup", fn, compositionWiring|compositionStartup, func() {
		composer.runtime.cleanups = append(composer.runtime.cleanups, fn)
	})
}

// Defer registers an infallible release on the same stack as [Scope.Cleanup].
// It runs once on failure or shutdown in reverse registration order, not when
// the enclosing Go function returns. Registration is allowed during composition
// and executing Startup callbacks. A nil release panics with [ErrNilFunc];
// release panics are handled like other cleanup panics. Use Cleanup when release
// needs a context or can return an error.
func (composer *Scope) Defer(release func()) {
	if release == nil {
		panic(fmt.Errorf("app: Defer: %w", ErrNilFunc))
	}

	composer.Cleanup(func(context.Context) error {
		release()

		return nil
	})
}

// Run registers a worker launched only after all Startup callbacks succeed.
// An unexpected error or panic cancels all workers; errors after cancellation
// are normal stops. Register only during synchronous composition.
func (composer *Scope) Run(fn func(context.Context) error) {
	composer.register("Run", fn, compositionWiring, func() {
		composer.runtime.runners = append(composer.runtime.runners, fn)
	})
}

// Check registers a nonempty, unique name and readiness callback during
// synchronous composition. Checks run once monitoring begins after Startup.
// Empty names or nil callbacks panic with ErrNilFunc; duplicate names panic
// through add and late registrations with ErrInvalidPhase.
func (composer *Scope) Check(name string, fn func(context.Context) error, add func()) {
	if name == "" {
		panic(fmt.Errorf("app: Check: %w", ErrNilFunc))
	}

	composer.register("Check", fn, compositionWiring, func() {
		add()
	})
}

// compose calls each wiring callback in order. Its deferred boundary seals
// registration before handling an intentional abort or rolling back an ordinary
// panic. Abort ownership prevents one execution swallowing another's marker.
func (composer *Scope) compose(callbacks []ScopeFunc) (err error) {
	defer func() {
		composer.setPhase(compositionSealed)

		if recovered := recover(); recovered != nil {
			err = composer.recoverComposition(recovered)
		}

		if composer.cause != nil {
			err = composer.cause
		}
	}()

	for _, callback := range callbacks {
		if err := composer.ctx.Err(); err != nil {
			return err
		}

		composer.setPhase(compositionWiring)
		callback(composer)
		composer.setPhase(compositionSealed)

		if composer.cause != nil {
			return composer.cause
		}
	}

	return composer.ctx.Err()
}

// recoverComposition returns only recognized intentional aborts. Programmer
// panics cause best-effort cleanup and telemetry finalization before re-panicking
// the exact original value, rather than converting construction bugs to errors.
func (composer *Scope) recoverComposition(recovered any) error {
	if abort, ok := recovered.(*compositionAbort); ok {
		if abort.owner == composer {
			return abort.err
		}
	}

	// Secondary cleanup errors cannot be returned when preserving the original
	// programmer panic, which must remain the caller-visible failure.
	_ = composer.runtime.rollback(composer.ctx, fmt.Errorf("composition panic: %v", recovered))
	panic(recovered)
}

// setPhase updates registration admission under the runtime's state lock.
// The caller always seals a callback before rollback or runner publication.
func (composer *Scope) setPhase(phase compositionPhase) {
	composer.runtime.mu.Lock()

	composer.phase = phase
	composer.runtime.mu.Unlock()
}

// startup temporarily admits resource releases while a Startup callback runs.
// protect preserves the existing lifecycle panic-to-error behavior.
func (composer *Scope) startup(fn func(context.Context) error) error {
	composer.setPhase(compositionStartup)
	defer composer.setPhase(compositionSealed)

	return Protect(func() error {
		return fn(composer.ctx)
	})
}

// Wiring reports whether this scope currently admits composition operations.
func (composer *Scope) Wiring() bool {
	composer.runtime.mu.Lock()
	defer composer.runtime.mu.Unlock()

	return composer.phase == compositionWiring
}

// Validate rejects expired scopes and rethrows a recorded execution abort.
func (composer *Scope) Validate() {
	composer.runtime.mu.Lock()
	defer composer.runtime.mu.Unlock()

	if composer.phase != compositionWiring {
		panic(fmt.Errorf("app: Use: %w", ErrInvalidPhase))
	}

	if composer.cause != nil {
		panic(&compositionAbort{owner: composer, err: composer.cause})
	}
}
