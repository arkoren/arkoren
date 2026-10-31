package paypalpayment

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/paypal/internal/paypal"
)

// getSubscription observes native lifecycle without activating a subscription.
// Callers validate the I- identity before constructing this route.
func (driver *Driver) getSubscription(ctx context.Context, id string) (paypal.Subscription, error) {
	var resource paypal.Subscription
	err := driver.client.Call(ctx, paypal.Request{Method: http.MethodGet,
		Path: "/v1/billing/subscriptions/" + id}, &resource)

	return resource, err
}

// GetSubscription observes native I- lifecycle and optional next billing time.
// Neither ACTIVE nor that scheduling field proves payment or paid-through time.
// Malformed successful JSON wraps ErrUnavailable without adding ErrOutcomeUnknown;
// semantic snapshot validation retains its specific validation/capability errors.
//
// Example:
//
//	subscription, err := backend.GetSubscription(ctx, "I-ABC")
//	if err != nil {
//		return err
//	}
//	_ = subscription.Status
func (driver *Driver) GetSubscription(ctx context.Context, id payment.SubscriptionID) (payment.Subscription, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Subscription{}, err
	}

	if err := paypal.SubscriptionNative(string(id)); err != nil {
		return payment.Subscription{}, err
	}

	resource, err := driver.getSubscription(ctx, string(id))

	if err != nil {
		return payment.Subscription{}, err
	}

	return resource.Snapshot()
}

// CancelSubscription observes lifecycle, then cancels ACTIVE or SUSPENDED
// subscriptions immediately. Already-canceled subscriptions succeed without a
// mutation. Pending/expired/unknown states fail explicitly. The cancel POST has
// no unsupported idempotency header and no post-204 GET, refund or proration.
// Only SUBSCRIPTION_STATUS_INVALID permits one explicit reconciliation read.
//
// Example:
//
//	err := backend.CancelSubscription(ctx, payment.NewCancelSubscriptionRequest("I-ABC",
//		func(details *payment.CancelSubscriptionRequestBuilder) {
//			details.Reason("Customer request")
//		}))
//	if err != nil {
//		return err
//	}
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

	cancelable := subscription.Status == payment.SubscriptionStatusActive ||
		subscription.Status == payment.SubscriptionStatusSuspended

	if !cancelable {
		return fmt.Errorf("%w: subscription is not cancelable", payment.ErrInvalidState)
	}

	err = driver.client.Call(ctx, paypal.Request{Method: http.MethodPost,
		Path: "/v1/billing/subscriptions/" + string(request.SubscriptionID()) + "/cancel",
		Body: map[string]string{"reason": cmp.Or(request.Reason(), "Customer request")}}, nil)

	if paypal.Issue(err, "SUBSCRIPTION_STATUS_INVALID") {
		return driver.canceledConflict(ctx, request.SubscriptionID(), err)
	}

	return err
}

// canceledConflict tolerates only a now-canceled subscription after the named
// state conflict. A read failure retains both causes; nothing is retried.
func (driver *Driver) canceledConflict(ctx context.Context, id payment.SubscriptionID, conflict error) error {
	subscription, err := driver.GetSubscription(ctx, id)

	if err != nil {
		return errors.Join(conflict, err)
	}

	if subscription.Status != payment.SubscriptionStatusCanceled {
		return conflict
	}

	return nil
}
