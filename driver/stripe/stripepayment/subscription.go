package stripepayment

import (
	"context"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/stripe/stripepayment/internal/protocol"
)

// GetSubscription reads a sub_ lifecycle snapshot and available item prices.
// Only a single-item subscription reports PlanReference. NextBillingAt stays nil:
// Stripe item period boundaries are not authoritative next collection times.
//
// Example:
//
//	subscription, err := backend.GetSubscription(ctx, payment.SubscriptionID("sub_example"))
//	if err != nil { return err }
//	_ = subscription.Status
func (driver *Driver) GetSubscription(
	ctx context.Context, id payment.SubscriptionID,
) (payment.Subscription, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Subscription{}, err
	}

	if err := protocol.CheckFamily(string(id), "sub_"); err != nil {
		return payment.Subscription{}, err
	}

	params := &stripe.SubscriptionRetrieveParams{}
	params.AddExpand("items.data.price")

	subscription, err := driver.client.V1Subscriptions.Retrieve(ctx, string(id), params)

	if err != nil {
		return payment.Subscription{}, readError(err)
	}

	result, err := protocol.Subscription(subscription)

	return result, readError(err)
}

// CancelSubscription reads current lifecycle and returns nil if already canceled.
// Otherwise it cancels immediately with invoice_now=false and prorate=false.
// Optional Reason becomes a cancellation comment. No post-mutation read occurs.
//
// Example:
//
//	err := backend.CancelSubscription(ctx, payment.NewCancelSubscriptionRequest("sub_example",
//		func(details *payment.CancelSubscriptionRequestBuilder) {
//			details.Reason("Customer request")
//		}))
//	if err != nil { return err }
func (driver *Driver) CancelSubscription(ctx context.Context, request payment.CancelSubscriptionRequest) error {
	if err := request.Validate(ctx); err != nil {
		return err
	}

	subscription, err := driver.GetSubscription(ctx, request.SubscriptionID())

	if err != nil {
		return err
	}

	if subscription.Status == payment.SubscriptionStatusCanceled {
		return nil
	}

	if err := payment.CheckContext(ctx); err != nil {
		return err
	}

	params := &stripe.SubscriptionCancelParams{
		InvoiceNow: stripe.Bool(false),
		Prorate:    stripe.Bool(false),
	}

	if request.Reason() != "" {
		params.CancellationDetails = &stripe.SubscriptionCancelCancellationDetailsParams{
			Comment: stripe.String(request.Reason()),
		}
	}

	result, err := driver.client.V1Subscriptions.Cancel(ctx, string(request.SubscriptionID()), params)

	if err != nil {
		return mutationError(err)
	}

	_, err = protocol.Subscription(result)

	return mutationError(err)
}
