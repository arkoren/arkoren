package stripepayment

import (
	"errors"
	"net/http"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/stripe/stripepayment/internal/protocol"
)

// failure retains diagnostic identity without exposing provider text, URLs,
// request bodies, or credentials through Error. Unwrap is for trusted callers.
type failure struct {
	// class is a safe contract error, possibly joined with outcome unknown.
	class error
	// cause preserves the SDK or transport error for errors.As and errors.Is.
	cause error
}

// Error returns only contract-owned text; native error messages may be sensitive.
//
// Example:
//
//	_, err := backend.GetPayment(ctx, "pi_missing")
//	if err != nil { _ = err.Error() }
func (failure failure) Error() string {
	return "stripe payment: " + failure.class.Error()
}

// GoString keeps fmt's %#v from traversing the retained native cause, which can
// contain provider response bodies and PaymentIntent client secrets.
//
// Example:
//
//	_, err := backend.PreparePayment(ctx, request)
//	if err != nil { _ = fmt.Sprintf("%#v", err) } // safe classification only
func (failure failure) GoString() string {
	return failure.Error()
}

// Unwrap preserves both safe classification and the original diagnostic cause.
// Use errors.Is and errors.As rather than displaying the native cause.
//
// Example:
//
//	_, err := backend.GetPayment(ctx, "pi_missing")
//	if errors.Is(err, payment.ErrNotFound) { return nil }
func (failure failure) Unwrap() []error {
	return []error{failure.class, failure.cause}
}

// readError maps known API responses and treats transport or decoding errors as
// unavailable. Its safe wrapper still exposes the original cause to errors.As.
func readError(err error) error {
	if err == nil {
		return nil
	}

	return failure{class: protocol.Classify(err), cause: err}
}

// mutationError distinguishes definitive client errors from uncertain writes.
// Once the SDK call begins, a transport error, 5xx, or decode failure cannot prove
// that Stripe did not apply the request. Never automatically replay these errors.
// idempotency_key_in_use is also uncertain: another request may already be
// executing the same logical mutation. Reads do not acquire this classification.
func mutationError(err error) error {
	if err == nil {
		return nil
	}

	var native *stripe.Error

	if errors.As(err, &native) {
		definitive := native.HTTPStatusCode >= http.StatusBadRequest &&
			native.HTTPStatusCode < http.StatusInternalServerError

		if definitive && native.Code != stripe.ErrorCodeIdempotencyKeyInUse {
			return readError(err)
		}
	}

	return failure{class: errors.Join(payment.ErrOutcomeUnknown, protocol.Classify(err)), cause: err}
}
