package stripepayment

import (
	"cmp"
	"context"
	"strings"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/stripe/stripepayment/internal/protocol"
)

// CreateCheckout creates one hosted Checkout Session with a quantity-one inline
// price. Amounts retain the core currency's minor units, with lowercase API codes.
// An empty description displays "Payment". No charge is confirmed by this call.
//
// Example:
//
//	amount, err := payment.NewMoney("USD", 2500)
//	if err != nil { return err }
//	checkout, err := backend.CreateCheckout(ctx,
//		payment.NewCheckoutRequest(amount, "https://shop.example/paid", "order-123"))
//	if err != nil { return err }
//	_ = checkout.ApprovalURL
func (driver *Driver) CreateCheckout(ctx context.Context, request payment.CheckoutRequest) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	params := checkoutParams(request.ReturnURL(), request.CancelURL(), request.Reference(), request.IdempotencyKey())
	params.Mode = stripe.String("payment")
	params.PaymentIntentData = &stripe.CheckoutSessionCreatePaymentIntentDataParams{
		Description: stripe.String(request.Description()),
		Metadata:    map[string]string{"reference": request.Reference()},
	}
	params.LineItems = []*stripe.CheckoutSessionCreateLineItemParams{{
		Quantity: stripe.Int64(1),
		PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
			Currency:   stripe.String(strings.ToLower(request.Amount().Currency)),
			UnitAmount: stripe.Int64(request.Amount().Amount),
			ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
				Name: stripe.String(cmp.Or(request.Description(), "Payment")),
			},
		},
	}}

	session, err := driver.client.V1CheckoutSessions.Create(ctx, params)

	if err != nil {
		return payment.Checkout{}, mutationError(err)
	}

	result, err := protocol.Checkout(session)

	return result, mutationError(err)
}

// CreateSubscriptionCheckout creates a hosted session for an existing recurring
// price_ ID, quantity one. Stripe owns catalog validation and customer approval;
// the driver neither stores subscription state nor infers invoice payment.
//
// Example:
//
//	request := payment.NewSubscriptionCheckoutRequest("price_monthly", "https://shop.example/account", "signup-123")
//	checkout, err := backend.CreateSubscriptionCheckout(ctx, request)
//	if err != nil { return err }
//	_ = checkout.ApprovalURL
func (driver *Driver) CreateSubscriptionCheckout(
	ctx context.Context, request payment.SubscriptionCheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	if err := protocol.CheckFamily(request.PlanReference(), "price_"); err != nil {
		return payment.Checkout{}, err
	}

	params := checkoutParams(request.ReturnURL(), request.CancelURL(), request.Reference(), request.IdempotencyKey())
	params.Mode = stripe.String("subscription")
	params.SubscriptionData = &stripe.CheckoutSessionCreateSubscriptionDataParams{
		Metadata: map[string]string{"reference": request.Reference()},
	}
	params.LineItems = []*stripe.CheckoutSessionCreateLineItemParams{{
		Price: stripe.String(request.PlanReference()), Quantity: stripe.Int64(1),
	}}

	session, err := driver.client.V1CheckoutSessions.Create(ctx, params)

	if err != nil {
		return payment.Checkout{}, mutationError(err)
	}

	result, err := protocol.Checkout(session)

	return result, mutationError(err)
}

// checkoutParams applies the common hosted-session redirect, correlation, and
// idempotency policy. Both checkout modes must use the same request-key scope.
func checkoutParams(returnURL, cancelURL, reference, key string) *stripe.CheckoutSessionCreateParams {
	params := &stripe.CheckoutSessionCreateParams{
		SuccessURL:        stripe.String(returnURL),
		ClientReferenceID: stripe.String(reference),
		Metadata:          map[string]string{"reference": reference},
		UIMode:            stripe.String("hosted_page"),
	}

	if cancelURL != "" {
		params.CancelURL = stripe.String(cancelURL)
	}

	params.SetIdempotencyKey(key)
	params.AddExpand("payment_intent")
	params.AddExpand("subscription")

	return params
}

// GetCheckout reads a cs_ session with its available payment and subscription
// relationships expanded. It never captures, activates, or completes anything.
// A known zero-total paid session reports StatusNotRequired; absent totals and
// absent relationships remain unknown rather than inferred from local state.
//
// Example:
//
//	checkout, err := backend.GetCheckout(ctx, payment.CheckoutID("cs_example"))
//	if err != nil { return err }
//	_ = checkout.PaymentStatus
func (driver *Driver) GetCheckout(ctx context.Context, id payment.CheckoutID) (payment.Checkout, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	if err := protocol.CheckFamily(string(id), "cs_"); err != nil {
		return payment.Checkout{}, err
	}

	params := &stripe.CheckoutSessionRetrieveParams{}
	params.AddExpand("payment_intent")
	params.AddExpand("subscription")

	session, err := driver.client.V1CheckoutSessions.Retrieve(ctx, string(id), params)

	if err != nil {
		return payment.Checkout{}, readError(err)
	}

	result, err := protocol.Checkout(session)

	return result, readError(err)
}

// CompleteCheckout reads the hosted checkout and returns ErrInvalidState unless
// it is complete. Stripe performs payment collection in hosted Checkout: this
// method never charges again and never equates completion with payment success.
// The completion key is validated but no mutation or new idempotency scope exists.
//
// Example:
//
//	checkout, err := backend.CompleteCheckout(ctx, payment.NewCompleteCheckoutRequest("cs_example", "complete-123"))
//	if err != nil { return err }
//	_ = checkout.PaymentStatus
func (driver *Driver) CompleteCheckout(
	ctx context.Context, request payment.CompleteCheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	result, err := driver.GetCheckout(ctx, request.CheckoutID())

	if err != nil {
		return payment.Checkout{}, err
	}

	if result.Status != payment.CheckoutStatusComplete {
		return payment.Checkout{}, payment.ErrInvalidState
	}

	return result, nil
}
