package paymenttest_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/paymenttest"
)

// TestFakeValidationDoesNotRecordOrConsumeFailure checks request and context
// preflight failures before fake histories or failure queues can change.
func TestFakeValidationDoesNotRecordOrConsumeFailure(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	fake.FailNext(payment.ErrUnavailable)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := fake.PreparePayment(ctx, payment.PreparePaymentRequest{})

	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	_, err = fake.CreateCheckout(t.Context(), payment.CheckoutRequest{})

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(fake.Attempted(), paymenttest.History{}) {
		t.Fatal("invalid calls were recorded")
	}

	_, err = fake.GetPayment(t.Context(), "pi_test")

	if !errors.Is(err, payment.ErrUnavailable) {
		t.Fatalf("validation consumed FailNext: %v", err)
	}
}

// TestFakeUnexpectedCallsFail proves valid unconfigured calls cannot report
// invented success; they appear only in attempted history.
func TestFakeUnexpectedCallsFail(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()

	_, err := fake.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, paymenttest.ErrUnexpectedCall) {
		t.Fatalf("unscripted creation = %v", err)
	}

	if len(fake.Attempted().CreatedCheckouts) != 1 || len(fake.Accepted().CreatedCheckouts) != 0 {
		t.Fatal("unexpected call history mismatch")
	}
}

// TestFakeFailurePrecedenceKeepsQueuedReply distinguishes injected pre-dispatch
// failure from scripted acceptance while testing one-shot/persistent precedence.
func TestFakeFailurePrecedenceKeepsQueuedReply(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	fake.ReplyGetPayment(payment.Payment{ID: "pi_test", Status: payment.StatusPending}, nil)
	fake.FailWith(payment.ErrUnavailable)
	fake.FailNext(payment.ErrRateLimited)

	_, err := fake.GetPayment(t.Context(), "pi_test")

	if !errors.Is(err, payment.ErrRateLimited) {
		t.Fatalf("FailNext precedence = %v", err)
	}

	_, err = fake.GetPayment(t.Context(), "pi_test")

	if !errors.Is(err, payment.ErrUnavailable) {
		t.Fatalf("persistent failure = %v", err)
	}

	fake.FailWith(nil)

	result, err := fake.GetPayment(t.Context(), "pi_test")

	if err != nil || result.Status != payment.StatusPending {
		t.Fatalf("failure consumed script: %v", err)
	}

	if len(fake.Attempted().ReadPayments) != 3 || len(fake.Accepted().ReadPayments) != 1 {
		t.Fatal("failure/acceptance histories mismatch")
	}
}

// TestFakeAmbiguousAcceptanceIsExplicit preserves the uncertain error while
// recording that the fixture accepted a refund. Empty error-only replies do not.
func TestFakeAmbiguousAcceptanceIsExplicit(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	request := payment.NewRefundRequest("pi_test", payment.Money{Amount: 100, Currency: "USD"}, "refund-1")
	fake.ReplyRefundPayment(payment.Refund{ID: "re_test"}, payment.ErrOutcomeUnknown)
	fake.ReplyRefundPayment(payment.Refund{}, payment.ErrOutcomeUnknown)

	refund, err := fake.RefundPayment(t.Context(), request)

	if !errors.Is(err, payment.ErrOutcomeUnknown) || refund.ID != "re_test" {
		t.Fatalf("accepted ambiguous refund = %v", err)
	}

	_, err = fake.RefundPayment(t.Context(), request)

	if !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("unaccepted ambiguous refund = %v", err)
	}

	if len(fake.Attempted().RefundedPayments) != 2 || len(fake.Accepted().RefundedPayments) != 1 {
		t.Fatal("ambiguous attempts and accepted effects were conflated")
	}

	fake.AssertRefundedPayment(t, func(observed payment.RefundRequest) bool {
		return observed == request
	})
}

