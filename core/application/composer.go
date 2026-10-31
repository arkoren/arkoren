package application

import (
	"context"

	"arkoren.dev/core/application/internal/lifecycle"
)

// Composer wires concrete dependencies for one Start execution. App exposes
// shared logging, telemetry, and events; construct other dependencies directly
// and register their lifecycle callbacks.
// Registration is synchronous: do not retain the composer for use by workers.
// Cleanup and Defer may also be called inside an executing Startup callback;
// Must, MustDefer, Require, and child composition remain composition-only.
//
//	err := app.Run(ctx, func(composer *application.Composer) {
//		composer.Startup(connect)
//		composer.Cleanup(disconnect)
//		composer.Run(serve)
//	})
//	if err != nil {
//		return err
//	}
type Composer struct {
	// application supplies public handles without dependency lookup.
	application *Application
	// scope owns registration permissions and execution-specific aborts.
	scope *lifecycle.Scope
}

// ComposerFunc composes concrete application dependencies during Start or Run.
// Callbacks execute once, synchronously in order, before any Startup callback.
// Nil callbacks are programmer errors and panic after best-effort rollback.
//
//	var compose application.ComposerFunc = func(composer *application.Composer) {
//		composer.Run(serve)
//	}
//	err := app.Run(ctx, compose)
//	if err != nil {
//		return err
//	}
type ComposerFunc = func(*Composer)

// Must returns value when err is nil, otherwise aborts this composition through
// Require. The first failure remains recorded even if application code recovers
// the execution-specific private panic. Calling outside synchronous composition
// panics with ErrInvalidPhase, including successful results. Ordinary constructor
// panics are never converted into composition errors. Dependencies and values
// are returned unchanged; no copying or dependency lookup occurs.
//
//	err := app.Start(ctx, func(composer *application.Composer) {
//		file := composer.Must(os.Open("settings.json"))
//		composer.Cleanup(func(context.Context) error {
//			return file.Close()
//		})
//	})
//	if err != nil {
//		return err
//	}
func (composer *Composer) Must[T any](value T, err error) T {
	composer.Require(err)

	return value
}

// App returns the existing application's stable service handles and settings.
// No dependency lookup or automatic construction occurs here.
//
//	logger := composer.App().Logger()
//	logger.InfoContext(composer.Context(), "wiring dependencies")
func (composer *Composer) App() *Application {
	return composer.application
}

// Context returns the startup context shared by composition and Startup.
// Shutdown cancels it while startup is in progress. Runners receive their own
// lifecycle context; do not retain this one for background work.
//
//	composer.Require(loadSettings(composer.Context()))
func (composer *Composer) Context() context.Context {
	return composer.scope.Context()
}

// Require aborts composition with err; nil is a no-op. The first failure is
// recorded before raising an execution-specific private panic, so recovering
// that panic does not make Start succeed. Calling outside an executing composer
// panics with ErrInvalidPhase, even for nil. Ordinary panics still propagate.
//
//	err := app.Start(ctx, func(composer *application.Composer) {
//		composer.Require(loadSettings(composer.Context()))
//	})
//	if err != nil {
//		return err
//	}
func (composer *Composer) Require(err error) {
	composer.scope.Require(err)
}

// Startup registers a callback run after every composer succeeds and telemetry
// starts. Callbacks run sequentially in registration order; an error prevents
// later startups and all runners. Panics become errors with a stack trace.
// Registration is permitted only during synchronous composition.
//
//	composer.Startup(listener.Start)
func (composer *Composer) Startup(fn func(context.Context) error) {
	composer.scope.Startup(fn)
}

// Cleanup registers an immediately active release, independent of whether any
// Startup callback runs. All releases run once in reverse registration order on
// failure or shutdown, before telemetry finalization, with bounded uncanceled
// context. Errors and recovered panics are joined without skipping releases.
// Registration is allowed during composition and executing Startup callbacks.
//
//	composer.Cleanup(listener.Stop)
func (composer *Composer) Cleanup(fn func(context.Context) error) {
	composer.scope.Cleanup(fn)
}

// Defer registers an infallible release on the same stack as [Composer.Cleanup].
// It runs once on failure or shutdown in reverse registration order, not when
// the enclosing Go function returns. Registration is allowed during composition
// and executing Startup callbacks. A nil release panics with [ErrNilFunc];
// release panics are handled like other cleanup panics. Use Cleanup when release
// needs a context or can return an error.
//
//	composer.Defer(cancel)
func (composer *Composer) Defer(release func()) {
	composer.scope.Defer(release)
}

// MustDefer aborts composition through [Composer.Require] when err is non-nil;
// otherwise it registers release through [Composer.Defer]. A failed acquisition
// registers no release, even if one is returned alongside the error. The caller
// that performs acquisition owns cleanup of partial results on failure.
// Like Must and Require, MustDefer is valid only during synchronous composition,
// not in Startup callbacks or runners. With nil err, a nil release is programmer
// misuse and panics with [ErrNilFunc].
//
//	events := composer.App().Events()
//
//	composer.MustDefer(events.Listen(application.Started, onStarted))
func (composer *Composer) MustDefer(release func(), err error) {
	composer.Require(err)
	composer.Defer(release)
}

// Run registers a worker launched only after all Startup callbacks succeed.
// An unexpected error or panic cancels all workers; errors after cancellation
// are normal stops. Register only during synchronous composition.
//
//	composer.Run(listener.Serve)
func (composer *Composer) Run(fn func(context.Context) error) {
	composer.scope.Run(fn)
}

// Check registers a nonempty, unique name and readiness callback during
// synchronous composition. Checks run once monitoring begins after Startup.
// Empty names or nil callbacks panic with ErrNilFunc; duplicate names panic
// with ErrDuplicateCheck and late registrations with ErrInvalidPhase.
//
//	composer.Check("database", connection.Ping)
func (composer *Composer) Check(name string, fn func(context.Context) error) {
	composer.scope.Check(name, fn, func() { application := composer.application; application.health.Add(name, fn) })
}
