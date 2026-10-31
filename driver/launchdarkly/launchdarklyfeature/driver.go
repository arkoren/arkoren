package launchdarklyfeature

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"arkoren.dev/core/service/feature"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	"github.com/launchdarkly/go-sdk-common/v3/ldreason"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ld "github.com/launchdarkly/go-server-sdk/v7"
)

// AnonymousKey is the context key given to a subject whose
// [feature.Subject.Key] is empty. Every anonymous subject shares it, so they
// all fall into the same percentage rollout bucket.
//
// Example:
//
//	subject := ldcontext.NewBuilder(launchdarklyfeature.AnonymousKey).Anonymous(true).Build()
//	_ = subject.Key()
const AnonymousKey = "anonymous"

// Driver implements [feature.Driver] on top of the LaunchDarkly Go server SDK.
// Build it with [New], which owns the SDK client, or [NewFrom], which wraps a
// client the caller manages. It is safe for concurrent use.
//
// Example:
//
//	driver, err := launchdarklyfeature.New("sdk-key")
//	if err != nil {
//		return err
//	}
//	_ = driver.Client() // Nil until owned startup succeeds.
type Driver struct {
	// lifecycle serializes owned acquisition and release, including native hooks.
	// Evaluations keep using the atomic client without waiting on SDK startup.
	lifecycle chan struct{}

	// client evaluates the flags. It is nil until Start succeeds on a driver
	// built by New, and nil again after Stop.
	client atomic.Pointer[ld.LDClient]

	// sdkKey authenticates the client created by Start. It is empty for a
	// borrowed client constructed with NewFrom.
	sdkKey string

	// startWait bounds the SDK's initial flag synchronization for owned clients.
	startWait time.Duration

	// configure applies the native SDK hook at Start, not during construction.
	configure func(*ld.Config)

	// owned reports whether Start and Stop create and close the client.
	owned bool
}

var _ feature.Driver = (*Driver)(nil)

// New creates a Driver that owns an SDK client authenticated with sdkKey, the
// server-side SDK key of one LaunchDarkly environment. It does no I/O:
// [Driver.Start] builds the client and waits for the first flags, and
// [Driver.Stop] closes it. Until Start succeeds, [Driver.Evaluate] fails with
// [feature.ErrNotReady]. New fails if sdkKey is empty.
// Construction callbacks run directly once in order on one fresh Builder before
// key validation. Nil entries panic; expanded nil or empty slices omit edits.
// Retained drafts cannot change published settings. SDK hooks are stored until
// Start, never invoked as construction policies.
//
//	driver, err := launchdarklyfeature.New(os.Getenv("LAUNCHDARKLY_SDK_KEY"), func(features *launchdarklyfeature.Builder) {
//		features.StartWait(10*time.Second)
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(sdkKey string, configure ...BuilderFunc) (*Driver, error) {
	config := configureDriver(configure...)

	if sdkKey == "" {
		return nil, errors.New("launchdarkly: SDK key is required")
	}

	return &Driver{
		lifecycle: make(chan struct{}, 1),
		sdkKey:    sdkKey,
		startWait: cmp.Or(config.startWait, DefaultStartWait),
		configure: config.sdk,
		owned:     true,
	}, nil
}

// NewFrom creates a Driver that evaluates with client, which the caller built
// and remains responsible for. The driver creates and closes nothing:
// [Driver.Start] and [Driver.Stop] do nothing, and [Driver.Ping] reports the
// client's own state. Options are not accepted because the client is already
// configured. The SDK can return a client together with an initialization error;
// its caller must release that client even when initialization fails.
//
//	client, err := ld.MakeClient(sdkKey, 5*time.Second)
//	if client != nil {
//		defer func() {
//			_ = client.Close() // Caller-owned cleanup; retain any initialization error.
//		}()
//	}
//	if err != nil {
//		return err
//	}
//	driver := launchdarklyfeature.NewFrom(client)
//	_ = driver
func NewFrom(client *ld.LDClient) *Driver {
	driver := &Driver{}
	driver.client.Store(client)

	return driver
}