// TestFakeCheckoutRepliesDetachAmounts checks ownership when capturing replies
// and again when returning them, including separately queued shared inputs.
func TestFakeCheckoutRepliesDetachAmounts(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	amount := payment.Money{Amount: 1000, Currency: "USD"}
	response := payment.Checkout{ID: "cs_test", Amount: &amount}
	fake.ReplyCreateCheckout(response, nil)
	fake.ReplyCreateCheckout(response, nil)

	amount.Amount = 2000

	first, err := fake.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil || first.Amount.Amount != 1000 {
		t.Fatalf("queued amount retained caller pointer: %v", err)
	}

	first.Amount.Amount = 3000

	second, err := fake.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil || second.Amount.Amount != 1000 {
		t.Fatalf("reply exposed another queue entry: %v", err)
	}

	fake.AssertCreatedCheckout(t)
}

// TestFakeSubscriptionRepliesDetachBillingTime exercises both optional pointer
// boundaries without a clock option, sleeps, or background renewal simulation.
func TestFakeSubscriptionRepliesDetachBillingTime(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	nextBillingAt := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	want := nextBillingAt
	response := payment.Subscription{ID: "sub_test", NextBillingAt: &nextBillingAt}
	fake.ReplyGetSubscription(response, nil)
	fake.ReplyGetSubscription(response, nil)

	nextBillingAt = nextBillingAt.AddDate(1, 0, 0)

	first, err := fake.GetSubscription(t.Context(), "sub_test")

	if err != nil || !first.NextBillingAt.Equal(want) {
		t.Fatalf("queued time retained caller pointer: %v", err)
	}

	*first.NextBillingAt = nextBillingAt

	second, err := fake.GetSubscription(t.Context(), "sub_test")

	if err != nil || !second.NextBillingAt.Equal(want) {
		t.Fatalf("reply exposed another queue entry: %v", err)
	}
}

// TestFakeHistoriesAreCopied proves callers cannot rewrite typed historical
// requests or mistake a successful creation for an automatic subsequent read.
func TestFakeHistoriesAreCopied(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	fake.ReplyCreateCheckout(payment.Checkout{ID: "cs_test"}, nil)

	if _, err := fake.CreateCheckout(t.Context(), checkoutRequest()); err != nil {
		t.Fatal(err)
	}

	attempted := fake.Attempted()

	accepted := fake.Accepted()
	attempted.CreatedCheckouts[0] = payment.NewCheckoutRequest(payment.Money{Amount: 1, Currency: "USD"},
		"https://shop.example/paid", "rewritten")
	accepted.CreatedCheckouts[0] = attempted.CreatedCheckouts[0]

	if fake.Attempted().CreatedCheckouts[0].IdempotencyKey() != "order-1" {
		t.Fatal("attempt history was not copied")
	}

	if fake.Accepted().CreatedCheckouts[0].Amount().Amount != 1000 {
		t.Fatal("acceptance history was not copied")
	}

	_, err := fake.GetCheckout(t.Context(), "cs_test")

	if !errors.Is(err, paymenttest.ErrUnexpectedCall) {
		t.Fatalf("creation invented readable ledger state: %v", err)
	}
}

// TestFakeCompletionAndCheckoutReadQueuesAreIndependent checks scripts consume
// in per-operation FIFO order without causing cross-operation calls.
func TestFakeCompletionAndCheckoutReadQueuesAreIndependent(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	fake.ReplyGetCheckout(payment.Checkout{ID: "cs_test", Status: payment.CheckoutStatusApproved}, nil)
	fake.ReplyCompleteCheckout(payment.Checkout{ID: "cs_test", Status: payment.CheckoutStatusComplete}, nil)
	fake.ReplyGetCheckout(payment.Checkout{ID: "cs_test", Status: payment.CheckoutStatusComplete}, nil)

	first, err := fake.GetCheckout(t.Context(), "cs_test")

	if err != nil || first.Status != payment.CheckoutStatusApproved {
		t.Fatalf("first read = %v", err)
	}

	_, err = fake.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest("cs_test", "complete-1"))

	if err != nil {
		t.Fatal(err)
	}

	second, err := fake.GetCheckout(t.Context(), "cs_test")

	if err != nil || second.Status != payment.CheckoutStatusComplete {
		t.Fatalf("second read = %v", err)
	}

	fake.AssertCompletedCheckout(t)
}

