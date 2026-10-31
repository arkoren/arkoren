package paypalpayment

import (
	"context"
	"net/http"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/paypal/internal/paypal"
)

// RefundPayment sends the explicit requested amount and caller key to the v2
// capture refund endpoint. PayPal enforces original currency and remaining
// balance atomically; there is no hidden lookup or implicit remaining refund.
// Successful responses are not hydrated. Sale families fail before any I/O.
//
// Example:
//
//	refund, err := backend.RefundPayment(ctx, payment.NewRefundRequest("capture:ABC",
//		payment.Money{Amount: 500, Currency: "USD"}, "refund-123"))
//	if err != nil {
//		return err
//	}
//	_ = refund.Status
func (driver *Driver) RefundPayment(ctx context.Context, request payment.RefundRequest) (payment.Refund, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	native, err := captureID(request.PaymentID())

	if err != nil {
		return payment.Refund{}, err
	}

	decimal, err := paypal.Decimal(request.Amount())

	if err != nil {
		return payment.Refund{}, err
	}

	// body always includes the explicit amount, never an implicit balance refund.
	body := map[string]any{"amount": map[string]string{
		"currency_code": request.Amount().Currency, "value": decimal,
	}}
	var resource paypal.Movement
	err = driver.client.Call(ctx, paypal.Request{Method: http.MethodPost,
		Path: "/v2/payments/captures/" + native + "/refund", Key: request.IdempotencyKey(),
		Body: body}, &resource)

	if err != nil {
		return payment.Refund{}, err
	}

	refund, err := resource.Refund()

	if err != nil {
		return payment.Refund{}, paypal.MutationError(err)
	}

	// The mutation target is an observed relationship, not a hydration guess.
	refund.PaymentID = request.PaymentID()

	return refund, nil
}

// GetRefund reads the v2 refund's own progress. A missing trusted up link leaves
// PaymentID empty rather than following arbitrary response URLs.
// Malformed successful JSON wraps ErrUnavailable without adding ErrOutcomeUnknown;
// semantic snapshot validation retains its specific validation/capability errors.
//
// Example:
//
//	refund, err := backend.GetRefund(ctx, "refund:ABC")
//	if err != nil {
//		return err
//	}
//	_ = refund.Status
func (driver *Driver) GetRefund(ctx context.Context, id payment.RefundID) (payment.Refund, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	native, err := paypal.Native(string(id), "refund:")

	if err != nil {
		return payment.Refund{}, err
	}

	var resource paypal.Movement

	if err := driver.client.Call(ctx, paypal.Request{Method: http.MethodGet,
		Path: "/v2/payments/refunds/" + native}, &resource); err != nil {
		return payment.Refund{}, err
	}

	return resource.Refund()
}
