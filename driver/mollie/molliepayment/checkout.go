package molliepayment

import (
	"cmp"
	"context"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/mollie/molliepayment/internal/resource"
)

// CreateCheckout starts hosted card approval with automatic capture and an
// explicit oneoff sequence. An empty Description becomes "Payment" because
// Mollie requires it. Reference is stored in metadata. The caller's key is sent
// unchanged; Mollie's replay window is one hour and scoped to the API credential.
// No mutation is retried. Ambiguous dispatched requests wrap ErrOutcomeUnknown;
// use the same credential, key and request for application-controlled recovery.
//
// Example:
//
//	checkout, err := backend.CreateCheckout(ctx, payment.NewCheckoutRequest(
//		payment.Money{Amount: 1200, Currency: "EUR"}, "https://shop.example/paid", "order-123"))
//	if err != nil {
//		return err
//	}
//	_ = checkout.ApprovalURL // Redirect the customer; this is not proof of payment.
func (driver *Driver) CreateCheckout(ctx context.Context, request payment.CheckoutRequest) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	decimal, err := request.Amount().Decimal()

	if err != nil {
		return payment.Checkout{}, err
	}

	body := map[string]any{
		"amount":      resource.Amount{Currency: request.Amount().Currency, Value: decimal},
		"description": cmp.Or(request.Description(), "Payment"), "redirectUrl": request.ReturnURL(),
		"method": "creditcard", "sequenceType": "oneoff", "captureMode": "automatic",
		"metadata": map[string]string{"reference": request.Reference()},
	}

	if request.CancelURL() != "" {
		body["cancelUrl"] = request.CancelURL()
	}

	if driver.webhookURL != "" {
		body["webhookUrl"] = driver.webhookURL
	}

	var observed resource.Payment

	if err := driver.call(ctx, operation{path: "/payments", key: request.IdempotencyKey(), body: body}, &observed); err != nil {
		return payment.Checkout{}, err
	}

	checkout, err := observed.Checkout()

	return checkout, mutationError(err)
}

// GetCheckout observes the payment underlying a hosted checkout. Checkout IDs
// have the tr_ family, identical to payment IDs. This read never captures or
// completes a payment; NeedsCompletion is always false for this driver.
// Malformed successful JSON and unusable snapshots wrap ErrUnavailable without
// adding ErrOutcomeUnknown; transport and HTTP errors retain their categories.
//
// Example:
//
//	checkout, err := backend.GetCheckout(ctx, savedCheckoutID)
//	if err != nil {
//		return err
//	}
//	_ = checkout.PaymentStatus
func (driver *Driver) GetCheckout(ctx context.Context, id payment.CheckoutID) (payment.Checkout, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	observed, err := driver.getPayment(ctx, string(id))

	if err != nil {
		return payment.Checkout{}, err
	}

	result, err := observed.Checkout()

	return result, readError(err)
}
