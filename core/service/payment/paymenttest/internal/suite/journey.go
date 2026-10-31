package suite

import (
	"cmp"
	"reflect"
	"testing"

	"arkoren.dev/core/service/payment"
)

// runPayment verifies approval, explicit completion when required, settlement,
// and an explicit refund. It does not impose vendor idempotency retention.
func runPayment(t *testing.T, harness Fixture, config Policy) {
	t.Helper()

	if harness.Approve == nil || harness.ResolvePayment == nil {
		t.Fatal("paymenttest: payment journey requires Approve and ResolvePayment hooks")
	}

	ctx := t.Context()
	request := checkoutRequest()
	request = payment.NewCheckoutRequest(cmp.Or(harness.Amount, request.Amount()), request.ReturnURL(), request.IdempotencyKey(),
		func(details *payment.CheckoutRequestBuilder) {
			details.CancelURL(request.CancelURL())
		})

	checkout, err := harness.Driver.CreateCheckout(ctx, request)

	requireSuccess(t, err)

	if checkout.ID == "" || checkout.Status != payment.CheckoutStatusOpen {
		t.Fatal("new checkout must have an ID and open approval")
	}

	checkAmountOwnership(t, harness.Driver, checkout)
	requireSuccess(t, harness.Approve(ctx, checkout.ID))

	checkout, err = harness.Driver.GetCheckout(ctx, checkout.ID)

	requireSuccess(t, err)

	checkout = completePayment(t, harness.Driver, checkout, config)
	id := resolvePaymentID(t, harness, checkout)

	requireSuccess(t, harness.ResolvePayment(ctx, id))

	result, err := harness.Driver.GetPayment(ctx, id)

	requireSuccess(t, err)

	if result.ID != id || result.Status != payment.StatusSucceeded {
		t.Fatal("resolved payment identity or outcome mismatch")
	}

	checkRefund(t, harness.Driver, result, config)
}

// resolvePaymentID accepts a checkout's observed identity or invokes the
// fixture's single bounded observation step. The suite never polls a provider.
func resolvePaymentID(t *testing.T, harness Fixture, checkout payment.Checkout) payment.ID {
	t.Helper()

	if checkout.PaymentID != "" {
		return checkout.PaymentID
	}

	if harness.ResolvePaymentID == nil {
		t.Fatal("paymenttest: delayed payment identity requires ResolvePaymentID hook")
	}

	id, err := harness.ResolvePaymentID(t.Context(), checkout.ID)

	requireSuccess(t, err)

	if id == "" {
		t.Fatal("paymenttest: fixture did not observe a payment ID")
	}

	return id
}

// completePayment verifies declared unsupported completion without effects, or
// completes the approved checkout only when the provider requires that step.
func completePayment(
	t *testing.T, driver payment.Driver, checkout payment.Checkout, config Policy,
) payment.Checkout {
	t.Helper()

	request := payment.NewCompleteCheckoutRequest(checkout.ID, "conformance-complete")

	if config.WithoutCompletion {
		_, err := driver.CompleteCheckout(t.Context(), request)

		requireError(t, err, payment.ErrUnsupported)

		observed, err := driver.GetCheckout(t.Context(), checkout.ID)

		requireSuccess(t, err)

		if !reflect.DeepEqual(observed, checkout) || observed.NeedsCompletion {
			t.Fatal("unsupported completion changed checkout or remains required")
		}

		return observed
	}

	if !checkout.NeedsCompletion {
		return checkout
	}

	completed, err := driver.CompleteCheckout(t.Context(), request)

	requireSuccess(t, err)

	if completed.Status != payment.CheckoutStatusComplete || completed.NeedsCompletion {
		t.Fatal("completion did not finish checkout approval flow")
	}

	return completed
}

// checkRefund verifies the refund's own readable resource, independently from
// the original payment, or checks the declared unsupported operation is inert.
func checkRefund(t *testing.T, driver payment.Driver, result payment.Payment, config Policy) {
	t.Helper()

	request := payment.NewRefundRequest(result.ID, payment.Money{
		Amount: min(100, result.Amount.Amount), Currency: result.Amount.Currency,
	}, "conformance-refund")

	refund, err := driver.RefundPayment(t.Context(), request)

	if config.WithoutRefunds {
		requireError(t, err, payment.ErrUnsupported)

		observed, err := driver.GetPayment(t.Context(), result.ID)

		requireSuccess(t, err)

		if observed != result {
			t.Fatal("unsupported refund changed original payment")
		}

		return
	}

	requireSuccess(t, err)
	checkRefundSnapshot(t, refund, request)

	observed, err := driver.GetRefund(t.Context(), refund.ID)

	if config.WithoutRefundLookup {
		requireError(t, err, payment.ErrUnsupported)

		return
	}

	requireSuccess(t, err)
	checkRefundSnapshot(t, observed, request)

	if observed.ID != refund.ID {
		t.Fatal("refund lookup identity mismatch")
	}
}

