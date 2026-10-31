package secret

import (
	"context"
	"errors"

	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry"
)

// operationNames defines low-cardinality instruments shared by store instances.
// Duration bounds cover local lookups through remote provider requests.
var operationNames = clientop.Names{
	Scope:    "arkoren.dev/core/service/secret",
	Noun:     "secret operations",
	Count:    semconv.SecretOperations,
	Duration: semconv.SecretOperationDuration,
	Keys:     []string{semconv.SecretOperation},
	Buckets:  []float64{0.0001, 0.001, 0.005, 0.025, 0.1, 0.5, 1, 5, 15},
}

// failure is a sanitized error class, never an underlying provider error.
// Its comparable value lets clientop report a bounded error.type verbatim.
type failure string

// Error returns only the fixed class selected by failureClass. clientop sees
// this sanitized error rather than a provider error whose text might be secret.
func (failure failure) Error() string {
	return string(failure)
}

// failureClass maps provider errors to a closed set without exposing native
// types or messages. Context errors take precedence over the contract sentinel.
func failureClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "context.Canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context.DeadlineExceeded"
	case errors.Is(err, ErrNotFound):
		return "secret.ErrNotFound"
	default:
		return "other"
	}
}

// begin starts a provider-operation span, resolving the dynamic default only
// for this call. Disabled instrumentation returns ctx unchanged and a nil op.
func (store *Store) begin(ctx context.Context, operation string) (context.Context, *clientop.Op) {
	return clientop.Begin(ctx, store.config.telemetry, store.instruments, telemetry.Client, "secret", operation,
		telemetry.String(semconv.SecretOperation, operation))
}

// end records a provider result using a sanitized class. It does not inspect
// errors when telemetry is disabled, and never changes the caller's error.
func end(ctx context.Context, op *clientop.Op, err error) {
	if op == nil {
		return
	}

	if err == nil {
		op.End(ctx, nil)

		return
	}

	class := failure(failureClass(err))
	op.End(ctx, class, errtype.Sentinel{Err: class, Name: string(class)})
}
