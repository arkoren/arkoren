package paypalpayment

import (
	"context"
	"errors"
	"net/http"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/paypal/internal/paypal"
)

// PreparePayment creates one plain CAPTURE order for the PayPal mobile SDK.
// Empty, iOS and Android channels use the same order profile; web is unsupported.
// ReturnURL must be empty: the SDK supplies return/cancel URLs through its own
// URL configuration. DeviceReference, when supplied, is visible ASCII risk data
// of at most 255 bytes and is sent only as PayPal-Client-Metadata-Id.
// No approval link is required, and no payment exists until explicit capture.
// Persist CheckoutID, obtain actual buyer approval, then use CompleteCheckout;
// an SDK callback alone never authorizes capture. Mutations are never retried.
// Malformed successful representations return ErrOutcomeUnknown for reconciliation.
//
// Example:
//
//	amount, err := payment.NewMoney("USD", 1200)
//	if err != nil {
//		return err
//	}
//	prepared, err := backend.PreparePayment(ctx, payment.NewPreparePaymentRequest(amount, "order-123"))
//	if err != nil {
//		return err
//	}
//	order, ok := prepared.Action.Order()
//	if ok {
//		_ = order.OrderID // Send to the PayPal SDK; persist prepared.CheckoutID server-side.
//	}
func (driver *Driver) PreparePayment(
	ctx context.Context, request payment.PreparePaymentRequest,
) (payment.Preparation, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Preparation{}, err
	}

	if request.Channel() == payment.ClientChannelWeb || request.ReturnURL() != "" {
		return payment.Preparation{}, payment.ErrUnsupported
	}

	unit, err := purchaseUnit(request.Amount(), request.Reference(), request.Description())

	if err != nil {
		return payment.Preparation{}, err
	}

	var resource paypal.Order
	err = driver.client.Call(ctx, paypal.Request{Method: http.MethodPost, Path: "/v2/checkout/orders",
		Key: request.IdempotencyKey(), DeviceReference: request.DeviceReference(),
		Body: map[string]any{"intent": "CAPTURE", "purchase_units": []any{unit}}}, &resource)

	if err != nil {
		return payment.Preparation{}, err
	}

	return preparedOrder(resource, request.Amount())
}

// preparedOrder validates the actual provider representation before publishing
// an SDK action. Partial representations may omit purchase units, but observed
// amounts must match the request and captures cannot be disguised as preparation.
// No request amount or reference is substituted for missing provider data.
func preparedOrder(resource paypal.Order, expected payment.Money) (payment.Preparation, error) {
	checkout, err := resource.Checkout()

	if err != nil {
		return payment.Preparation{}, paypal.MutationError(err)
	}

	ready := checkout.Status == payment.CheckoutStatusOpen || checkout.Status == payment.CheckoutStatusApproved

	if !ready || checkout.PaymentID != "" {
		return payment.Preparation{}, paypal.MutationError(errors.New("paypal preparation order state"))
	}

	if checkout.Amount != nil && *checkout.Amount != expected {
		return payment.Preparation{}, paypal.MutationError(errors.New("paypal preparation amount mismatch"))
	}

	return payment.Preparation{CheckoutID: checkout.ID,
		Action: payment.NewClientOrder(payment.ClientProtocolPayPalOrder, resource.ID)}, nil
}
