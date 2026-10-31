package memorypayment

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"sync"

	"arkoren.dev/core/service/payment"
)

// Driver is a concurrent in-memory simulator, not a payment processor.
// Construct it with New; its zero value is not ready for mutations. Reads return
// detached snapshots and never advance approval or settlement. Mutations sharing
// an operation and key replay their original response, even after later controls.
// Reusing that key with different input returns payment.ErrInvalidRequest.
//
// Example:
//
//	driver := memorypayment.New()
//	var backend payment.Driver = driver
//	_ = backend
type Driver struct {
	// mutex serializes records, balances, keys, and the identity sequence.
	mutex sync.Mutex
	// sequence supplies identities unique within this driver's lifetime.
	sequence uint64
	// checkouts stores approval snapshots independently of payments.
	checkouts map[payment.CheckoutID]payment.Checkout
	// payments stores explicitly created money movements.
	payments map[payment.ID]payment.Payment
	// refunds stores successful refund snapshots.
	refunds map[payment.RefundID]payment.Refund
	// subscriptions stores lifecycle snapshots without an invoice ledger.
	subscriptions map[payment.SubscriptionID]payment.Subscription
	// refunded tracks successful minor units against each payment.
	refunded map[payment.ID]int64
	// created retains one-time checkout request and response pairs.
	created map[string]replay[payment.CheckoutRequest, payment.Checkout]
	// subscribed retains subscription checkout request and response pairs.
	subscribed map[string]replay[payment.SubscriptionCheckoutRequest, payment.Checkout]
	// completed retains completion request and response pairs.
	completed map[string]replay[payment.CompleteCheckoutRequest, payment.Checkout]
	// returned retains refund request and response pairs.
	returned map[string]replay[payment.RefundRequest, payment.Refund]
	// prepared retains independent client-initialization request/response pairs.
	prepared map[string]replay[payment.PreparePaymentRequest, payment.Preparation]
}

// replay retains the accepted input and original output for one mutation key.
// Requests are compared by their valid wire payload, never private error identity
// or pointers. Outputs with mutable members are cloned before publication.
type replay[Request any, Result any] struct {
	// request detects accidental reuse for a different mutation.
	request Request
	// result is the original response, not the current resource state.
	result Result
}

// New creates an empty simulator without settings, I/O, or background work.
// Tests drive external outcomes through ApproveCheckout and ResolvePayment.
//
// Example:
//
//	driver := memorypayment.New()
//	_ = payment.New(driver)
func New() *Driver {
	driver := &Driver{}
	driver.Reset()

	return driver
}

// Reset forgets records, balances, and idempotency keys atomically. The identity
// sequence is retained so stale IDs cannot address resources created afterwards.
//
// Example:
//
//	driver := memorypayment.New()
//	driver.Reset()
func (driver *Driver) Reset() {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	driver.checkouts = make(map[payment.CheckoutID]payment.Checkout)
	driver.payments = make(map[payment.ID]payment.Payment)
	driver.refunds = make(map[payment.RefundID]payment.Refund)
	driver.subscriptions = make(map[payment.SubscriptionID]payment.Subscription)
	driver.refunded = make(map[payment.ID]int64)
	driver.created = make(map[string]replay[payment.CheckoutRequest, payment.Checkout])
	driver.subscribed = make(map[string]replay[payment.SubscriptionCheckoutRequest, payment.Checkout])
	driver.completed = make(map[string]replay[payment.CompleteCheckoutRequest, payment.Checkout])
	driver.returned = make(map[string]replay[payment.RefundRequest, payment.Refund])
	driver.prepared = make(map[string]replay[payment.PreparePaymentRequest, payment.Preparation])
}

// nextID allocates a family-specific opaque ID while the caller holds mutex.
// Reset never rewinds the sequence, keeping previously issued IDs stale.
func (driver *Driver) nextID(family string) string {
	driver.sequence++

	return fmt.Sprintf("memory_%s_%d", family, driver.sequence)
}

// recalled checks a key's input before replaying its response. Callers must
// serialize map access and clone mutable parts of a returned result.
func recalled[Request any, Result any](
	entries map[string]replay[Request, Result], key string, request Request,
) (Result, bool, error) {
	entry, found := entries[key]

	if !found {
		return entry.result, false, nil
	}

	original, err := json.Marshal(entry.request)

	if err != nil {
		return *new(Result), true, err
	}

	current, err := json.Marshal(request)

	if err != nil {
		return *new(Result), true, err
	}

	if string(original) != string(current) {
		return *new(Result), true, payment.ErrInvalidRequest
	}

	return entry.result, found, nil
}

// cloneCheckout detaches the optional amount so callers cannot change state or
// an original idempotency response by modifying their snapshot.
func cloneCheckout(checkout payment.Checkout) payment.Checkout {
	if checkout.Amount != nil {
		amount := *checkout.Amount
		checkout.Amount = &amount
	}

	return checkout
}

// cloneSubscription detaches the optional billing instant. Time advancement
// never changes subscription state automatically.
func cloneSubscription(subscription payment.Subscription) payment.Subscription {
	if subscription.NextBillingAt != nil {
		nextBillingAt := *subscription.NextBillingAt
		subscription.NextBillingAt = &nextBillingAt
	}

	return subscription
}

// GetCheckout validates the context and identity, then returns a detached
// snapshot. Missing IDs return payment.ErrNotFound without changing state.
//
// Example:
//
//	checkout, err := driver.GetCheckout(ctx, id)
//	if err != nil { return err }
//	_ = checkout.Status
func (driver *Driver) GetCheckout(ctx context.Context, id payment.CheckoutID) (payment.Checkout, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	checkout, found := driver.checkouts[id]

	if !found {
		return payment.Checkout{}, payment.ErrNotFound
	}

	return cloneCheckout(checkout), nil
}

// GetPayment observes settlement without advancing it. A valid missing identity
// returns payment.ErrNotFound, including checkout IDs converted to payment IDs.
//
// Example:
//
//	result, err := driver.GetPayment(ctx, id)
//	if err != nil { return err }
//	_ = result.Status
func (driver *Driver) GetPayment(ctx context.Context, id payment.ID) (payment.Payment, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Payment{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	result, found := driver.payments[id]

	if !found {
		return payment.Payment{}, payment.ErrNotFound
	}

	return result, nil
}

// GetRefund observes a refund without submitting another return of funds.
// Valid identities absent from this instance return payment.ErrNotFound.
//
// Example:
//
//	refund, err := driver.GetRefund(ctx, id)
//	if err != nil { return err }
//	_ = refund.Amount
func (driver *Driver) GetRefund(ctx context.Context, id payment.RefundID) (payment.Refund, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	refund, found := driver.refunds[id]

	if !found {
		return payment.Refund{}, payment.ErrNotFound
	}

	return refund, nil
}

// GetSubscription returns a detached lifecycle snapshot without activation or
// invoice settlement. Valid missing identities return payment.ErrNotFound.
//
// Example:
//
//	subscription, err := driver.GetSubscription(ctx, id)
//	if err != nil { return err }
//	_ = subscription.NextBillingAt
func (driver *Driver) GetSubscription(
	ctx context.Context, id payment.SubscriptionID,
) (payment.Subscription, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Subscription{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	subscription, found := driver.subscriptions[id]

	if !found {
		return payment.Subscription{}, payment.ErrNotFound
	}

	return cloneSubscription(subscription), nil
}
