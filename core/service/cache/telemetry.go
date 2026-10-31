package cache

import (
	"context"
	"errors"
	"strconv"
	"time"

	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// telemetryScope is the instrumentation scope of cache telemetry.
const telemetryScope = "arkoren.dev/core/service/cache"

// Operation names reported as arkoren.cache.operation.
const (
	operationGet = "get"
	operationPut = "put"
	// operationCompareAndPut identifies an atomic guarded write; mismatches
	// are successful observations, not storage failures or read misses.
	operationCompareAndPut    = "compare_and_put"
	operationDelete           = "delete"
	operationAdd              = "add"
	operationHas              = "has"
	operationPull             = "pull"
	operationIncrement        = "increment"
	operationDecrement        = "decrement"
	operationIncrementWithTTL = "increment_with_ttl"
	operationTTL              = "ttl"
	operationLock             = "lock"
	operationTryLock          = "try_lock"
	operationRemember         = "remember"
)

// durationBuckets are the bucket bounds, in seconds, suited to cache latency.
var durationBuckets = []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1}

// errorSentinels name the package errors reported as error.type.
var errorSentinels = []errtype.Sentinel{
	{Err: ErrInvalidKey, Name: "cache.ErrInvalidKey"},
	{Err: ErrUnsupported, Name: "cache.ErrUnsupported"},
	{Err: ErrInvalidTTL, Name: "cache.ErrInvalidTTL"},
	{Err: ErrEmptyLockName, Name: "cache.ErrEmptyLockName"},
	{Err: ErrLockLost, Name: "cache.ErrLockLost"},
}

// hitState records whether a read found its key.
type hitState uint8

const (
	// hitUnknown is the state of operations that are not reads.
	hitUnknown hitState = iota

	// hitYes means the read found its key.
	hitYes

	// hitNo means the key was missing or expired.
	hitNo
)

// readHit returns the hit state of a read that failed with err: a not-found
// error is a miss, any other error leaves it unknown.
func readHit(err error) hitState {
	switch {
	case err == nil:
		return hitYes
	case isNotFound(err):
		return hitNo
	default:
		return hitUnknown
	}
}

// boolHit returns the hit state for a read that reports found.
func boolHit(found bool) hitState {
	if found {
		return hitYes
	}

	return hitNo
}

// label returns the metric label of the state: "true", "false", or "".
func (state hitState) label() string {
	switch state {
	case hitYes:
		return strconv.FormatBool(true)
	case hitNo:
		return strconv.FormatBool(false)
	default:
		return ""
	}
}

// instruments are the tracer and metrics of one [telemetry.Telemetry].
type instruments struct {
	// tracer starts the client spans.
	tracer telemetry.Tracer

	// operations counts operations.
	operations telemetry.Counter

	// duration records arkoren.cache.operation.duration.
	duration telemetry.Histogram
}

// newInstruments builds the cache instruments for t.
func newInstruments(t *telemetry.Telemetry) *instruments {
	meter := t.Meter(telemetryScope)

	return &instruments{
		tracer: t.Tracer(telemetryScope),
		operations: meter.Counter(semconv.CacheOperations,
			telemetry.Unit("{operation}"), telemetry.Description("Number of cache operations."),
			telemetry.Keys(semconv.CacheOperation, semconv.CacheStore, semconv.CacheHit, semconv.ErrorType)),
		duration: meter.Histogram(semconv.CacheOperationDuration,
			telemetry.Unit("s"), telemetry.Description("Duration of cache operations."),
			telemetry.Keys(semconv.CacheOperation, semconv.CacheStore, semconv.ErrorType),
			telemetry.Buckets(durationBuckets...)),
	}
}

// operation is one in-flight traced cache operation. A nil *operation is
// valid and does nothing, which is how disabled telemetry costs nothing.
type operation struct {
	// instruments record the result.
	instruments *instruments

	// span is the client span.
	span *telemetry.Span

	// start is when the operation began.
	start time.Time

	// name is the operation name.
	name string

	// store is the store name.
	store string
}

// begin starts a traced operation. It returns a nil operation, without
// allocating, when telemetry is disabled. The span never carries keys or
// values.
func (cache *Cache) begin(ctx context.Context, name string) (context.Context, *operation) {
	t := cache.config.Telemetry

	if t == nil {
		t = telemetry.Default()
	}

	if !t.Enabled() {
		return ctx, nil
	}

	store := cache.config.Name

	if store == "" {
		store = DefaultName
	}

	instruments := cache.instruments.For(t)
	ctx, span := instruments.tracer.Start(ctx, "cache "+name, telemetry.Kind(telemetry.Client),
		telemetry.Attrs(telemetry.String(semconv.CacheOperation, name), telemetry.String(semconv.CacheStore, store)))

	return ctx, &operation{
		instruments: instruments,
		span:        span,
		start:       time.Now(),
		name:        name,
		store:       store,
	}
}

// end finishes the operation with err and its hit state and records the
// metrics. A missing key is a miss, not an error. Other errors set the span
// status and error.type from the error's class only: the error text can
// contain keys, so it is never recorded and RecordError is never called.
func (operation *operation) end(ctx context.Context, err error, hit hitState) {
	if operation == nil {
		return
	}

	if isNotFound(err) {
		err = nil
	}

	durationAttrs := []telemetry.Attr{
		telemetry.String(semconv.CacheOperation, operation.name),
		telemetry.String(semconv.CacheStore, operation.store),
	}
	countAttrs := append(durationAttrs[:2:2], telemetry.String(semconv.CacheHit, hit.label()))

	if hit != hitUnknown {
		operation.span.SetAttributes(telemetry.Bool(semconv.CacheHit, hit == hitYes))
	}

	if err != nil {
		name := errtype.Of(err, errorSentinels...)
		errorType := telemetry.String(semconv.ErrorType, name)
		operation.span.SetAttributes(errorType)
		operation.span.SetStatus(telemetry.Error, name)

		durationAttrs = append(durationAttrs, errorType)
		countAttrs = append(countAttrs, errorType)
	}

	operation.span.End()
	operation.instruments.operations.Add(ctx, 1, countAttrs...)
	operation.instruments.duration.Record(ctx, time.Since(operation.start).Seconds(), durationAttrs...)
}

// isNotFound reports whether err means the key was missing.
func isNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
