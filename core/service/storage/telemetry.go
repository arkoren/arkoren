package storage

import (
	"context"
	"errors"

	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// telemetryScope is the instrumentation scope of storage telemetry.
const telemetryScope = "arkoren.dev/core/service/storage"

const (
	// operationOpen measures acquiring a body, not subsequently consuming it.
	// Opened-body transfer and cleanup errors belong to the caller's operation.
	operationOpen = "open"

	// operationPut measures one complete reader submission to the driver.
	// Upload helpers reuse this operation rather than recording a second write.
	operationPut = "put"

	// operationStat identifies metadata lookup without opening object contents.
	// Missing objects are outcomes, not telemetry failures.
	operationStat = "stat"

	// operationDelete identifies idempotent logical-object deletion.
	// Successful deletion of an absent object is measured as a success too.
	operationDelete = "delete"

	// operationList measures one page request, not a traversal of all pages.
	// Prefixes and cursor values never enter its automatic attributes.
	operationList = "list"

	// operationSign measures URL creation, not the later authorized request.
	// Signed bearer URLs and their query strings are never recorded.
	operationSign = "sign"
)

// operationNames describes the storage instruments. Operations range from a
// local stat to a multi-gigabyte upload, so the buckets span one millisecond
// to a minute.
var operationNames = clientop.Names{
	Scope:    telemetryScope,
	Noun:     "storage operations",
	Count:    semconv.StorageOperations,
	Duration: semconv.StorageOperationDuration,
	Keys:     []string{semconv.StorageOperation, semconv.StorageStore},
	Buckets:  []float64{0.001, 0.005, 0.025, 0.1, 0.5, 1, 5, 15, 60},
}

// errorSentinels name the package errors reported as error.type.
var errorSentinels = []errtype.Sentinel{
	{Err: ErrInvalidKey, Name: "storage.ErrInvalidKey"},
	{Err: ErrUnsupported, Name: "storage.ErrUnsupported"},
	{Err: ErrInvalidTTL, Name: "storage.ErrInvalidTTL"},
	{Err: ErrInvalidSignOptions, Name: "storage.ErrInvalidSignOptions"},
}

// begin starts the span of an operation. It returns a nil operation, without
// allocating, when telemetry is disabled. The span never carries keys,
// prefixes, cursors or signed URLs.
func (storage *Storage) begin(ctx context.Context, operation string) (context.Context, *clientop.Op) {
	return clientop.Begin(ctx, storage.config.Telemetry, storage.instruments, telemetry.Client, "storage", operation,
		telemetry.String(semconv.StorageOperation, operation),
		telemetry.String(semconv.StorageStore, storage.name()))
}

// end finishes op with err. A missing object is an answer, not a failure, so
// [ErrNotFound] does not set the span status or error.type. Other errors are
// reported by class only, because their text can contain keys.
func end(ctx context.Context, op *clientop.Op, err error) {
	if errors.Is(err, ErrNotFound) {
		err = nil
	}

	op.End(ctx, err, errorSentinels...)
}
