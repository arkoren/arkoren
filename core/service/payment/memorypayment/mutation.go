package memorypayment

import (
	"context"

	"arkoren.dev/core/service/payment"
)

// CreateCheckout records an open approval flow with a non-routable approval URL.
// It neither approves nor creates a payment; completion becomes necessary only
// after approval. Same-input key replays return the original detached response;
// changed-input key reuse is invalid.
//
// Example:
//
//	request := payment.NewCheckoutRequest(payment.Money{Amount: 1000, Currency: "USD"},
//		"https://shop.example/paid", "order-1")
//	checkout, err := driver.CreateCheckout(ctx, request)
//	if err != nil { return err }
//	_ = checkout.ApprovalURL
func (driver *Driver) CreateCheckout(
	ctx context.Context, request payment.CheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	prior, found, err := recalled(driver.created, request.IdempotencyKey(), request)

	if found {
		return cloneCheckout(prior), err
	}

	amount := request.Amount()
	checkout := payment.Checkout{
		ID: payment.CheckoutID(driver.nextID("checkout")), Kind: payment.CheckoutKindPayment,
		Status: payment.CheckoutStatusOpen, NativeStatus: "open",
		Amount: &amount, Reference: request.Reference(),
	}
	checkout.ApprovalURL = "https://test.invalid/approve/" + string(checkout.ID)
	driver.checkouts[checkout.ID] = cloneCheckout(checkout)
	driver.created[request.IdempotencyKey()] = replay[payment.CheckoutRequest, payment.Checkout]{request, checkout}

	return cloneCheckout(checkout), nil
}

// CreateSubscriptionCheckout records an open approval and pending subscription
// for the supplied plan reference. It does not infer a price, create a payment,
// activate the subscription, or schedule future renewals.
//
// Example:
//
//	request := payment.NewSubscriptionCheckoutRequest("plan-monthly", "https://shop.example/account", "signup-1")
//	checkout, err := driver.CreateSubscriptionCheckout(ctx, request)
//	if err != nil { return err }
//	_ = checkout.SubscriptionID
func (driver *Driver) CreateSubscriptionCheckout(
	ctx context.Context, request payment.SubscriptionCheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	prior, found, err := recalled(driver.subscribed, request.IdempotencyKey(), request)

	if found {
		return cloneCheckout(prior), err
	}

	subscription := payment.Subscription{
		ID: payment.SubscriptionID(driver.nextID("subscription")), PlanReference: request.PlanReference(),
		Status: payment.SubscriptionStatusPending, NativeStatus: "pending", Reference: request.Reference(),
	}
	checkout := payment.Checkout{
		ID: payment.CheckoutID(driver.nextID("checkout")), Kind: payment.CheckoutKindSubscription,
		Status: payment.CheckoutStatusOpen, NativeStatus: "open", Reference: request.Reference(),
		SubscriptionID: subscription.ID,
	}
	checkout.ApprovalURL = "https://test.invalid/approve/" + string(checkout.ID)
	driver.checkouts[checkout.ID] = checkout
	driver.subscriptions[subscription.ID] = subscription
	driver.subscribed[request.IdempotencyKey()] = replay[payment.SubscriptionCheckoutRequest, payment.Checkout]{
		request, checkout,
	}

	return checkout, nil
}

// CompleteCheckout creates one pending payment for an approved payment checkout
// that needs completion. Settlement requires ResolvePayment; GET never settles
// it. Already completed checkouts, including approved subscription enrollments,
// are observed without creating money movements or changing lifecycle. New keys
// retain that observation; existing keys replay their original response.
//
// Example:
//
//	checkout, err := driver.CompleteCheckout(ctx, payment.NewCompleteCheckoutRequest(id, "capture-1"))
//	if err != nil { return err }
//	_ = checkout.PaymentID
func (driver *Driver) CompleteCheckout(
	ctx context.Context, request payment.CompleteCheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	prior, found, err := recalled(driver.completed, request.IdempotencyKey(), request)

	if found {
		return cloneCheckout(prior), err
	}

	checkout, err := driver.completable(request.CheckoutID())

	if err != nil {
		return payment.Checkout{}, err
	}

	if checkout.NeedsCompletion {
		result := payment.Payment{
			ID: payment.ID(driver.nextID("payment")), Amount: *checkout.Amount,
			Status: payment.StatusPending, NativeStatus: "pending",
		}
		checkout.Status, checkout.NativeStatus = payment.CheckoutStatusComplete, "complete"
		checkout.NeedsCompletion = false
		checkout.PaymentID, checkout.PaymentStatus = result.ID, result.Status
		driver.payments[result.ID] = result
		driver.checkouts[checkout.ID] = checkout
	}

	driver.completed[request.IdempotencyKey()] = replay[payment.CompleteCheckoutRequest, payment.Checkout]{
		request, checkout,
	}

	return cloneCheckout(checkout), nil
}

