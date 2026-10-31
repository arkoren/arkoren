package paymenttest_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/paymenttest"
)

// TestFakePreparationRequiresExplicitValidReply prevents unscripted or partial
// initialization results from inventing implicit successful client handoffs.
func TestFakePreparationRequiresExplicitValidReply(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1")

	_, err := fake.PreparePayment(t.Context(), request)

	if !errors.Is(err, paymenttest.ErrUnexpectedCall) {
		t.Fatal("unscripted preparation succeeded")
	}

	fake.ReplyPreparePayment(payment.Preparation{PaymentID: "pi_1"}, nil)

	_, err = fake.PreparePayment(t.Context(), request)

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("partial preparation implicitly succeeded")
	}

	if len(fake.PreparedPayments()) != 0 {
		t.Fatal("unaccepted initialization recorded as accepted")
	}
}

// TestFakePreparationUncertaintyIsExplicit records a valid accepted session
// handoff alongside a lost response, without creating a fake financial ledger.
func TestFakePreparationUncertaintyIsExplicit(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1")
	want := payment.Preparation{Action: payment.NewClientSession("vendor.session", "session-1", "sdk-data")}
	cause := errors.New("transport stopped")
	fake.ReplyPreparePayment(want, errors.Join(payment.ErrOutcomeUnknown, cause))

	prepared, err := fake.PreparePayment(t.Context(), request)

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, cause) {
		t.Fatal("uncertainty identities lost")
	}

	if prepared != want || len(fake.PreparedPayments()) != 1 {
		t.Fatal("accepted handoff not preserved")
	}

	fake.AssertPreparedPayment(t)

	_, err = fake.GetPayment(t.Context(), "pi_1")

	if !errors.Is(err, paymenttest.ErrUnexpectedCall) {
		t.Fatal("preparation invented a financial read reply")
	}

	_, err = fake.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest("cs_1", "capture-1"))

	if !errors.Is(err, paymenttest.ErrUnexpectedCall) {
		t.Fatal("preparation invented a completion reply")
	}
}

// TestFakePreparationPreflightDoesNotConsumeQueues checks context precedence,
// one-shot failure precedence, and independent FIFO reply preservation.
func TestFakePreparationPreflightDoesNotConsumeQueues(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1")
	want := payment.Preparation{Action: payment.NewClientOrder("paypal.order", "ORDER-1")}
	fake.ReplyPreparePayment(want, nil)
	fake.FailNext(payment.ErrUnavailable)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := fake.PreparePayment(ctx, payment.PreparePaymentRequest{})

	if !errors.Is(err, context.Canceled) {
		t.Fatal("invalid input displaced canceled context")
	}

	if len(fake.Attempted().PreparedPayments) != 0 {
		t.Fatal("preflight changed history")
	}

	_, err = fake.PreparePayment(t.Context(), request)

	if !errors.Is(err, payment.ErrUnavailable) {
		t.Fatal("preflight consumed failure")
	}

	prepared, err := fake.PreparePayment(t.Context(), request)

	if err != nil || prepared != want {
		t.Fatal("failure consumed reply")
	}
}

// TestFakePreparationHistoryAndResetDetach checks caller-owned history slices,
// immutable request getters, and clearing of both scripts and acceptance.
func TestFakePreparationHistoryAndResetDetach(t *testing.T) {
	t.Parallel()

	fake := paymenttest.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1")
	fake.ReplyPreparePayment(payment.Preparation{Action: payment.NewClientOrder("paypal.order", "ORDER-1")}, nil)

	if _, err := fake.PreparePayment(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	history := fake.PreparedPayments()
	history[0] = payment.PreparePaymentRequest{}

	if fake.PreparedPayments()[0].IdempotencyKey() != "prepare-1" {
		t.Fatal("history slice aliased fake")
	}

	fake.ReplyPreparePayment(payment.Preparation{Action: payment.NewClientOrder("paypal.order", "ORDER-2")}, nil)
	fake.Reset()

	if len(fake.PreparedPayments()) != 0 || len(fake.Attempted().PreparedPayments) != 0 {
		t.Fatal("reset retained history")
	}

	_, err := fake.PreparePayment(t.Context(), request)

	if !errors.Is(err, paymenttest.ErrUnexpectedCall) {
		t.Fatal("reset retained script")
	}
}

// TestSuitePreparationRunsWithoutHostedApproval verifies the additive operation
// is not accidentally gated by subscription plans or hosted customer approval.
func TestSuitePreparationRunsWithoutHostedApproval(t *testing.T) {
	t.Parallel()

	accepted := 0

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		t.Helper()

		fake := paymenttest.New()
		fake.ReplyPreparePayment(payment.Preparation{Action: payment.NewClientSession("vendor.session", "session-1", "data")}, nil)
		t.Cleanup(func() {
			accepted += len(fake.PreparedPayments())
		})

		return paymenttest.Harness{Driver: fake}
	}, paymenttest.WithoutApproval())

	if accepted != 1 {
		t.Fatal("hosted limitation suppressed client preparation")
	}
}
