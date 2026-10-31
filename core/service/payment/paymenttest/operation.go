package paymenttest

import (
	"context"

	"arkoren.dev/core/service/payment"
)

// CreateCheckout validates and consumes one queued creation reply. It never
// approves, completes, or stores a readable checkout implicitly.
//
// Example:
//
//	checkout, err := fake.CreateCheckout(ctx, request)
//	if err != nil { return err }
//	_ = checkout.ID
func (fake *Fake) CreateCheckout(ctx context.Context, request payment.CheckoutRequest) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	checkout, err := takeReply(&fake.create, request, fake.takeFailure())

	return cloneCheckout(checkout), err
}

// CreateSubscriptionCheckout consumes an existing-plan creation reply after
// validation. It neither creates catalog entries nor activates a subscription.
//
// Example:
//
//	checkout, err := fake.CreateSubscriptionCheckout(ctx, request)
//	if err != nil { return err }
//	_ = checkout.SubscriptionID
func (fake *Fake) CreateSubscriptionCheckout(
	ctx context.Context, request payment.SubscriptionCheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	checkout, err := takeReply(&fake.subscribe, request, fake.takeFailure())

	return cloneCheckout(checkout), err
}

// GetCheckout consumes a separately scripted read reply without invoking a
// mutation. Creation replies never seed this queue automatically.
//
// Example:
//
//	checkout, err := fake.GetCheckout(ctx, "cs_test")
//	if err != nil { return err }
//	_ = checkout.Status
func (fake *Fake) GetCheckout(ctx context.Context, id payment.CheckoutID) (payment.Checkout, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	checkout, err := takeReply(&fake.checkout, id, fake.takeFailure())

	return cloneCheckout(checkout), err
}

// CompleteCheckout consumes a scripted explicit-completion reply after request
// validation. Approval requirements are represented by the queued result/error.
//
// Example:
//
//	checkout, err := fake.CompleteCheckout(ctx, request)
//	if err != nil { return err }
//	_ = checkout.PaymentStatus
func (fake *Fake) CompleteCheckout(
	ctx context.Context, request payment.CompleteCheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	checkout, err := takeReply(&fake.complete, request, fake.takeFailure())

	return cloneCheckout(checkout), err
}

// GetPayment consumes a scripted payment observation. It never infers payment
// settlement from a preceding completion or subscription response.
//
// Example:
//
//	result, err := fake.GetPayment(ctx, "pi_test")
//	if err != nil { return err }
//	_ = result.Status
func (fake *Fake) GetPayment(ctx context.Context, id payment.ID) (payment.Payment, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Payment{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	return takeReply(&fake.payment, id, fake.takeFailure())
}

// RefundPayment consumes a scripted explicit-refund reply. Only request syntax
// is validated; provider balances and currency agreement belong in simulator
// tests or in the script, not a second fake ledger.
//
// Example:
//
//	refund, err := fake.RefundPayment(ctx, request)
//	if err != nil { return err }
//	_ = refund.ID
func (fake *Fake) RefundPayment(ctx context.Context, request payment.RefundRequest) (payment.Refund, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	return takeReply(&fake.refund, request, fake.takeFailure())
}

// GetRefund consumes a refund observation independently of submission scripts.
// It never resubmits funds or changes a previous scripted payment result.
//
// Example:
//
//	refund, err := fake.GetRefund(ctx, "re_test")
//	if err != nil { return err }
//	_ = refund.Status
func (fake *Fake) GetRefund(ctx context.Context, id payment.RefundID) (payment.Refund, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	return takeReply(&fake.returned, id, fake.takeFailure())
}

// GetSubscription consumes a detached lifecycle observation. Active status is
// not treated as evidence of paid invoices or successful money movement.
//
// Example:
//
//	subscription, err := fake.GetSubscription(ctx, "sub_test")
//	if err != nil { return err }
//	_ = subscription.Status
func (fake *Fake) GetSubscription(
	ctx context.Context, id payment.SubscriptionID,
) (payment.Subscription, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Subscription{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	subscription, err := takeReply(&fake.subscription, id, fake.takeFailure())

	return cloneSubscription(subscription), err
}

// CancelSubscription consumes a scripted immediate-cancellation outcome. It
// neither creates refunds nor schedules future changes.
//
// Example:
//
//	if err := fake.CancelSubscription(ctx, request); err != nil { return err }
func (fake *Fake) CancelSubscription(ctx context.Context, request payment.CancelSubscriptionRequest) error {
	if err := request.Validate(ctx); err != nil {
		return err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	_, err := takeReply(&fake.cancel, request, fake.takeFailure())

	return err
}

// PreparePayment validates before recording or consuming scripts/failures, then
// returns one independent client-initialization reply. No automatic payment
// ledger, hosted checkout, completion, or read response is created.
//
// Example:
//
//	fake.ReplyPreparePayment(payment.Preparation{Action: payment.NewClientOrder("paypal.order", "ORDER-1")}, nil)
//	prepared, err := fake.PreparePayment(ctx, payment.NewPreparePaymentRequest(amount, "order-1"))
//	if err != nil { return err }
//	_ = prepared.Action.Kind()
func (fake *Fake) PreparePayment(
	ctx context.Context, request payment.PreparePaymentRequest,
) (payment.Preparation, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Preparation{}, err
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	return takeReply(&fake.prepare, request, fake.takeFailure())
}
