package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/foundation/query"
)

// telemetryScope is the instrumentation scope of database telemetry.
const telemetryScope = "arkoren.dev/core/service/database"

// systemOther is the db.system.name for databases without a specific value.
const systemOther = "other_sql"

// lockOperation is the db.operation.name of [Database.Lock].
const lockOperation = "LOCK"

// tryLockOperation is the db.operation.name of [Database.TryLock]. It differs
// from [lockOperation] because a TryLock returns at once and never waits, so
// sharing the name would mix two very different durations in one histogram.
const tryLockOperation = "TRY_LOCK"

// durationBuckets are the bucket bounds, in seconds, that the database client
// semantic conventions recommend.
var durationBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

// systemByDialect maps a dialect name to its db.system.name value.
var systemByDialect = map[string]string{
	query.Postgres.String(): "postgresql",
	query.MySQL.String():    "mysql",
	query.SQLite.String():   "sqlite",
}

// knownOperations are the SQL verbs recorded as db.operation.name. Any other
// leading keyword is left out to keep cardinality bounded.
var knownOperations = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true,
}

// errorSentinels name the package errors reported as error.type.
var errorSentinels = []errtype.Sentinel{
	{Err: ErrEmptyLockName, Name: "database.ErrEmptyLockName"},
	{Err: ErrNestedTransaction, Name: "database.ErrNestedTransaction"},
	{Err: ErrSessionDone, Name: "database.ErrSessionDone"},
}

// instruments are the tracer and metrics of one [Telemetry].
type instruments struct {
	// tracer starts the client spans.
	tracer telemetry.Tracer

	// duration records db.client.operation.duration.
	duration telemetry.Histogram
}

// newInstruments builds the database instruments for t.
func newInstruments(t *telemetry.Telemetry) *instruments {
	return &instruments{
		tracer: t.Tracer(telemetryScope),
		duration: t.Meter(telemetryScope).Histogram(semconv.DBClientOperationDuration,
			telemetry.Unit("s"), telemetry.Description("Duration of database client operations."),
			telemetry.Keys(semconv.DBSystemName, semconv.DBNamespace, semconv.DBOperationName,
				semconv.DBCollectionName, semconv.ErrorType),
			telemetry.Buckets(durationBuckets...)),
	}
}

// operation is one in-flight traced database operation. A nil *operation is
// valid and does nothing, which is how disabled telemetry costs nothing.
type operation struct {
	// instruments record the result.
	instruments *instruments

	// span is the client span.
	span *telemetry.Span

	// startedAt retains the monotonic start time for elapsed-duration metrics.
	// It is not exported as a wall-clock timestamp in spans or events.
	startedAt time.Time

	// attrs are the metric attributes known at start.
	attrs []telemetry.Attr
}

// resolveSystem returns the db.system.name of database: the configured value,
// else the one matching the dialect its driver reports, else "other_sql".
func resolveSystem(database *Database) string {
	if database.config.System != "" {
		return database.config.System
	}

	if system, ok := systemByDialect[database.Dialect().String()]; ok {
		return system
	}

	return systemOther
}

// begin starts a traced operation for statement s compiled to sqlText. It
// returns a nil operation, without allocating, when telemetry is disabled.
func (database *Database) begin(ctx context.Context, s query.Statementer, sqlText string) (context.Context, *operation) {
	t := database.config.Telemetry

	if t == nil {
		t = telemetry.Default()
	}

	if !t.Enabled() {
		return ctx, nil
	}

	name, collection := describe(s, sqlText)

	return database.open(ctx, t, name, collection, sqlText)
}

// beginLock starts a traced lock acquisition named opName, either
// [lockOperation] or [tryLockOperation].
func (database *Database) beginLock(ctx context.Context, opName string) (context.Context, *operation) {
	t := database.config.Telemetry

	if t == nil {
		t = telemetry.Default()
	}

	if !t.Enabled() {
		return ctx, nil
	}

	return database.open(ctx, t, opName, "", "")
}

// open starts the client span for an operation on the enabled telemetry t.
func (database *Database) open(ctx context.Context, t *telemetry.Telemetry, name, collection, sqlText string) (context.Context, *operation) {
	attrs := make([]telemetry.Attr, 0, 5)
	attrs = append(attrs, telemetry.String(semconv.DBSystemName, database.system))
	attrs = appendNonEmpty(attrs, semconv.DBNamespace, database.config.Namespace)
	attrs = appendNonEmpty(attrs, semconv.DBOperationName, name)
	attrs = appendNonEmpty(attrs, semconv.DBCollectionName, collection)

	spanAttrs := attrs

	if database.config.QueryText && sqlText != "" {
		spanAttrs = append(spanAttrs[:len(spanAttrs):len(spanAttrs)], telemetry.String(semconv.DBQueryText, sqlText))
	}

	instruments := database.instruments.For(t)
	ctx, span := instruments.tracer.Start(ctx, spanName(database.system, name, collection),
		telemetry.Kind(telemetry.Client), telemetry.Attrs(spanAttrs...))

	return ctx, &operation{
		instruments: instruments,
		span:        span,
		startedAt:   time.Now(),
		attrs:       attrs,
	}
}

// end finishes the operation with err and records its duration. Not-found
// outcomes are successes. Other errors set the span status and error.type
// from the error's class only: the error text can contain values, so it is
// never recorded and RecordError is never called.
func (operation *operation) end(ctx context.Context, err error) {
	if operation == nil {
		return
	}

	if errors.Is(err, ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
		err = nil
	}

	if err != nil {
		name := errtype.Of(err, errorSentinels...)
		errorType := telemetry.String(semconv.ErrorType, name)
		operation.span.SetAttributes(errorType)
		operation.span.SetStatus(telemetry.Error, name)

		operation.attrs = append(operation.attrs, errorType)
	}

	operation.span.End()
	operation.instruments.duration.Record(ctx, time.Since(operation.startedAt).Seconds(), operation.attrs...)
}

// describe returns the db.operation.name and db.collection.name of s, which
// compiled to sqlText. The table is known only for a query.Query over a table.
// Any whitespace separates the leading verb; comments and CTEs are not parsed,
// and unknown leading tokens omit the bounded operation label.
func describe(s query.Statementer, sqlText string) (operation, collection string) {
	if statement, ok := s.(query.Query); ok {
		collection = statement.Inspect().Table
	}

	verb := ""

	for token := range strings.FieldsSeq(sqlText) {
		verb = strings.ToUpper(token)
		break
	}

	if !knownOperations[verb] {
		return "", collection
	}

	return verb, collection
}

// spanName builds the span name the database semantic conventions prescribe:
// "{operation} {collection}", else the operation, else the system.
func spanName(system, operation, collection string) string {
	switch {
	case operation != "" && collection != "":
		return operation + " " + collection
	case operation != "":
		return operation
	default:
		return system
	}
}

// appendNonEmpty appends the string attribute key=value unless value is empty.
func appendNonEmpty(attrs []telemetry.Attr, key, value string) []telemetry.Attr {
	if value == "" {
		return attrs
	}

	return append(attrs, telemetry.String(key, value))
}
