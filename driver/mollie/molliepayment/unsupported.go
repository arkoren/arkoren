package molliepayment

import (
	"context"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/mollie/molliepayment/internal/resource"
)

// CompleteCheckout returns ErrUnsupported after context, portable request and
// native ID validation. Mollie automatically captures this driver's payments;
// this operation never dispatches HTTP or changes the checkout.
//
// Example:
//
//	_, err := backend.CompleteCheckout(ctx, payment.NewCompleteCheckoutRequest("tr_example", "complete-123"))
//	if !errors.Is(err, payment.ErrUnsupported) {
//		return err
//	}
func (driver *Driver) CompleteCheckout(ctx context.Context, request payment.CompleteCheckoutRequest) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	if err := resource.NativeID(string(request.CheckoutID()), "tr_"); err != nil {
		return payment.Checkout{}, err
	}

	return payment.Checkout{}, payment.ErrUnsupported
}

// PreparePayment validates context and portable input before returning
// ErrUnsupported without HTTP. This driver supports hosted one-off checkout,
// not a native SDK session or submission of client-collected payment tokens.
// Invalid requests and canceled contexts retain their normal validation errors.
//
// Example:
//
//	amount, err := payment.NewMoney("EUR", 1200)
//	if err != nil {
//		return err
//	}
//	_, err = backend.PreparePayment(ctx, payment.NewPreparePaymentRequest(amount, "order-123"))
//	if !errors.Is(err, payment.ErrUnsupported) {
//		return err
//	}
func (driver *Driver) PreparePayment(
	ctx context.Context, request payment.PreparePaymentRequest,
) (payment.Preparation, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Preparation{}, err
	}

	return payment.Preparation{}, payment.ErrUnsupported
}

// CreateSubscriptionCheckout validates context and the portable request, then
// returns ErrUnsupported without HTTP. Mollie recurring setup needs customer
// and mandate relationships that the existing plan-only contract cannot supply.
//
// Example:
//
//	_, err := backend.CreateSubscriptionCheckout(ctx, payment.NewSubscriptionCheckoutRequest(
//		"plan-monthly", "https://shop.example/account", "signup-123"))
//	if !errors.Is(err, payment.ErrUnsupported) {
//		return err
//	}
func (driver *Driver) CreateSubscriptionCheckout(ctx context.Context, request payment.SubscriptionCheckoutRequest) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	return payment.Checkout{}, payment.ErrUnsupported
}

// GetSubscription validates context and the portable ID, then returns
// ErrUnsupported without HTTP. This driver never creates subscription IDs.
//
// Example:
//
//	_, err := backend.GetSubscription(ctx, payment.SubscriptionID("sub_example"))
//	if !errors.Is(err, payment.ErrUnsupported) {
//		return err
//	}
func (driver *Driver) GetSubscription(ctx context.Context, id payment.SubscriptionID) (payment.Subscription, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Subscription{}, err
	}

	return payment.Subscription{}, payment.ErrUnsupported
}

// CancelSubscription validates context and the portable request before
// returning ErrUnsupported. No mutation is dispatched and therefore no unknown
// money-moving outcome is implied by this unsupported operation.
//
// Example:
//
//	err := backend.CancelSubscription(ctx, payment.NewCancelSubscriptionRequest("sub_example",
//		func(details *payment.CancelSubscriptionRequestBuilder) {
//			details.Reason("Customer request")
//		}))
//	if !errors.Is(err, payment.ErrUnsupported) {
//		return err
//	}
func (driver *Driver) CancelSubscription(ctx context.Context, request payment.CancelSubscriptionRequest) error {
	if err := request.Validate(ctx); err != nil {
		return err
	}

	return payment.ErrUnsupported
}
