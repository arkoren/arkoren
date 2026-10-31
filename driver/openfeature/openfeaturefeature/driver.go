package openfeaturefeature

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"arkoren.dev/core/service/feature"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/isolated"
)

// staleReason is OpenFeature's STALE resolution reason, which the Go SDK
// does not define a constant for.
const staleReason openfeature.Reason = "STALE"

// Lifecycle states of a Driver built by [New].
const (
	// stateNew is the state before Start; evaluations fail with ErrNotReady.
	stateNew int32 = iota

	// stateStarted is the state between a successful Start and Stop.
	stateStarted

	// stateStopped is the state after Stop; a Driver cannot be restarted.
	stateStopped
)

// Driver implements [feature.Driver] on top of an OpenFeature client. Build it
// with [New], which owns a provider, or [NewFrom], which wraps a client the
// caller manages. It is safe for concurrent use.
//
// Example:
//
//	driver := openfeaturefeature.New(provider)
//	_ = driver.Client() // Native access does not start the owned provider.
type Driver struct {
	// lifecycle serializes owned native initialization and shutdown without
	// locking evaluation or exposing an in-progress provider as ready.
	lifecycle chan struct{}

	// client evaluates the flags.
	client *openfeature.Client

	// api is the private API instance a driver built by New registers its
	// provider in. It is nil for NewFrom.
	api *openfeature.EvaluationAPI

	// provider is the provider New registers on Start. It is nil for NewFrom.
	provider openfeature.FeatureProvider

	// state is the lifecycle state. It is only consulted when the driver owns
	// its provider; a NewFrom driver is always ready to evaluate.
	state atomic.Int32
}

var _ feature.Driver = (*Driver)(nil)

// New creates a Driver that owns provider. It does no I/O: [Driver.Start]
// registers the provider and waits for it to initialize, and [Driver.Stop]
// shuts it down. The provider is registered in a private OpenFeature API
// instance, so the application's global OpenFeature provider is neither used
// nor replaced, and several drivers can coexist. Until Start succeeds,
// [Driver.Evaluate] fails with [feature.ErrNotReady].
//
// A provider can belong to one driver only, and a driver cannot be restarted
// after [Driver.Stop].
// Construction callbacks run directly once in order on one fresh Builder.
// Nil entries panic; expanded nil or empty slices omit edits. Ambient maps copy
// at input and publication, so retained drafts cannot change the context. Nested
// application values remain shared rather than recursively cloned.
//
//	driver := openfeaturefeature.New(flagdProvider, func(features *openfeaturefeature.Builder) {
//		features.EvaluationContext(openfeature.NewTargetlessEvaluationContext(map[string]any{"env": "prod"}))
//	})
func New(provider openfeature.FeatureProvider, configure ...BuilderFunc) *Driver {
	config := configureDriver(configure...)
	api := isolated.NewAPI()
	api.SetEvaluationContext(config)

	return &Driver{
		lifecycle: make(chan struct{}, 1),
		client:    api.NewClient(),
		api:       api,
		provider:  provider,
	}
}

// NewFrom creates a Driver that evaluates with client, which the caller has
// already bound to a provider and remains responsible for. The driver
// registers and shuts down nothing: [Driver.Start] and [Driver.Stop] do
// nothing, and [Driver.Ping] reports the client's provider state. Options are
// not accepted because the client carries its own evaluation context.
//
//	client := openfeature.NewClient("checkout")
//	driver := openfeaturefeature.NewFrom(client)
func NewFrom(client *openfeature.Client) *Driver {
	return &Driver{
		client: client,
	}
}

// Start registers the provider given to [New] and waits until it is ready, so
// a misconfigured provider fails fast. It returns the provider's
// initialization error, or an error wrapping ctx's if ctx ends first; after a
// failed Start the driver stays not ready and the provider is shut down.
// Starting twice, or after [Driver.Stop], fails. For a driver built by
// [NewFrom] it does nothing.
// Owned lifecycle calls serialize, and this wait is not canceled by ctx. Native
// provider callbacks must not recursively call Start/Stop. A legacy contextless
// initializer can outlive cancellation inside the SDK; provider implementations
// remain responsible for safely releasing resources in that case.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	if driver.provider == nil {
		return nil
	}

	driver.lifecycle <- struct{}{}
	defer func() {
		<-driver.lifecycle
	}()

	if driver.state.Load() != stateNew {
		return errors.New("openfeature: driver already started or stopped")
	}

	if err := driver.api.SetProviderAndWait(ctx, driver.provider); err != nil {
		driver.state.Store(stateStopped)
		// The initialization error is the one worth reporting.
		_ = driver.api.Shutdown(ctx)

		return fmt.Errorf("openfeature: start provider: %w", err)
	}

	driver.state.Store(stateStarted)

	return nil
}

// Stop shuts down the provider given to [New] and releases the private API
// instance after any concurrent Start returns. Context-aware providers receive
// ctx and control their own cancellation and cleanup; legacy Shutdown calls and
// the lifecycle serialization wait are not bounded by ctx. The SDK unregisters
// provider bindings even when shutdown reports an error. Evaluations afterwards
// fail with [feature.ErrNotReady]. An owned driver stopped before startup also
// becomes terminal, without initializing or shutting down the unused provider.
// It does nothing for a driver built by [NewFrom].
//
// Example:
//
//	if err := driver.Stop(ctx); err != nil {
//		return err // Inspect provider cleanup errors; the driver cannot restart.
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	if driver.provider == nil {
		return nil
	}

	driver.lifecycle <- struct{}{}
	defer func() {
		<-driver.lifecycle
	}()

	if driver.state.Swap(stateStopped) != stateStarted {
		return nil
	}

	return driver.api.Shutdown(ctx)
}

