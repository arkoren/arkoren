package paypalpayment

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/paypal/internal/paypal"
)

// CreateCheckout creates one CAPTURE order with one exact-amount purchase unit.
// Blank CancelURL uses ReturnURL. This call never captures funds or hydrates a
// successful response; malformed successful results are outcome-unknown.
//
// Example:
//
//	checkout, err := backend.CreateCheckout(ctx, payment.NewCheckoutRequest(
//		payment.Money{Amount: 1250, Currency: "USD"}, "https://shop.example/paid", "order-123"))
//	if err != nil {
//		return err
//	}
//	_ = checkout.ApprovalURL
func (driver *Driver) CreateCheckout(ctx context.Context, request payment.CheckoutRequest) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	unit, err := purchaseUnit(request.Amount(), request.Reference(), request.Description())

	if err != nil {
		return payment.Checkout{}, err
	}

	// body uses PayPal's current payment_source experience context, not the
	// deprecated order-level application_context.
	body := map[string]any{
		"intent": "CAPTURE", "purchase_units": []any{unit},
		"payment_source": map[string]any{"paypal": map[string]any{"experience_context": map[string]string{
			"return_url": request.ReturnURL(), "cancel_url": cmp.Or(request.CancelURL(), request.ReturnURL()),
			"user_action": "PAY_NOW",
		}}},
	}
	var resource paypal.Order

	if err := driver.client.Call(ctx, paypal.Request{Method: http.MethodPost,
		Path: "/v2/checkout/orders", Key: request.IdempotencyKey(), Body: body}, &resource); err != nil {
		return payment.Checkout{}, err
	}

	checkout, err := resource.Checkout()

	return checkout, paypal.MutationError(err)
}

// purchaseUnit builds the single exact-amount unit shared by hosted and native
// one-off orders. Callers validate portable input first; this step additionally
// enforces PayPal's currency scales without rounding. Optional application
// reference and description are copied verbatim, independently of request keys.
func purchaseUnit(money payment.Money, reference, description string) (map[string]any, error) {
	decimal, err := paypal.Decimal(money)

	if err != nil {
		return nil, err
	}

	unit := map[string]any{"amount": map[string]string{"currency_code": money.Currency, "value": decimal}}

	if reference != "" {
		unit["custom_id"] = reference
	}

	if description != "" {
		unit["description"] = description
	}

	return unit, nil
}

// CreateSubscriptionCheckout starts approval of an existing native plan. It
// returns subscription:<I-ID> and the native SubscriptionID before approval;
// active lifecycle never implies paid. Blank CancelURL uses ReturnURL.
//
// Example:
//
//	checkout, err := backend.CreateSubscriptionCheckout(ctx, payment.NewSubscriptionCheckoutRequest(
//		"P-MONTHLY", "https://shop.example/account", "signup-123"))
//	if err != nil {
//		return err
//	}
//	_ = checkout.SubscriptionID
func (driver *Driver) CreateSubscriptionCheckout(
	ctx context.Context, request payment.SubscriptionCheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	// body starts approval only; there is no hidden activation call.
	body := map[string]any{"plan_id": request.PlanReference(),
		"application_context": map[string]string{"user_action": "SUBSCRIBE_NOW", "return_url": request.ReturnURL(),
			"cancel_url": cmp.Or(request.CancelURL(), request.ReturnURL())}}

	if request.Reference() != "" {
		body["custom_id"] = request.Reference()
	}

	var resource paypal.Subscription

	if err := driver.client.Call(ctx, paypal.Request{Method: http.MethodPost,
		Path: "/v1/billing/subscriptions", Key: request.IdempotencyKey(), Body: body}, &resource); err != nil {
		return payment.Checkout{}, err
	}

	checkout, err := resource.Checkout()

	return checkout, paypal.MutationError(err)
}

