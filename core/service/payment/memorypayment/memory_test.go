package memorypayment_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/memorypayment"
	"arkoren.dev/core/service/payment/paymenttest"
)

// TestConformance exercises every boundary and both explicit simulator journeys.
func TestConformance(t *testing.T) {
	t.Parallel()

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		driver := memorypayment.New()

		return paymenttest.Harness{
			Driver: driver, PlanReference: "plan-monthly", Approve: driver.ApproveCheckout,
			ResolvePayment: func(ctx context.Context, id payment.ID) error {
				return driver.ResolvePayment(ctx, id, payment.StatusSucceeded)
			},
			ActivateSubscription: func(ctx context.Context, id payment.SubscriptionID) error {
				return driver.ResolveSubscription(ctx, id, payment.SubscriptionStatusActive, nil)
			},
		}
	})
}

// TestCreateCheckoutDoesNotApprove proves creation and reads never advance
// approval or create a payment, and uses a non-routable approval URL.
func TestCreateCheckoutDoesNotApprove(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	checkout, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}

	if checkout.Status != payment.CheckoutStatusOpen || checkout.PaymentID != "" {
		t.Fatal("creation approved or created a payment")
	}

	if checkout.NeedsCompletion {
		t.Fatal("open checkout requires completion before buyer approval")
	}

	if checkout.ApprovalURL != "https://test.invalid/approve/"+string(checkout.ID) {
		t.Fatal("unexpected simulator approval URL")
	}

	observed, err := driver.GetCheckout(t.Context(), checkout.ID)

	if err != nil || observed.Status != payment.CheckoutStatusOpen {
		t.Fatalf("lookup advanced checkout: %v", err)
	}
}

// TestApprovalMarksPaymentReadyForCompletion checks buyer approval is the point
// where explicit payment completion becomes necessary, without settling money.
func TestApprovalMarksPaymentReadyForCompletion(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	checkout, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.ApproveCheckout(t.Context(), checkout.ID); err != nil {
		t.Fatal(err)
	}

	approved, err := driver.GetCheckout(t.Context(), checkout.ID)

	if err != nil {
		t.Fatal(err)
	}

	if approved.Status != payment.CheckoutStatusApproved || !approved.NeedsCompletion {
		t.Fatal("approved payment checkout must require explicit completion")
	}

	if approved.PaymentID != "" || approved.PaymentStatus == payment.StatusSucceeded {
		t.Fatal("approval created or settled a payment")
	}
}

// TestCompletionRequiresApprovalThenCreatesPending checks a premature attempt
// does not reserve its key, and completion still requires explicit settlement.
func TestCompletionRequiresApprovalThenCreatesPending(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	checkout, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}

	request := payment.NewCompleteCheckoutRequest(checkout.ID, "complete-1")

	_, err = driver.CompleteCheckout(t.Context(), request)

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatalf("premature completion = %v", err)
	}

	if err := driver.ApproveCheckout(t.Context(), checkout.ID); err != nil {
		t.Fatal(err)
	}

	completed, err := driver.CompleteCheckout(t.Context(), request)

	if err != nil || completed.PaymentStatus != payment.StatusPending {
		t.Fatalf("completion must create pending payment: %v", err)
	}

	result, err := driver.GetPayment(t.Context(), completed.PaymentID)

	if err != nil || result.Status != payment.StatusPending {
		t.Fatalf("lookup settled payment: %v", err)
	}
}

// TestConcurrentCreationReplaysOneCheckout proves mutex-protected key replay
// returns one identity without sharing optional amount pointers with callers.
func TestConcurrentCreationReplaysOneCheckout(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	request := checkoutRequest()
	results := make(chan payment.Checkout, 16)
	var workers sync.WaitGroup

	for range cap(results) {
		workers.Go(func() {
			checkout, err := driver.CreateCheckout(t.Context(), request)

			if err != nil {
				t.Error(err)
			}

			results <- checkout
		})
	}

	workers.Wait()
	close(results)

	first := <-results

	for checkout := range results {
		if checkout.ID != first.ID || checkout.Amount == first.Amount {
			t.Fatal("retries duplicated checkout or shared amount pointers")
		}
	}
}

// TestCreationKeyCannotChangeRequest checks provider-local replay safeguards.
// A rejected reuse leaves the accepted checkout amount intact.
func TestCreationKeyCannotChangeRequest(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	request := checkoutRequest()

	checkout, err := driver.CreateCheckout(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	amount := request.Amount()
	amount.Amount++
	request = payment.NewCheckoutRequest(amount, request.ReturnURL(), request.IdempotencyKey())

	_, err = driver.CreateCheckout(t.Context(), request)

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("changed key input = %v", err)
	}

	observed, err := driver.GetCheckout(t.Context(), checkout.ID)

	if err != nil || observed.Amount.Amount != 1000 {
		t.Fatalf("rejected input changed amount: %v", err)
	}
}