// Start builds the SDK client and waits up to [Builder.StartWait] for it to
// receive its first flags, so a wrong SDK key or an unreachable service fails
// fast. If the client is not initialized by then, Start closes it and returns
// an error; start again to retry. Starting a running driver fails. For a
// driver built by [NewFrom] it does nothing.
//
// The SDK does not take a context, so ctx is checked only before the client is
// built; the wait is bounded by [Builder.StartWait].
// Concurrent owned lifecycle calls are serialized. Stop waits for an in-progress
// acquisition, including its native hook; native hooks must not call Start/Stop
// recursively. This serialization wait is not canceled by ctx.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	if !driver.owned {
		return nil
	}

	driver.lifecycle <- struct{}{}
	defer func() {
		<-driver.lifecycle
	}()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("launchdarkly: start: %w", err)
	}

	if driver.client.Load() != nil {
		return errors.New("launchdarkly: driver already started")
	}

	var sdkConfig ld.Config

	if driver.configure != nil {
		driver.configure(&sdkConfig)
	}

	// A negative wait still means "do not wait"; the SDK treats zero that way.
	client, err := ld.MakeCustomClient(driver.sdkKey, sdkConfig, max(driver.startWait, 0))

	if client == nil {
		return fmt.Errorf("launchdarkly: start: %w", err)
	}

	if err == nil && !client.Initialized() {
		err = ld.ErrInitializationFailed
	}

	if err != nil {
		_ = client.Close() // The initialization error is the one worth reporting.

		return fmt.Errorf("launchdarkly: start: %w", err)
	}

	driver.client.Store(client)

	return nil
}

// Stop closes the SDK client built by [Driver.Start], flushing pending
// analytics events first. The SDK's Close does not take a context and blocks
// until the flush finishes, so ctx is not used to bound it. Evaluations after
// Stop fail with [feature.ErrNotReady], and the driver can be started again. It
// does nothing for a driver built by [NewFrom] and for one never started.
// It also waits for any concurrent Start to finish before closing its client.
//
// Example:
//
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Stop(context.Context) error {
	if !driver.owned {
		return nil
	}

	driver.lifecycle <- struct{}{}
	defer func() {
		<-driver.lifecycle
	}()

	client := driver.client.Swap(nil)

	if client == nil {
		return nil
	}

	if err := client.Close(); err != nil {
		return fmt.Errorf("launchdarkly: stop: %w", err)
	}

	return nil
}

// Ping reports whether the SDK can serve evaluations. It fails with an error
// wrapping [feature.ErrNotReady] unless the driver is running and the client
// reports itself initialized, so a driver not started, already stopped, or
// wrapping a client that never received flags is not ready. It does no I/O.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("launchdarkly: ping: %w", err)
	}

	client := driver.client.Load()

	if client == nil {
		return fmt.Errorf("launchdarkly: ping: driver not started: %w", feature.ErrNotReady)
	}

	if !client.Initialized() {
		return fmt.Errorf("launchdarkly: ping: client not initialized: %w", feature.ErrNotReady)
	}

	return nil
}