// completable checks the simulator's explicit completion preconditions under
// mutex. Completed checkouts are readable through completion without another
// effect; open, expired, and canceled approval flows remain invalid.
func (driver *Driver) completable(id payment.CheckoutID) (payment.Checkout, error) {
	checkout, found := driver.checkouts[id]

	if !found {
		return payment.Checkout{}, payment.ErrNotFound
	}

	if checkout.Status != payment.CheckoutStatusApproved && checkout.Status != payment.CheckoutStatusComplete {
		return payment.Checkout{}, payment.ErrInvalidState
	}

	return checkout, nil
}

// PreparePayment creates an unconfirmed pending simulated payment and an inert
// memory.authorization token. It never approves, confirms, captures, or creates
// a hosted checkout. ResolvePayment is the explicit test control for subsequent
// outcomes. Same-payload key reuse replays the original handoff; changed payloads
// fail with ErrInvalidRequest. The token is not usable with a real SDK.
//
// Example:
//
//	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "order-1")
//	prepared, err := driver.PreparePayment(ctx, request)
//	if err != nil { return err }
//	if err := driver.ResolvePayment(ctx, prepared.PaymentID, payment.StatusSucceeded); err != nil { return err }
func (driver *Driver) PreparePayment(
	ctx context.Context, request payment.PreparePaymentRequest,
) (payment.Preparation, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Preparation{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	prior, found, err := recalled(driver.prepared, request.IdempotencyKey(), request)

	if found {
		return prior, err
	}

	result := payment.Payment{
		ID: payment.ID(driver.nextID("payment")), Amount: request.Amount(),
		Status: payment.StatusPending, NativeStatus: "unconfirmed",
	}
	prepared := payment.Preparation{
		PaymentID: result.ID,
		Action: payment.NewClientAuthorization(payment.ClientProtocolMemoryAuthorization,
			"inert_"+driver.nextID("authorization")),
	}
	driver.payments[result.ID] = result
	driver.prepared[request.IdempotencyKey()] = replay[payment.PreparePaymentRequest, payment.Preparation]{request, prepared}

	return prepared, nil
}

// RefundPayment immediately succeeds for a positive amount of a succeeded
// payment in its original currency and within its remaining balance. Balance
// checks and idempotency recording are atomic, including concurrent retries.
// Refunds never change the original payment's succeeded status.
//
// Example:
//
//	request := payment.NewRefundRequest(id, payment.Money{Amount: 100, Currency: "USD"}, "refund-1")
//	refund, err := driver.RefundPayment(ctx, request)
//	if err != nil { return err }
//	_ = refund.Status
func (driver *Driver) RefundPayment(
	ctx context.Context, request payment.RefundRequest,
) (payment.Refund, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	prior, found, err := recalled(driver.returned, request.IdempotencyKey(), request)

	if found {
		return prior, err
	}

	if err := driver.refundable(request); err != nil {
		return payment.Refund{}, err
	}

	refund := payment.Refund{
		ID: payment.RefundID(driver.nextID("refund")), PaymentID: request.PaymentID(),
		Amount: request.Amount(), Status: payment.RefundStatusSucceeded, NativeStatus: "succeeded",
	}
	driver.refunded[request.PaymentID()] += request.Amount().Amount
	driver.refunds[refund.ID] = refund
	driver.returned[request.IdempotencyKey()] = replay[payment.RefundRequest, payment.Refund]{request, refund}

	return refund, nil
}

// refundable enforces provider-local refund rules under mutex. Subtraction
// avoids overflowing when a caller supplies a very large positive amount.
func (driver *Driver) refundable(request payment.RefundRequest) error {
	result, found := driver.payments[request.PaymentID()]

	if !found {
		return payment.ErrNotFound
	}

	if result.Status != payment.StatusSucceeded {
		return payment.ErrInvalidState
	}

	if request.Amount().Currency != result.Amount.Currency {
		return payment.ErrInvalidRequest
	}

	if request.Amount().Amount > result.Amount.Amount-driver.refunded[result.ID] {
		return payment.ErrInvalidState
	}

	return nil
}

// CancelSubscription immediately cancels a known subscription. Repeated calls
// succeed without refunds, proration, invoice writes, or time-based behavior.
//
// Example:
//
//	err := driver.CancelSubscription(ctx, payment.NewCancelSubscriptionRequest(id))
//	if err != nil { return err }
func (driver *Driver) CancelSubscription(
	ctx context.Context, request payment.CancelSubscriptionRequest,
) error {
	if err := request.Validate(ctx); err != nil {
		return err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	subscription, found := driver.subscriptions[request.SubscriptionID()]

	if !found {
		return payment.ErrNotFound
	}

	subscription.Status, subscription.NativeStatus = payment.SubscriptionStatusCanceled, "canceled"
	subscription.NextBillingAt = nil
	driver.subscriptions[subscription.ID] = subscription

	return nil
}