// TestCompletionReplayKeepsOriginalPendingResponse separates retry responses
// from live GET observations and prevents a second payment under a new key.
func TestCompletionReplayKeepsOriginalPendingResponse(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	checkout := completedCheckout(t, driver)

	if err := driver.ResolvePayment(t.Context(), checkout.PaymentID, payment.StatusSucceeded); err != nil {
		t.Fatal(err)
	}

	request := payment.NewCompleteCheckoutRequest(checkout.ID, "complete-1")

	replayed, err := driver.CompleteCheckout(t.Context(), request)

	if err != nil || replayed.PaymentID != checkout.PaymentID || replayed.PaymentStatus != payment.StatusPending {
		t.Fatalf("completion replay changed original response: %v", err)
	}

	request = payment.NewCompleteCheckoutRequest(request.CheckoutID(), "complete-2")

	observed, err := driver.CompleteCheckout(t.Context(), request)

	if err != nil || observed.PaymentID != checkout.PaymentID {
		t.Fatalf("new completion key duplicated payment: %v", err)
	}

	if observed.PaymentStatus != payment.StatusSucceeded || observed.NeedsCompletion {
		t.Fatal("new completion key lost settled observation or requires another completion")
	}
}

// TestConcurrentRefundReplayMovesMoneyOnce verifies concurrent same-key retries
// preserve balance so the full remainder is refundable afterwards.
func TestConcurrentRefundReplayMovesMoneyOnce(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	checkout := completedCheckout(t, driver)

	if err := driver.ResolvePayment(t.Context(), checkout.PaymentID, payment.StatusSucceeded); err != nil {
		t.Fatal(err)
	}

	request := payment.NewRefundRequest(checkout.PaymentID, payment.Money{Amount: 400, Currency: "USD"}, "refund-1")
	results := make(chan payment.Refund, 16)
	var workers sync.WaitGroup

	for range cap(results) {
		workers.Go(func() {
			refund, err := driver.RefundPayment(t.Context(), request)

			if err != nil {
				t.Error(err)
			}

			results <- refund
		})
	}

	workers.Wait()
	close(results)

	first := <-results

	for refund := range results {
		if refund.ID != first.ID {
			t.Fatal("same key created multiple refunds")
		}
	}

	request = payment.NewRefundRequest(request.PaymentID(), payment.Money{Amount: 600, Currency: "USD"}, "refund-rest")

	if _, err := driver.RefundPayment(t.Context(), request); err != nil {
		t.Fatalf("retries consumed additional balance: %v", err)
	}
}

// TestRefundRulesEnforceCurrencyBalanceAndStatus checks provider-local rejection
// without consuming a key on failure or changing the original payment outcome.
func TestRefundRulesEnforceCurrencyBalanceAndStatus(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	checkout := completedCheckout(t, driver)
	request := payment.NewRefundRequest(checkout.PaymentID, payment.Money{Amount: 1000, Currency: "USD"}, "refund-1")

	_, err := driver.RefundPayment(t.Context(), request)

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatalf("pending refund = %v", err)
	}

	if err := driver.ResolvePayment(t.Context(), checkout.PaymentID, payment.StatusSucceeded); err != nil {
		t.Fatal(err)
	}

	request = payment.NewRefundRequest(request.PaymentID(), payment.Money{Amount: 1000, Currency: "EUR"}, request.IdempotencyKey())

	_, err = driver.RefundPayment(t.Context(), request)

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("wrong-currency refund = %v", err)
	}

	request = payment.NewRefundRequest(request.PaymentID(), payment.Money{Amount: 1000, Currency: "USD"}, request.IdempotencyKey())

	if _, err := driver.RefundPayment(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	request = payment.NewRefundRequest(request.PaymentID(), request.Amount(), "refund-extra")

	_, err = driver.RefundPayment(t.Context(), request)

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatalf("excess refund = %v", err)
	}

	result, err := driver.GetPayment(t.Context(), checkout.PaymentID)

	if err != nil || result.Status != payment.StatusSucceeded {
		t.Fatalf("refund changed original payment status: %v", err)
	}
}

// TestRefundKeyCannotChangeAmount verifies failed changed-input reuse neither
// consumes money nor changes the original returned-funds record.
func TestRefundKeyCannotChangeAmount(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	checkout := completedCheckout(t, driver)

	if err := driver.ResolvePayment(t.Context(), checkout.PaymentID, payment.StatusSucceeded); err != nil {
		t.Fatal(err)
	}

	request := payment.NewRefundRequest(checkout.PaymentID, payment.Money{Amount: 200, Currency: "USD"}, "refund-1")

	refund, err := driver.RefundPayment(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	request = payment.NewRefundRequest(request.PaymentID(), payment.Money{Amount: 300, Currency: "USD"}, request.IdempotencyKey())

	_, err = driver.RefundPayment(t.Context(), request)

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("changed refund key = %v", err)
	}

	observed, err := driver.GetRefund(t.Context(), refund.ID)

	if err != nil || observed != refund {
		t.Fatalf("rejected refund changed accepted record: %v", err)
	}
}