// Evaluate implements [feature.Driver]. It converts the subject to a
// LaunchDarkly context, evaluates request.Key with the SDK's JSON detail
// method and a null default, and maps the result: see the package
// documentation for the reasons and errors. A canceled ctx fails with an error
// wrapping its error before the SDK is called; the SDK evaluates locally and
// does not observe cancellation afterwards.
// Empty or unknown request.Kind returns ErrTypeMismatch after cancellation is
// checked and before readiness, lookup, or the disabled-flag shortcut.
//
// Evaluating the JSON form lets the driver tell a null variation (returned as
// no value) from a variation of the wrong type (an error), which the SDK's
// typed methods cannot. A KindInt request is answered with an int64 when the
// number is whole, and otherwise with the float64, which the facade rejects
// with [feature.ErrTypeMismatch].
//
// Example:
//
//	result, err := driver.Evaluate(ctx, feature.Request{Key: "checkout", Kind: feature.KindBool})
//	if err != nil {
//		return err
//	}
//	_ = result.Value // Nil for a disabled flag; the facade supplies the fallback.
func (driver *Driver) Evaluate(ctx context.Context, request feature.Request) (feature.Result, error) {
	if err := ctx.Err(); err != nil {
		return feature.Result{}, fmt.Errorf("launchdarkly: evaluate %q: %w", request.Key, err)
	}

	switch request.Kind {
	case feature.KindBool, feature.KindString, feature.KindInt, feature.KindFloat:
	default:
		return feature.Result{}, fmt.Errorf("launchdarkly: evaluate %q: unsupported kind %q: %w", request.Key, request.Kind, feature.ErrTypeMismatch)
	}

	client := driver.client.Load()

	if client == nil {
		return feature.Result{}, fmt.Errorf("launchdarkly: evaluate %q: driver not started: %w", request.Key, feature.ErrNotReady)
	}

	subject, err := newContext(request.Subject)

	if err != nil {
		return feature.Result{}, fmt.Errorf("launchdarkly: evaluate %q: %w: %w", request.Key, feature.ErrInvalidSubject, err)
	}

	value, detail, err := client.JSONVariationDetail(request.Key, subject, ldvalue.Null())
	reason := detail.Reason

	if reason.GetKind() == ldreason.EvalReasonError {
		return feature.Result{}, evaluationError(request.Key, reason.GetErrorKind(), err)
	}

	result := feature.Result{
		Reason: reasonOf(reason),
	}

	if detail.VariationIndex.IsDefined() {
		result.Variant = strconv.Itoa(detail.VariationIndex.IntValue())
	}

	if result.Reason == feature.ReasonDisabled || value.IsNull() {
		return result, nil
	}

	result.Value, err = variationValue(request.Kind, value)

	if err != nil {
		return feature.Result{}, fmt.Errorf("launchdarkly: evaluate %q: %w", request.Key, err)
	}

	return result, nil
}

// variationValue translates one non-null native variation without truncating
// numeric values. Fractional numbers requested as integers remain float64 so the
// facade can reject them with ErrTypeMismatch instead of silently rounding.
func variationValue(kind feature.Kind, value ldvalue.Value) (any, error) {
	switch kind {
	case feature.KindBool:
		if value.IsBool() {
			return value.BoolValue(), nil
		}
	case feature.KindString:
		if value.IsString() {
			return value.StringValue(), nil
		}
	case feature.KindFloat:
		if value.IsNumber() {
			return value.Float64Value(), nil
		}
	case feature.KindInt:
		if value.IsInt() {
			return int64(value.IntValue()), nil
		}

		if value.IsNumber() {
			return value.Float64Value(), nil
		}
	}

	return nil, fmt.Errorf("%s variation requested as %s: %w", value.Type(), kind, feature.ErrTypeMismatch)
}

// newContext converts subject to a LaunchDarkly context of the default kind
// "user". An empty key becomes an anonymous context with [AnonymousKey].
// Attributes are set first and the key last, so an attribute named "key" or
// "kind" cannot replace the subject's identity. It fails when the SDK rejects
// an attribute (such as a non-string "name") or the context is invalid.
func newContext(subject feature.Subject) (ldcontext.Context, error) {
	builder := ldcontext.NewBuilder(subject.Key)

	for name, attribute := range subject.Attributes {
		if !builder.TrySetValue(name, attributeValue(attribute)) {
			return ldcontext.Context{}, fmt.Errorf("attribute %q is not accepted by LaunchDarkly", name)
		}
	}

	builder.Kind(ldcontext.DefaultKind).Key(cmp.Or(subject.Key, AnonymousKey))

	if subject.Key == "" {
		builder.Anonymous(true)
	}

	evaluationContext := builder.Build()

	return evaluationContext, evaluationContext.Err()
}

// attributeValue converts an attribute validated by the facade to an ldvalue.
// Numbers become JSON numbers, a [time.Time] becomes Unix milliseconds, which
// LaunchDarkly's date operators accept, and []string becomes an array.
func attributeValue(attribute any) ldvalue.Value {
	switch v := attribute.(type) {
	case string:
		return ldvalue.String(v)
	case bool:
		return ldvalue.Bool(v)
	case int64:
		return ldvalue.Float64(float64(v))
	case float64:
		return ldvalue.Float64(v)
	case []string:
		items := make([]ldvalue.Value, len(v))

		for i, s := range v {
			items[i] = ldvalue.String(s)
		}

		return ldvalue.ArrayOf(items...)
	case time.Time:
		return ldvalue.Float64(float64(v.UnixMilli()))
	default:
		// The facade rejects other types; keep the driver safe if used directly.
		return ldvalue.Null()
	}
}
