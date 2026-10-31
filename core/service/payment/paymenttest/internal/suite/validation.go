package suite

import (
	"context"
	"testing"
	"time"

	"arkoren.dev/core/service/payment"
)

// runValidation checks the complete validation boundary on one fixture.
// No valid operation is dispatched, so it is safe for local protocol fixtures.
func runValidation(t *testing.T, driver payment.Driver) {
	t.Helper()

	if driver == nil {
		t.Fatal("paymenttest: factory returned nil Driver")
	}

	checkInvalidRequests(t, driver)
	checkNilContext(t, driver)
	checkCanceledContext(t, driver)
	checkExpiredContext(t, driver)
}

// checkInvalidRequests checks zero request and identity rejection for every
// operation. Providers must validate even operations they do not support.
func checkInvalidRequests(t *testing.T, driver payment.Driver) {
	t.Helper()

	ctx := t.Context()

	_, err := driver.PreparePayment(ctx, payment.PreparePaymentRequest{})

	requireError(t, err, payment.ErrInvalidRequest)

	_, err = driver.CreateCheckout(ctx, payment.CheckoutRequest{})

	requireError(t, err, payment.ErrInvalidRequest)

	_, err = driver.CreateSubscriptionCheckout(ctx, payment.SubscriptionCheckoutRequest{})

	requireError(t, err, payment.ErrInvalidRequest)

	_, err = driver.GetCheckout(ctx, "")

	requireError(t, err, payment.ErrInvalidRequest)

	_, err = driver.CompleteCheckout(ctx, payment.CompleteCheckoutRequest{})

	requireError(t, err, payment.ErrInvalidRequest)

	_, err = driver.GetPayment(ctx, "")

	requireError(t, err, payment.ErrInvalidRequest)

	_, err = driver.RefundPayment(ctx, payment.RefundRequest{})

	requireError(t, err, payment.ErrInvalidRequest)

	_, err = driver.GetRefund(ctx, "")

	requireError(t, err, payment.ErrInvalidRequest)

	_, err = driver.GetSubscription(ctx, "")

	requireError(t, err, payment.ErrInvalidRequest)

	err = driver.CancelSubscription(ctx, payment.CancelSubscriptionRequest{})

	requireError(t, err, payment.ErrInvalidRequest)
}

// checkNilContext uses valid requests to isolate nil context rejection.
// A nil context is invalid input rather than a substitute for Background.
func checkNilContext(t *testing.T, driver payment.Driver) {
	t.Helper()

	// ctx deliberately exercises the contract's explicit nil-context guard.
	var ctx context.Context

	checkContextErrors(ctx, t, driver, payment.ErrInvalidRequest)
}

// checkCanceledContext verifies cancellation wins before dispatch, even for
// identities which do not exist and operations which are unsupported.
func checkCanceledContext(t *testing.T, driver payment.Driver) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	checkContextErrors(ctx, t, driver, context.Canceled)
}

// checkExpiredContext uses a past deadline rather than waiting in real time.
// An undispatched expired call must preserve context.DeadlineExceeded.
func checkExpiredContext(t *testing.T, driver payment.Driver) {
	t.Helper()

	ctx, cancel := context.WithDeadline(t.Context(), time.Time{})
	t.Cleanup(cancel)

	checkContextErrors(ctx, t, driver, context.DeadlineExceeded)
}

// checkContextErrors exercises each typed boundary with otherwise valid input.
// No provider-specific identity syntax is assumed beyond the core contract.
func checkContextErrors(ctx context.Context, t *testing.T, driver payment.Driver, target error) {
	t.Helper()

	_, err := driver.PreparePayment(ctx, payment.NewPreparePaymentRequest(
		payment.Money{Amount: 100, Currency: "USD"}, "prepare-test"))

	requireError(t, err, target)

	_, err = driver.CreateCheckout(ctx, checkoutRequest())

	requireError(t, err, target)

	_, err = driver.CreateSubscriptionCheckout(ctx, payment.NewSubscriptionCheckoutRequest("plan-test", "https://shop.example/account", "signup-test"))

	requireError(t, err, target)

	_, err = driver.GetCheckout(ctx, "checkout-test")

	requireError(t, err, target)

	_, err = driver.CompleteCheckout(ctx, payment.NewCompleteCheckoutRequest("checkout-test", "complete-test"))

	requireError(t, err, target)

	_, err = driver.GetPayment(ctx, "payment-test")

	requireError(t, err, target)

	_, err = driver.RefundPayment(ctx, payment.NewRefundRequest("payment-test", payment.Money{Amount: 100, Currency: "USD"}, "refund-test"))

	requireError(t, err, target)

	_, err = driver.GetRefund(ctx, "refund-test")

	requireError(t, err, target)

	_, err = driver.GetSubscription(ctx, "subscription-test")

	requireError(t, err, target)

	err = driver.CancelSubscription(ctx, payment.NewCancelSubscriptionRequest("subscription-test"))

	requireError(t, err, target)
}
