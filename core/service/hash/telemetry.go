package hash

import (
	"context"
	"errors"
	"time"

	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// instruments holds only aggregate metrics, never traces or input values.
type instruments struct {
	// count counts completed facade calls, including encoding failures.
	count telemetry.Counter

	// duration measures complete facade calls in seconds.
	duration telemetry.Histogram
}

// newInstruments declares counts and second-based durations for service.
// No input, hash, driver error text or native error type becomes a label;
// operation, outcome and error labels use the facade's bounded vocabulary.
func newInstruments(service *telemetry.Telemetry) *instruments {
	meter := service.Meter("arkoren.dev/core/service/hash")
	keys := telemetry.Keys(semconv.HashOperation, semconv.HashOutcome, semconv.ErrorType)

	return &instruments{
		count: meter.Counter(semconv.HashOperations, telemetry.Unit("{operation}"), keys,
			telemetry.Description("Number of completed hashing facade operations.")),
		duration: meter.Histogram(semconv.HashOperationDuration, telemetry.Unit("s"), keys,
			telemetry.Description("Duration of complete hashing facade operations."),
			telemetry.Buckets(0.0001, 0.001, 0.01, 0.05, 0.1, 0.5, 1, 5)),
	}
}

// operation retains only the metric handles, bounded operation name and start
// time. A nil operation skips reporting, including timing, when disabled.
type operation struct {
	// instruments records the completed call.
	instruments *instruments

	// name identifies hash, check or needs_rehash, never a caller value.
	name string

	// startedAt precedes encoding and driver work for the complete facade call.
	startedAt time.Time
}

// begin resolves dynamic defaults on every call before creating instruments or
// reading time. Instrument resolution is safe for concurrent facade use.
func (hasher *Hasher) begin(name string) *operation {
	service := hasher.telemetry

	if service == nil {
		service = telemetry.Default()
	}

	if !service.Enabled() {
		return nil
	}

	return &operation{
		instruments: hasher.instruments.For(service),
		name:        name,
		startedAt:   time.Now(),
	}
}

// check reports match or mismatch only when err is nil. end converts every
// non-nil error into an error outcome, so a failed check never counts as a
// password mismatch. A nil operation remains an instrumentation no-op.
func (operation *operation) check(match bool, err error) {
	outcome := "mismatch"

	if match {
		outcome = "match"
	}

	operation.end(outcome, err)
}

// rehash reports whether the driver recommends a parameter update. It records
// a policy observation, never a completed password check or persisted update;
// a nil operation skips reporting.
func (operation *operation) rehash(needed bool) {
	outcome := "current"

	if needed {
		outcome = "rehash_needed"
	}

	operation.end(outcome, nil)
}

// end records one final outcome. Errors use a closed vocabulary rather than
// arbitrary native types or text; successful mismatches have no error label.
// Call it only after normal completion, never from a defer: a driver or codec
// panic has no completed result and must not be counted as success or mismatch.
func (operation *operation) end(outcome string, err error) {
	if operation == nil {
		return
	}

	class := ""

	if err != nil {
		outcome, class = "error", "error"
	}

	if errors.Is(err, ErrInvalidHash) {
		class = "hash.ErrInvalidHash"
	}

	attrs := []telemetry.Attr{
		telemetry.String(semconv.HashOperation, operation.name),
		telemetry.String(semconv.HashOutcome, outcome),
		telemetry.String(semconv.ErrorType, class),
	}
	ctx := context.Background()
	operation.instruments.count.Add(ctx, 1, attrs...)
	operation.instruments.duration.Record(ctx, time.Since(operation.startedAt).Seconds(), attrs...)
}