// TestFakeSubscriptionAndRefundReadScriptsCompleteSurface covers operations
// whose scripted records must remain independent of other operation replies.
func TestFakeSubscriptionAndRefundReadScriptsCompleteSurface(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	fake.ReplyCreateSubscriptionCheckout(payment.Checkout{ID: "cs_sub", SubscriptionID: "sub_test"}, nil)

	checkout, err := fake.CreateSubscriptionCheckout(t.Context(), payment.NewSubscriptionCheckoutRequest("plan-test", "https://shop.example/account", "signup-1"))

	if err != nil || checkout.SubscriptionID != "sub_test" {
		t.Fatalf("subscription creation = %v", err)
	}

	fake.ReplyGetRefund(payment.Refund{ID: "re_test", Status: payment.RefundStatusPending}, nil)

	refund, err := fake.GetRefund(t.Context(), "re_test")

	if err != nil || refund.Status != payment.RefundStatusPending {
		t.Fatalf("refund observation = %v", err)
	}

	fake.AssertCreatedSubscriptionCheckout(t)
}

// TestFakeCancellationCanBeAcceptedWithUnknownOutcome checks the error-only
// mutation has an explicit accepted-effect script without a fake ledger.
func TestFakeCancellationCanBeAcceptedWithUnknownOutcome(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	fake.ReplyCancelSubscription(payment.ErrUnavailable)
	fake.ReplyCanceledSubscription(payment.ErrOutcomeUnknown)

	request := payment.NewCancelSubscriptionRequest("sub_test")

	err := fake.CancelSubscription(t.Context(), request)

	if !errors.Is(err, payment.ErrUnavailable) {
		t.Fatalf("unaccepted cancellation = %v", err)
	}

	err = fake.CancelSubscription(t.Context(), request)

	if !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("accepted ambiguous cancellation = %v", err)
	}

	if len(fake.Attempted().CanceledSubscriptions) != 2 || len(fake.Accepted().CanceledSubscriptions) != 1 {
		t.Fatal("cancellation effects and attempts were conflated")
	}

	fake.AssertCanceledSubscription(t)
}

// TestFakeResetClearsScriptsHistoriesAndFailures ensures each test can start a
// new scripted lifetime without retaining a previous success or failure plan.
func TestFakeResetClearsScriptsHistoriesAndFailures(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	fake.ReplyCancelSubscription(nil)

	if err := fake.CancelSubscription(t.Context(), payment.NewCancelSubscriptionRequest("sub_test")); err != nil {
		t.Fatal(err)
	}

	fake.ReplyGetPayment(payment.Payment{ID: "pi_test"}, nil)
	fake.FailNext(payment.ErrRateLimited)
	fake.FailWith(payment.ErrUnavailable)
	fake.Reset()

	if !reflect.DeepEqual(fake.Attempted(), paymenttest.History{}) {
		t.Fatal("reset retained attempts")
	}

	if !reflect.DeepEqual(fake.Accepted(), paymenttest.History{}) {
		t.Fatal("reset retained acceptance")
	}

	_, err := fake.GetPayment(t.Context(), "pi_test")

	if !errors.Is(err, paymenttest.ErrUnexpectedCall) {
		t.Fatalf("reset retained script or failure: %v", err)
	}
}

// TestFakeConcurrentRepliesAndHistories verifies serialized script consumption
// and snapshot access under race detection without nondeterministic assertions.
func TestFakeConcurrentRepliesAndHistories(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()

	for range 16 {
		fake.ReplyGetPayment(payment.Payment{ID: "pi_test"}, nil)
	}

	var workers sync.WaitGroup

	for range 16 {
		workers.Go(func() {
			if _, err := fake.GetPayment(t.Context(), "pi_test"); err != nil {
				t.Error(err)
			}

			_ = fake.Attempted()

			_ = fake.Accepted()
		})
	}

	workers.Wait()

	if len(fake.Attempted().ReadPayments) != 16 || len(fake.Accepted().ReadPayments) != 16 {
		t.Fatal("concurrent fake lost calls")
	}
}

// checkoutRequest gives isolated fake tests valid explicit money and key syntax.
// Tests do not depend on provider balances or idempotency retention semantics.
func checkoutRequest() payment.CheckoutRequest {
	return payment.NewCheckoutRequest(payment.Money{Amount: 1000, Currency: "USD"}, "https://shop.example/paid", "order-1")
}
