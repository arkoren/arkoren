package memorypayment

import (
	"context"
	"slices"
	"time"

	"arkoren.dev/core/service/payment"
)

// ApproveCheckout simulates customer approval without settlement or activation.
// Payment approval requires explicit completion; subscription approval completes
// enrollment but leaves lifecycle pending. Repeated approval, including already
// completed checkouts, is harmless. Expired or canceled checkouts reject approval
// with payment.ErrInvalidState; missing IDs return payment.ErrNotFound.
//
// Example:
//
//	if err := driver.ApproveCheckout(ctx, checkout.ID); err != nil { return err }
func (driver *Driver) ApproveCheckout(ctx context.Context, id payment.CheckoutID) error {
	if err := id.Validate(ctx); err != nil {
		return err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	checkout, found := driver.checkouts[id]

	if !found {
		return payment.ErrNotFound
	}

	if checkout.Status == payment.CheckoutStatusComplete {
		return nil
	}

	if checkout.Status != payment.CheckoutStatusOpen && checkout.Status != payment.CheckoutStatusApproved {
		return payment.ErrInvalidState
	}

	checkout.Status, checkout.NativeStatus = payment.CheckoutStatusApproved, "approved"
	checkout.NeedsCompletion = checkout.Kind == payment.CheckoutKindPayment

	if checkout.Kind == payment.CheckoutKindSubscription {
		checkout.Status, checkout.NativeStatus = payment.CheckoutStatusComplete, "complete"
	}

	driver.checkouts[id] = checkout

	return nil
}

// ResolveCheckout simulates expiration or cancellation before completion.
// Only open or approved checkouts can change; repeating the same outcome is
// harmless. It never cancels a subscription or changes a payment implicitly.
//
// Example:
//
//	if err := driver.ResolveCheckout(ctx, id, payment.CheckoutStatusExpired); err != nil { return err }
func (driver *Driver) ResolveCheckout(
	ctx context.Context, id payment.CheckoutID, status payment.CheckoutStatus,
) error {
	if err := id.Validate(ctx); err != nil {
		return err
	}

	if status != payment.CheckoutStatusExpired && status != payment.CheckoutStatusCanceled {
		return payment.ErrInvalidRequest
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	checkout, found := driver.checkouts[id]

	if !found {
		return payment.ErrNotFound
	}

	if !slices.Contains([]payment.CheckoutStatus{payment.CheckoutStatusOpen,
		payment.CheckoutStatusApproved, status}, checkout.Status) {
		return payment.ErrInvalidState
	}

	checkout.Status, checkout.NativeStatus = status, string(status)
	checkout.NeedsCompletion = false
	driver.checkouts[id] = checkout

	return nil
}

// ResolvePayment settles a pending payment as succeeded, failed, or canceled.
// Terminal outcomes cannot be replaced, though replaying the same outcome is
// harmless. Related checkout observations are updated without changing approval.
//
// Example:
//
//	if err := driver.ResolvePayment(ctx, id, payment.StatusSucceeded); err != nil { return err }
func (driver *Driver) ResolvePayment(ctx context.Context, id payment.ID, status payment.Status) error {
	if err := id.Validate(ctx); err != nil {
		return err
	}

	if !slices.Contains([]payment.Status{payment.StatusSucceeded, payment.StatusFailed,
		payment.StatusCanceled}, status) {
		return payment.ErrInvalidRequest
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	result, found := driver.payments[id]

	if !found {
		return payment.ErrNotFound
	}

	if result.Status != payment.StatusPending && result.Status != status {
		return payment.ErrInvalidState
	}

	result.Status, result.NativeStatus = status, string(status)
	driver.payments[id] = result
	driver.observePayment(id, status)

	return nil
}

// observePayment updates checkout snapshots under mutex. Idempotency responses
// remain unchanged, preserving the original mutation result on retries.
func (driver *Driver) observePayment(id payment.ID, status payment.Status) {
	for key, checkout := range driver.checkouts {
		if checkout.PaymentID == id {
			checkout.PaymentStatus = status
			driver.checkouts[key] = checkout
		}
	}
}

// ResolveSubscription explicitly supplies a lifecycle outcome and optional next
// billing instant, which is copied. No invoice or payment is created. Canceled
// subscriptions cannot reactivate; use a new checkout for a new subscription.
// Nil nextBillingAt means unknown, not an unlimited paid-through period.
//
// Example:
//
//	err := driver.ResolveSubscription(ctx, id, payment.SubscriptionStatusActive, nil)
//	if err != nil { return err }
func (driver *Driver) ResolveSubscription(
	ctx context.Context, id payment.SubscriptionID, status payment.SubscriptionStatus, nextBillingAt *time.Time,
) error {
	if err := id.Validate(ctx); err != nil {
		return err
	}

	if !slices.Contains([]payment.SubscriptionStatus{payment.SubscriptionStatusPending,
		payment.SubscriptionStatusTrialing, payment.SubscriptionStatusActive,
		payment.SubscriptionStatusPastDue, payment.SubscriptionStatusUnpaid,
		payment.SubscriptionStatusPaused, payment.SubscriptionStatusSuspended,
		payment.SubscriptionStatusCanceled, payment.SubscriptionStatusExpired}, status) {
		return payment.ErrInvalidRequest
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	subscription, found := driver.subscriptions[id]

	if !found {
		return payment.ErrNotFound
	}

	if subscription.Status == payment.SubscriptionStatusCanceled && status != subscription.Status {
		return payment.ErrInvalidState
	}

	subscription.Status, subscription.NativeStatus = status, string(status)
	subscription.NextBillingAt = nextBillingAt

	if status == payment.SubscriptionStatusCanceled {
		subscription.NextBillingAt = nil
	}

	driver.subscriptions[id] = cloneSubscription(subscription)

	return nil
}