// TestSubscriptionApprovalDoesNotActivate separates approval from lifecycle
// and proves catalog prices and successful payments are never synthesized.
func TestSubscriptionApprovalDoesNotActivate(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	checkout, err := driver.CreateSubscriptionCheckout(t.Context(), subscriptionRequest())

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.ApproveCheckout(t.Context(), checkout.ID); err != nil {
		t.Fatal(err)
	}

	subscription, err := driver.GetSubscription(t.Context(), checkout.SubscriptionID)

	if err != nil || subscription.Status != payment.SubscriptionStatusPending {
		t.Fatalf("approval activated subscription: %v", err)
	}

	if subscription.PlanReference != "plan-monthly" || checkout.Amount != nil || checkout.PaymentID != "" {
		t.Fatal("subscription checkout invented price or payment")
	}
}

// TestSubscriptionApprovalCompletesEnrollment checks approval finishes checkout
// enrollment without requiring the application's explicit capture operation.
func TestSubscriptionApprovalCompletesEnrollment(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	checkout, err := driver.CreateSubscriptionCheckout(t.Context(), subscriptionRequest())

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.ApproveCheckout(t.Context(), checkout.ID); err != nil {
		t.Fatal(err)
	}

	approved, err := driver.GetCheckout(t.Context(), checkout.ID)

	if err != nil {
		t.Fatal(err)
	}

	if approved.Status != payment.CheckoutStatusComplete || approved.NeedsCompletion {
		t.Fatal("subscription approval did not complete enrollment")
	}
}

// TestSubscriptionCompletionObservesEnrollmentOnly checks approved enrollment
// can be observed through completion without activating lifecycle or settling.
func TestSubscriptionCompletionObservesEnrollmentOnly(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	checkout, err := driver.CreateSubscriptionCheckout(t.Context(), subscriptionRequest())

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.ApproveCheckout(t.Context(), checkout.ID); err != nil {
		t.Fatal(err)
	}

	completed, err := driver.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest(checkout.ID, "complete-subscription"))

	if err != nil {
		t.Fatal(err)
	}

	if completed.Status != payment.CheckoutStatusComplete || completed.NeedsCompletion {
		t.Fatal("approved subscription enrollment is not complete")
	}

	if completed.PaymentID != "" || completed.PaymentStatus == payment.StatusSucceeded {
		t.Fatal("subscription completion created or settled a payment")
	}

	subscription, err := driver.GetSubscription(t.Context(), completed.SubscriptionID)

	if err != nil || subscription.Status != payment.SubscriptionStatusPending {
		t.Fatalf("subscription completion activated lifecycle: %v", err)
	}
}

// TestSubscriptionBillingPointersAreDetached checks the control's input and
// every read's output ownership, then verifies cancellation is terminal.
func TestSubscriptionBillingPointersAreDetached(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	checkout, err := driver.CreateSubscriptionCheckout(t.Context(), subscriptionRequest())

	if err != nil {
		t.Fatal(err)
	}

	nextBillingAt := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	want := nextBillingAt

	err = driver.ResolveSubscription(t.Context(), checkout.SubscriptionID, payment.SubscriptionStatusActive, &nextBillingAt)

	if err != nil {
		t.Fatal(err)
	}

	nextBillingAt = nextBillingAt.AddDate(1, 0, 0)

	subscription, err := driver.GetSubscription(t.Context(), checkout.SubscriptionID)

	if err != nil || !subscription.NextBillingAt.Equal(want) {
		t.Fatalf("control retained caller pointer: %v", err)
	}

	*subscription.NextBillingAt = nextBillingAt

	again, err := driver.GetSubscription(t.Context(), checkout.SubscriptionID)

	if err != nil || !again.NextBillingAt.Equal(want) {
		t.Fatalf("lookup exposed stored pointer: %v", err)
	}

	request := payment.NewCancelSubscriptionRequest(checkout.SubscriptionID)

	if err := driver.CancelSubscription(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	err = driver.ResolveSubscription(t.Context(), checkout.SubscriptionID, payment.SubscriptionStatusActive, nil)

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatalf("reactivated canceled subscription: %v", err)
	}
}

