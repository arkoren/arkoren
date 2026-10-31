package crypto

import (
	"context"
	"errors"
	"time"

	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// instruments holds only aggregate metrics, never traces or sensitive values.
type instruments struct {
	// count counts completed facade calls, including encoding/decoding failures.
	count telemetry.Counter

	// duration measures the complete operation, including all fallback attempts.
	duration telemetry.Histogram
}

// newInstruments declares counts and second-based durations for service.
// Ciphertext, plaintext, keys and native error text or types are never labels;
// only bounded operation, outcome and error classes are accepted.
func newInstruments(service *telemetry.Telemetry) *instruments {
	meter := service.Meter("arkoren.dev/core/service/crypto")
	keys := telemetry.Keys(semconv.CryptoOperation, semconv.CryptoOutcome, semconv.ErrorType)

	return &instruments{
		count: meter.Counter(semconv.CryptoOperations, telemetry.Unit("{operation}"), keys,
			telemetry.Description("Number of completed encryption facade operations.")),
		duration: meter.Histogram(semconv.CryptoOperationDuration, telemetry.Unit("s"), keys,
			telemetry.Description("Duration of complete encryption facade operations."),
			telemetry.Buckets(0.000001, 0.00001, 0.0001, 0.001, 0.01, 0.1, 1)),
	}
}

// operation retains only metric handles, a bounded operation name and start
// time. A nil operation skips reporting and timing when telemetry is disabled.
type operation struct {
	// instruments records the completed logical operation.
	instruments *instruments

	// name is encrypt or decrypt, never a caller-supplied identifier.
	name string

	// startedAt precedes encoding and every key attempt for the complete call.
	startedAt time.Time
}

// begin resolves defaults per call before reading time or building instruments.
// Instrument resolution is safe for concurrent facade use.
func (encrypter *Encrypter) begin(name string) *operation {
	service := encrypter.telemetry

	if service == nil {
		service = telemetry.Default()
	}

	if !service.Enabled() {
		return nil
	}

	return &operation{
		instruments: encrypter.instruments.For(service),
		name:        name,
		startedAt:   time.Now(),
	}
}

// decrypt reports successful fallback separately from current-key success.
// Fallback success does not claim that the caller persisted a rotated value;
// a subsequent typed decoding error overrides it with an error outcome.
func (operation *operation) decrypt(previous bool, err error) {
	outcome := "ok"

	if previous {
		outcome = "previous_key"
	}

	operation.end(outcome, err)
}

// end records the final logical result once, never individual key attempts.
// Only fixed error classes are emitted, including for JSON codec failures.
// Call it only after normal completion, never from a defer: a driver or codec
// panic must propagate without producing a completed operation observation.
func (operation *operation) end(outcome string, err error) {
	if operation == nil {
		return
	}

	attrs := []telemetry.Attr{
		telemetry.String(semconv.CryptoOperation, operation.name),
		telemetry.String(semconv.CryptoOutcome, outcome),
	}

	if err != nil {
		attrs[1] = telemetry.String(semconv.CryptoOutcome, "error")
		attrs = append(attrs, telemetry.String(semconv.ErrorType, errorClass(err)))
	}

	ctx := context.Background()
	operation.instruments.count.Add(ctx, 1, attrs...)
	operation.instruments.duration.Record(ctx, time.Since(operation.startedAt).Seconds(), attrs...)
}

// errorClass maps only known contract sentinels, keeping native types and error
// messages out of aggregate metrics. Codec and unknown driver errors use error.
func errorClass(err error) string {
	if errors.Is(err, ErrEncrypterClosed) {
		return "crypto.ErrEncrypterClosed"
	}

	if errors.Is(err, ErrInvalidCiphertext) {
		return "crypto.ErrInvalidCiphertext"
	}

	return "error"
}
