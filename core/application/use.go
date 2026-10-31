package application

import (
	"fmt"
	"slices"
)

// Factory composes a dependency and returns its concrete value. Factory helpers
// capture explicit inputs and return function literals of this defined type;
// acquisition belongs inside the callback so skipped operations acquire nothing.
// Use returns the callback's value without cloning it or managing its lifecycle.
// Register cleanup explicitly with the composer that owns the acquired resource.
// A nil Factory is invalid, including when conditional composition skips it.
//
//	logger := composer.App().Logger()
//
//	create := application.Factory[string](func(composer *application.Composer) string {
//		return string(composer.App().Environment())
//	})
//	name := composer.Use(create)
//	logger.Info("environment", "name", name)
type Factory[T any] func(*Composer) T

// Action composes dependencies without publishing a value. Action helpers return
// function literals of this defined type and defer construction and registration
// until invocation. Use returns an empty struct for an Action; conditional Use
// methods additionally return whether the action ran. Nothing is automatically
// started or released. A nil Action is invalid even when skipped.
//
//	bind := application.Action(func(composer *application.Composer) {
//		composer.Run(serve)
//	})
//	composer.Use(bind)
type Action func(*Composer)

// step binds a callback's invocation and nil validation to its result type.
// Only Factory and Action implement this private composition protocol, keeping
// result inference static without reflection or a dependency registry.
type step[T any] interface {
	// invoke runs the callback directly in the enclosing composition scope.
	invoke(*Composer) T

	// valid reports whether the underlying function can be invoked.
	valid() bool
}

// invoke returns the factory's result unchanged. Validation belongs to Use so
// the callback executes once, outside the application's lifecycle lock.
func (factory Factory[T]) invoke(composer *Composer) T {
	return factory(composer)
}

// valid detects a typed nil factory without reflection or invocation.
func (factory Factory[T]) valid() bool {
	return factory != nil
}

// invoke runs an action in the same scope and supplies the empty result used by
// the common typed composition path. It adds no lifecycle registrations itself.
func (action Action) invoke(composer *Composer) struct{} {
	action(composer)

	return struct{}{}
}

// valid detects a typed nil action without reflection or invocation.
func (action Action) valid() bool {
	return action != nil
}

// Use invokes one Factory or Action synchronously in this composition scope and
// returns its result unchanged. An Action returns struct{} and can be called as
// a statement. Callbacks share cleanup ownership and lifecycle registrations;
// Use does not memoize, start, or automatically release dependencies.
//
// Nil operations, including typed nil functions, panic with ErrNilFunc. Calls
// outside composition panic with ErrInvalidPhase. A recorded composition
// failure aborts before invocation and prevents publication of a returned value,
// even if the callback recovered the private abort. Ordinary panics propagate
// through the enclosing composition after best-effort cleanup. Callbacks run
// outside the lifecycle lock, before any Startup callback.
//
//	logger := composer.App().Logger()
//
//	create := application.Factory[int](func(composer *application.Composer) int {
//		return 8080
//	})
//	port := composer.Use(create)
//	logger.Info("listener", "port", port)
func (composer *Composer) Use[T any](operation step[T]) T {
	value, _ := composer.UseWhen(true, operation)

	return value
}

// validateStep rejects nil operations before checking the composition phase and
// sticky failure under the lifecycle lock. It never invokes application code.
func (composer *Composer) validateStep[T any](operation step[T]) {
	if operation == nil || !operation.valid() {
		panic(fmt.Errorf("app: Use: %w", ErrNilFunc))
	}

	composer.validateComposition()
}

// validateComposition checks admission and a previously recorded failure while
// holding the lifecycle lock. Both invocation and result publication use this
// check, so a recovered composition failure cannot publish a dependency.
func (composer *Composer) validateComposition() {
	composer.scope.Validate()
}

// UseWhen invokes a Factory or Action synchronously when condition is true.
// Selected operations return their actual result and true, including legitimate
// zero results. Skipped operations return the result type's zero value and false
// without invoking the callback. The caller evaluates condition immediately.
//
// Operations share the enclosing composition and cleanup owner. Nil interfaces
// and typed nil functions panic with ErrNilFunc even when skipped. Invalid phases
// panic with ErrInvalidPhase, and recorded composition failures abort even
// when skipped. A failure recovered inside an operation still prevents result
// publication. Callbacks run outside the lifecycle lock; ordinary panics receive
// the enclosing composition's cleanup and re-panic behavior.
//
//	logger := composer.App().Logger()
//
//	create := application.Factory[int](func(composer *application.Composer) int {
//		return 8080
//	})
//	if port, selected := composer.UseWhen(enableHTTP, create); selected {
//		logger.Info("listener", "port", port)
//	}
func (composer *Composer) UseWhen[T any](condition bool, operation step[T]) (T, bool) {
	composer.validateStep(operation)

	if !condition {
		var zero T

		return zero, false
	}

	value := operation.invoke(composer)
	composer.validateComposition()

	return value, true
}

// UseDebug invokes a Factory or Action when the application's construction-time
// debug setting is enabled, independently of runtime log levels or environment.
// It returns the result and selection flag with UseWhen's validation, failure,
// and cleanup rules. A skipped operation returns zero and false.
//
//	composer.UseDebug(application.Action(func(composer *application.Composer) {
//		logger := composer.App().Logger()
//
//		logger.Debug("composing diagnostics")
//	}))
func (composer *Composer) UseDebug[T any](operation step[T]) (T, bool) {
	return composer.UseWhen(composer.application.Debug(), operation)
}

// UseIn invokes a Factory or Action when environment exactly matches the
// application's environment. Debug mode does not affect matching. It returns
// the result and selection flag with UseWhen's validation, failure, and cleanup
// rules; a skipped operation returns zero and false.
//
//	composer.UseIn(application.Development, application.Action(func(composer *application.Composer) {
//		logger := composer.App().Logger()
//
//		logger.Info("composing development tools")
//	}))
func (composer *Composer) UseIn[T any](environment Environment, operation step[T]) (T, bool) {
	return composer.UseWhen(composer.application.Environment() == environment, operation)
}

// UseInAny invokes a Factory or Action once when any supplied environment
// exactly matches the application's environment. Empty or nil slices skip it;
// duplicates do not repeat it. The slice is read immediately and not retained.
// It returns the result and selection flag with UseWhen's validation, failure,
// and cleanup rules; a skipped operation returns zero and false.
//
//	composer.UseInAny([]application.Environment{
//		application.Development,
//		application.Staging,
//	}, application.Action(func(composer *application.Composer) {
//		logger := composer.App().Logger()
//
//		logger.Info("composing nonproduction tools")
//	}))
func (composer *Composer) UseInAny[T any](environments []Environment, operation step[T]) (T, bool) {
	return composer.UseWhen(slices.Contains(environments, composer.application.Environment()), operation)
}
