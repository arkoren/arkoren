package search

import (
	"context"
	"time"

	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// telemetryScope is the instrumentation scope of search telemetry.
const telemetryScope = "arkoren.dev/core/service/search"

const (
	// operationSync labels an administrative synchronization, including no-ops.
	// It is bounded metadata, never an index name or caller-provided value.
	operationSync = "sync"

	// operationCheck labels a read-only schema compatibility check. It remains
	// separate from Sync because no backend settings are changed by a check.
	operationCheck = "check"

	// operationPut labels one logical facade batch, regardless of the number
	// of documents or provider requests needed to store it.
	operationPut = "put"

	// operationDelete labels one logical document-deletion batch. IDs are not
	// included in the operation name, metrics, or automatic span attributes.
	operationDelete = "delete"

	// operationDrop labels explicit index removal, including an absent index.
	// It is distinct from synchronization, which never prunes registrations.
	operationDrop = "drop"

	// operationSearch labels one paged search. Query text and document contents
	// stay outside automatic telemetry; only the hit count is observed.
	operationSearch = "search"
)

// durationBuckets are the bucket bounds, in seconds, suited to search
// latency, which spans fast reads and slow reindexing syncs.
var durationBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 30}

// errorSentinels name the package errors reported as error.type.
var errorSentinels = []errtype.Sentinel{
	{Err: ErrNotFound, Name: "search.ErrNotFound"},
	{Err: ErrInvalidName, Name: "search.ErrInvalidName"},
	{Err: ErrInvalidQuery, Name: "search.ErrInvalidQuery"},
	{Err: ErrInvalidID, Name: "search.ErrInvalidID"},
	{Err: ErrSchemaChanged, Name: "search.ErrSchemaChanged"},
	{Err: ErrUnsupported, Name: "search.ErrUnsupported"},
}

// instruments are the tracer and metrics of one [telemetry.Telemetry].
type instruments struct {
	// tracer starts the client spans.
	tracer telemetry.Tracer

	// operations counts operations.
	operations telemetry.Counter

	// duration records arkoren.search.operation.duration.
	duration telemetry.Histogram
}

// newInstruments builds the search instruments for t.
func newInstruments(t *telemetry.Telemetry) *instruments {
	meter := t.Meter(telemetryScope)
	keys := telemetry.Keys(semconv.SearchOperation, semconv.SearchName, semconv.SearchIndex, semconv.ErrorType)

	return &instruments{
		tracer: t.Tracer(telemetryScope),
		operations: meter.Counter(semconv.SearchOperations,
			telemetry.Unit("{operation}"), telemetry.Description("Number of search operations."), keys),
		duration: meter.Histogram(semconv.SearchOperationDuration,
			telemetry.Unit("s"), telemetry.Description("Duration of search operations."), keys,
			telemetry.Buckets(durationBuckets...)),
	}
}

// operation is one in-flight traced search operation. A nil *operation is
// valid and does nothing, which is how disabled telemetry costs nothing.
type operation struct {
	// instruments record the result.
	instruments *instruments

	// span is the client span.
	span *telemetry.Span

	// startedAt is when the operation began, including its monotonic clock.
	startedAt time.Time

	// attrs are the operation, engine name, and index attributes shared by
	// the span and the metrics.
	attrs []telemetry.Attr
}

// begin starts a traced operation on index, which is empty for operations on
// every index. It returns a nil operation, without allocating, when
// telemetry is disabled. The span never carries query text or documents.
func (engine *Engine) begin(ctx context.Context, name, index string) (context.Context, *operation) {
	t := engine.config.Telemetry

	if t == nil {
		t = telemetry.Default()
	}

	if !t.Enabled() {
		return ctx, nil
	}

	engineName := engine.config.Name

	if engineName == "" {
		engineName = DefaultName
	}

	attrs := []telemetry.Attr{
		telemetry.String(semconv.SearchOperation, name),
		telemetry.String(semconv.SearchName, engineName),
		telemetry.String(semconv.SearchIndex, index),
	}

	instruments := engine.instruments.For(t)
	ctx, span := instruments.tracer.Start(ctx, "search "+name, telemetry.Kind(telemetry.Client),
		telemetry.Attrs(attrs...))

	return ctx, &operation{
		instruments: instruments,
		span:        span,
		startedAt:   time.Now(),
		attrs:       attrs,
	}
}

// setHits records the number of hits a search returned on the span.
func (operation *operation) setHits(hits int) {
	if operation == nil {
		return
	}

	operation.span.SetAttributes(telemetry.Int(semconv.SearchHits, hits))
}

// end finishes the operation with err and records the metrics. A failure
// sets the span status and error.type from the error's class only: the error
// text can contain field and index names from callers, so it is never
// recorded and RecordError is never called.
func (operation *operation) end(ctx context.Context, err error) {
	if operation == nil {
		return
	}

	attrs := operation.attrs

	if err != nil {
		name := errtype.Of(err, errorSentinels...)
		errorType := telemetry.String(semconv.ErrorType, name)
		operation.span.SetAttributes(errorType)
		operation.span.SetStatus(telemetry.Error, name)

		attrs = append(attrs[:len(attrs):len(attrs)], errorType)
	}

	operation.span.End()
	operation.instruments.operations.Add(ctx, 1, attrs...)
	operation.instruments.duration.Record(ctx, time.Since(operation.startedAt).Seconds(), attrs...)
}
