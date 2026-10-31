package molliepayment

import (
	"context"
	"errors"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/mollie/molliepayment/internal/resource"
)

// RefundPayment requests an explicit positive partial or full amount. Mollie
// validates currency and remaining refundable balance; no hidden preflight or
// follow-up read occurs. Pending, queued and processing are normal accepted
// results, not unknown outcomes. Preserve the returned compound RefundID for
// GetRefund; a bare re_ identifier does not contain its required parent.
//
// Example:
//
//	refund, err := backend.RefundPayment(ctx, payment.NewRefundRequest("tr_example",
//		payment.Money{Amount: 100, Currency: "EUR"}, "refund-123"))
//	if err != nil {
//		return err
//	}
//	_ = refund.ID // Persist this opaque value, not just Mollie's re_ token.
func (driver *Driver) RefundPayment(ctx context.Context, request payment.RefundRequest) (payment.Refund, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	if err := resource.NativeID(string(request.PaymentID()), "tr_"); err != nil {
		return payment.Refund{}, err
	}

	decimal, err := request.Amount().Decimal()

	if err != nil {
		return payment.Refund{}, err
	}

	var observed resource.Refund
	action := operation{path: "/payments/" + string(request.PaymentID()) + "/refunds",
		key: request.IdempotencyKey(), body: map[string]any{
			"amount": resource.Amount{Currency: request.Amount().Currency, Value: decimal},
		}}

	if err := driver.call(ctx, action, &observed); err != nil {
		return payment.Refund{}, err
	}

	if observed.PaymentID != string(request.PaymentID()) {
		return payment.Refund{}, mutationError(errors.New("mollie response refund parent mismatch"))
	}

	refund, err := observed.Snapshot()

	return refund, mutationError(err)
}

// GetRefund reads one refund using the opaque ID returned by RefundPayment.
// Its bounded compound representation carries the native parent and refund IDs
// without process-local state or a database. A parent or identity mismatch in
// the response fails closed; API links supplied by Mollie are never followed.
// Malformed successful JSON and unusable snapshots wrap ErrUnavailable without
// adding ErrOutcomeUnknown; transport and HTTP errors retain their categories.
//
// Example:
//
//	refund, err := backend.GetRefund(ctx, savedRefundID)
//	if err != nil {
//		return err
//	}
//	_ = refund.Status
func (driver *Driver) GetRefund(ctx context.Context, id payment.RefundID) (payment.Refund, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	parent, native, err := resource.RefundParts(string(id))

	if err != nil {
		return payment.Refund{}, err
	}

	var observed resource.Refund

	if err := driver.call(ctx, operation{path: "/payments/" + parent + "/refunds/" + native}, &observed); err != nil {
		return payment.Refund{}, err
	}

	if observed.PaymentID != parent || observed.ID != native {
		return payment.Refund{}, readError(errors.New("mollie response refund identity mismatch"))
	}

	result, err := observed.Snapshot()

	return result, readError(err)
}