// GetCheckout observes an order or subscription without capture, activation,
// polling or inferred settlement. Other ID families fail before authentication.
// Malformed successful JSON wraps ErrUnavailable without adding ErrOutcomeUnknown;
// semantic snapshot validation retains its specific validation/capability errors.
//
// Example:
//
//	checkout, err := backend.GetCheckout(ctx, "order:ABC")
//	if err != nil {
//		return err
//	}
//	_ = checkout.Status
func (driver *Driver) GetCheckout(ctx context.Context, id payment.CheckoutID) (payment.Checkout, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	family, native, _ := strings.Cut(string(id), ":")

	switch family {
	case "order":
		if err := paypal.ValidNative(native); err != nil {
			return payment.Checkout{}, err
		}

		var resource paypal.Order

		if err := driver.client.Call(ctx, paypal.Request{Method: http.MethodGet,
			Path: "/v2/checkout/orders/" + native}, &resource); err != nil {
			return payment.Checkout{}, err
		}

		return resource.Checkout()
	case "subscription":
		if err := paypal.SubscriptionNative(native); err != nil {
			return payment.Checkout{}, err
		}

		resource, err := driver.getSubscription(ctx, native)

		if err != nil {
			return payment.Checkout{}, err
		}

		return resource.Checkout()
	default:
		return payment.Checkout{}, fmt.Errorf("%w: checkout family", payment.ErrInvalidRequest)
	}
}

// CompleteCheckout first observes approval. Approved orders are captured with
// the caller's key; completed orders are returned without a second charge.
// Subscriptions are read-only approval observations, with no activate/poll loop.
// An instrument decline returns ErrDeclined; the application owns a fresh
// checkout attempt. Only ORDER_ALREADY_CAPTURED permits one conflict read.
//
// Example:
//
//	checkout, err := backend.CompleteCheckout(ctx, payment.NewCompleteCheckoutRequest("order:ABC", "capture-123"))
//	if err != nil {
//		return err
//	}
//	_ = checkout.PaymentStatus
func (driver *Driver) CompleteCheckout(
	ctx context.Context, request payment.CompleteCheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	checkout, err := driver.GetCheckout(ctx, request.CheckoutID())

	if err != nil {
		return payment.Checkout{}, err
	}

	if checkout.Status == payment.CheckoutStatusComplete {
		return checkout, nil
	}

	if checkout.Status != payment.CheckoutStatusApproved {
		return payment.Checkout{}, fmt.Errorf("%w: checkout is not approved", payment.ErrInvalidState)
	}

	if checkout.Kind == payment.CheckoutKindSubscription {
		return checkout, nil
	}

	return driver.capture(ctx, request)
}

// capture dispatches exactly one approved order capture. It does not follow a
// successful mutation with GET. A documented conflict allows one explicit read.
func (driver *Driver) capture(ctx context.Context, request payment.CompleteCheckoutRequest) (payment.Checkout, error) {
	var resource paypal.Order
	err := driver.client.Call(ctx, paypal.Request{Method: http.MethodPost,
		Path: "/v2/checkout/orders/" + strings.TrimPrefix(string(request.CheckoutID()), "order:") + "/capture",
		Key:  request.IdempotencyKey(), Body: map[string]any{}}, &resource)

	if paypal.Issue(err, "ORDER_ALREADY_CAPTURED") {
		return driver.capturedConflict(ctx, request.CheckoutID(), err)
	}

	if err != nil {
		return payment.Checkout{}, err
	}

	checkout, err := resource.Checkout()

	return checkout, paypal.MutationError(err)
}

// capturedConflict accepts only a now-completed order after the explicit
// already-captured conflict. Unsuccessful reconciliation preserves the conflict.
func (driver *Driver) capturedConflict(
	ctx context.Context, id payment.CheckoutID, conflict error,
) (payment.Checkout, error) {
	checkout, err := driver.GetCheckout(ctx, id)

	if err != nil {
		return payment.Checkout{}, errors.Join(conflict, err)
	}

	if checkout.Status != payment.CheckoutStatusComplete {
		return payment.Checkout{}, conflict
	}

	return checkout, nil
}