// checkRefundSnapshot checks only mandatory identity and amount plus any
// observed parent. A provider may legitimately omit the optional payment ID,
// and a pending refund is not required to settle during the test.
func checkRefundSnapshot(t *testing.T, refund payment.Refund, request payment.RefundRequest) {
	t.Helper()

	if refund.ID == "" || refund.Amount != request.Amount() {
		t.Fatal("refund identity or explicit amount mismatch")
	}

	if refund.PaymentID != "" && refund.PaymentID != request.PaymentID() {
		t.Fatal("refund observed a conflicting original payment")
	}
}

// checkAmountOwnership ensures an optional snapshot amount is caller-owned.
// Mutating that pointer must not change a separately observed provider amount.
func checkAmountOwnership(t *testing.T, driver payment.Driver, checkout payment.Checkout) {
	t.Helper()

	if checkout.Amount == nil {
		return
	}

	original := *checkout.Amount
	checkout.Amount.Amount++
	observed, err := driver.GetCheckout(t.Context(), checkout.ID)

	requireSuccess(t, err)

	if observed.Amount == nil || *observed.Amount != original {
		t.Fatal("mutating checkout snapshot changed provider state")
	}
}

// runSubscription verifies lifecycle is separate from approval and cancellation
// is immediate and repeatable. No paid-through or invoice claim is inferred.
func runSubscription(t *testing.T, harness Fixture) {
	t.Helper()

	if harness.Approve == nil || harness.ActivateSubscription == nil {
		t.Fatal("paymenttest: subscription journey requires Approve and ActivateSubscription hooks")
	}

	ctx := t.Context()

	checkout, err := harness.Driver.CreateSubscriptionCheckout(ctx, payment.NewSubscriptionCheckoutRequest(
		harness.PlanReference, "https://shop.example/account", "conformance-signup", func(details *payment.SubscriptionCheckoutRequestBuilder) {
			details.CancelURL("https://shop.example/plans")
		}))

	requireSuccess(t, err)

	if checkout.Status != payment.CheckoutStatusOpen {
		t.Fatal("subscription checkout must start open")
	}

	requireSuccess(t, harness.Approve(ctx, checkout.ID))

	checkout, err = harness.Driver.GetCheckout(ctx, checkout.ID)

	requireSuccess(t, err)

	if checkout.SubscriptionID == "" {
		t.Fatal("approved fixture must expose its subscription ID")
	}

	requireSuccess(t, harness.ActivateSubscription(ctx, checkout.SubscriptionID))

	subscription, err := harness.Driver.GetSubscription(ctx, checkout.SubscriptionID)

	requireSuccess(t, err)

	if subscription.Status != payment.SubscriptionStatusActive {
		t.Fatal("explicitly activated subscription must be active")
	}

	checkBillingOwnership(t, harness.Driver, subscription)

	request := payment.NewCancelSubscriptionRequest(subscription.ID)

	requireSuccess(t, harness.Driver.CancelSubscription(ctx, request))
	requireSuccess(t, harness.Driver.CancelSubscription(ctx, request))

	subscription, err = harness.Driver.GetSubscription(ctx, subscription.ID)

	requireSuccess(t, err)

	if subscription.Status != payment.SubscriptionStatusCanceled {
		t.Fatal("cancellation must be observable immediately")
	}
}

// checkBillingOwnership tests optional billing-time pointer detachment only when
// the provider observes one. Absence must remain meaningful, not synthesized.
func checkBillingOwnership(t *testing.T, driver payment.Driver, subscription payment.Subscription) {
	t.Helper()

	if subscription.NextBillingAt == nil {
		return
	}

	original := *subscription.NextBillingAt
	*subscription.NextBillingAt = subscription.NextBillingAt.AddDate(1, 0, 0)

	observed, err := driver.GetSubscription(t.Context(), subscription.ID)

	requireSuccess(t, err)

	if observed.NextBillingAt == nil || !observed.NextBillingAt.Equal(original) {
		t.Fatal("mutating billing snapshot changed provider state")
	}
}