// TestSubscriptionKeyReplaysOriginalCheckout checks subscription request
// comparison and replay independently from one-time checkout key scope.
func TestSubscriptionKeyReplaysOriginalCheckout(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	request := subscriptionRequest()

	first, err := driver.CreateSubscriptionCheckout(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	again, err := driver.CreateSubscriptionCheckout(t.Context(), request)

	if err != nil || again.ID != first.ID || again.SubscriptionID != first.SubscriptionID {
		t.Fatalf("subscription replay = %v", err)
	}

	request = payment.NewSubscriptionCheckoutRequest("plan-yearly", request.ReturnURL(), request.IdempotencyKey())

	_, err = driver.CreateSubscriptionCheckout(t.Context(), request)

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("changed subscription input = %v", err)
	}
}

// TestResetForgetsRecordsAndKeysButNotIdentitySequence proves stale references
// remain missing after the same input is accepted in the new memory lifetime.
func TestResetForgetsRecordsAndKeysButNotIdentitySequence(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	first, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}

	driver.Reset()

	again, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil || first.ID == again.ID {
		t.Fatalf("reset reused stale ID: %v", err)
	}

	_, err = driver.GetCheckout(t.Context(), first.ID)

	if !errors.Is(err, payment.ErrNotFound) {
		t.Fatalf("stale checkout survived reset: %v", err)
	}
}

// TestMissingAndWrongFamilyIDsReturnNotFound ensures syntactically valid IDs
// are looked up in their own resource family rather than inferred or converted.
func TestMissingAndWrongFamilyIDsReturnNotFound(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	checkout, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}

	_, err = driver.GetCheckout(t.Context(), "memory_checkout_missing")

	if !errors.Is(err, payment.ErrNotFound) {
		t.Fatalf("missing checkout = %v", err)
	}

	_, err = driver.GetPayment(t.Context(), payment.ID(checkout.ID))

	if !errors.Is(err, payment.ErrNotFound) {
		t.Fatalf("checkout used as payment = %v", err)
	}

	_, err = driver.GetRefund(t.Context(), payment.RefundID(checkout.ID))

	if !errors.Is(err, payment.ErrNotFound) {
		t.Fatalf("checkout used as refund = %v", err)
	}

	_, err = driver.GetSubscription(t.Context(), payment.SubscriptionID(checkout.ID))

	if !errors.Is(err, payment.ErrNotFound) {
		t.Fatalf("checkout used as subscription = %v", err)
	}
}

// TestExpiredCheckoutCannotBeCompleted confirms terminal approval outcomes
// are explicit and cannot be undone by a later approve or complete call.
func TestExpiredCheckoutCannotBeCompleted(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()

	checkout, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.ResolveCheckout(t.Context(), checkout.ID, payment.CheckoutStatusExpired); err != nil {
		t.Fatal(err)
	}

	err = driver.ApproveCheckout(t.Context(), checkout.ID)

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatalf("approval after expiry = %v", err)
	}

	_, err = driver.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest(checkout.ID, "complete-expired"))

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatalf("completion after expiry = %v", err)
	}
}

// TestPaymentResolutionCannotReplaceTerminalOutcome prevents an explicit
// failure fixture from later becoming successful without a new payment.
func TestPaymentResolutionCannotReplaceTerminalOutcome(t *testing.T) {
	t.Parallel()

	driver := memorypayment.New()
	checkout := completedCheckout(t, driver)

	if err := driver.ResolvePayment(t.Context(), checkout.PaymentID, payment.StatusFailed); err != nil {
		t.Fatal(err)
	}

	if err := driver.ResolvePayment(t.Context(), checkout.PaymentID, payment.StatusFailed); err != nil {
		t.Fatal(err)
	}

	err := driver.ResolvePayment(t.Context(), checkout.PaymentID, payment.StatusSucceeded)

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatalf("replaced failed outcome: %v", err)
	}
}

// checkoutRequest supplies a positive, explicit one-time amount for isolated
// scenarios. Every test owns a new driver, so this key is local to that test.
func checkoutRequest() payment.CheckoutRequest {
	return payment.NewCheckoutRequest(payment.Money{Amount: 1000, Currency: "USD"}, "https://shop.example/paid", "order-1")
}

// subscriptionRequest supplies an existing synthetic plan, never a price or
// inferred amount. Simulator plan references do not require a catalog registry.
func subscriptionRequest() payment.SubscriptionCheckoutRequest {
	return payment.NewSubscriptionCheckoutRequest("plan-monthly", "https://shop.example/account", "signup-1")
}

// completedCheckout performs the shared approval-to-pending-money setup used by
// settlement/refund scenarios. It deliberately does not resolve the payment.
func completedCheckout(t *testing.T, driver *memorypayment.Driver) payment.Checkout {
	t.Helper()

	checkout, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.ApproveCheckout(t.Context(), checkout.ID); err != nil {
		t.Fatal(err)
	}

	checkout, err = driver.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest(checkout.ID, "complete-1"))

	if err != nil {
		t.Fatal(err)
	}

	return checkout
}