// Ping reports whether the provider can serve evaluations. It fails with an
// error wrapping [feature.ErrNotReady] unless the provider's state is READY,
// so a provider that is initializing, in error, or serving stale data is
// reported as not ready, as is a driver not started or already stopped. It
// does no I/O.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("openfeature: ping: %w", err)
	}

	if !driver.ready() {
		return fmt.Errorf("openfeature: ping: driver not started: %w", feature.ErrNotReady)
	}

	if state := driver.client.State(); state != openfeature.ReadyState {
		return fmt.Errorf("openfeature: ping: provider state %s: %w", state, feature.ErrNotReady)
	}

	return nil
}

// ready reports whether the driver may evaluate: always for a [NewFrom]
// driver, and between Start and Stop for one built by [New].
func (driver *Driver) ready() bool {
	return driver.provider == nil || driver.state.Load() == stateStarted
}

// Evaluate implements [feature.Driver]. It calls the SDK's value-details
// method for request.Kind with the zero value as the default, and maps the
// resolution to the contract: see the package documentation for the error
// codes and reasons. A canceled ctx fails with an error wrapping its error
// before the provider is called. A resolution with the DISABLED reason returns
// no value, because OpenFeature hands back the call's default in that case.
// Empty or unknown request.Kind returns ErrTypeMismatch after cancellation is
// checked and before readiness or any native provider call.
//
// Example:
//
//	result, err := driver.Evaluate(ctx, feature.Request{Key: "checkout", Kind: feature.KindBool})
//	if err != nil {
//		return err
//	}
//	_ = result.Reason
func (driver *Driver) Evaluate(ctx context.Context, request feature.Request) (feature.Result, error) {
	if err := ctx.Err(); err != nil {
		return feature.Result{}, fmt.Errorf("openfeature: evaluate %q: %w", request.Key, err)
	}

	switch request.Kind {
	case feature.KindBool, feature.KindString, feature.KindInt, feature.KindFloat:
	default:
		return feature.Result{}, fmt.Errorf("openfeature: evaluate %q: unsupported kind %q: %w", request.Key, request.Kind, feature.ErrTypeMismatch)
	}

	if !driver.ready() {
		return feature.Result{}, fmt.Errorf("openfeature: evaluate %q: driver not started: %w", request.Key, feature.ErrNotReady)
	}

	evalCtx := openfeature.NewEvaluationContext(request.Subject.Key, request.Subject.Attributes)

	var (
		value   any
		details openfeature.EvaluationDetails
		err     error
	)

	switch request.Kind {
	case feature.KindBool:
		var d openfeature.BooleanEvaluationDetails
		d, err = driver.client.BooleanValueDetails(ctx, request.Key, false, evalCtx)
		value, details = d.Value, d.EvaluationDetails
	case feature.KindString:
		var d openfeature.StringEvaluationDetails
		d, err = driver.client.StringValueDetails(ctx, request.Key, "", evalCtx)
		value, details = d.Value, d.EvaluationDetails
	case feature.KindInt:
		var d openfeature.IntEvaluationDetails
		d, err = driver.client.IntValueDetails(ctx, request.Key, 0, evalCtx)
		value, details = d.Value, d.EvaluationDetails
	case feature.KindFloat:
		var d openfeature.FloatEvaluationDetails
		d, err = driver.client.FloatValueDetails(ctx, request.Key, 0, evalCtx)
		value, details = d.Value, d.EvaluationDetails
	}

	if err != nil {
		return feature.Result{}, resolutionError(request.Key, details.ErrorCode, err)
	}

	result := feature.Result{
		Variant: details.Variant,
		Reason:  reasonOf(details.Reason),
	}

	if result.Reason != feature.ReasonDisabled {
		result.Value = value
	}

	return result, nil
}

// resolutionError maps the error code of a failed evaluation to the
// contract's sentinel, keeping the SDK's error in the chain. Codes without a
// sentinel produce a plain error naming the code.
func resolutionError(key string, code openfeature.ErrorCode, err error) error {
	var sentinel error

	switch code {
	case openfeature.FlagNotFoundCode:
		sentinel = feature.ErrNotFound
	case openfeature.TypeMismatchCode, openfeature.ParseErrorCode:
		sentinel = feature.ErrTypeMismatch
	case openfeature.ProviderNotReadyCode:
		sentinel = feature.ErrNotReady
	case openfeature.TargetingKeyMissingCode, openfeature.InvalidContextCode:
		sentinel = feature.ErrInvalidSubject
	default:
		return fmt.Errorf("openfeature: evaluate %q: %s: %w", key, cmp.Or(code, "unknown error"), err)
	}

	return fmt.Errorf("openfeature: evaluate %q: %w: %w", key, sentinel, err)
}

// reasonOf maps an OpenFeature resolution reason to the contract's. STALE,
// which means a value that may be out of date, maps to
// [feature.ReasonCached]; an empty or unrecognized reason maps to
// [feature.ReasonUnknown].
func reasonOf(reason openfeature.Reason) feature.Reason {
	switch reason {
	case openfeature.StaticReason:
		return feature.ReasonStatic
	case openfeature.DefaultReason:
		return feature.ReasonDefault
	case openfeature.TargetingMatchReason:
		return feature.ReasonTargetingMatch
	case openfeature.SplitReason:
		return feature.ReasonSplit
	case openfeature.CachedReason, staleReason:
		return feature.ReasonCached
	case openfeature.DisabledReason:
		return feature.ReasonDisabled
	case openfeature.ErrorReason:
		return feature.ReasonError
	default:
		return feature.ReasonUnknown
	}
}
