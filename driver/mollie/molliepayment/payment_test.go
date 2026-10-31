package molliepayment_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"arkoren.dev/core/service/payment"
)

// observePayment decodes one complete native status using the public GET path,
// leaving the real response validation and core money parser under test.
func observePayment(t *testing.T, status string) payment.Payment {
	t.Helper()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(200, fmt.Sprintf(`{"id":"tr_example","status":%q,"amount":{"currency":"EUR","value":"0.00"}}`, status)), nil
	})

	observed, err := driver.GetPayment(t.Context(), "tr_example")

	if err != nil {
		t.Fatal(err)
	}

	return observed
}

// TestUnknownPaymentStatusPreservesZeroAndZeroMoney ensures future native
// tokens cannot establish successful settlement or erase an observed zero.
func TestUnknownPaymentStatusPreservesZeroAndZeroMoney(t *testing.T) {
	t.Parallel()

	observed := observePayment(t, "future_state")

	if observed.Status != payment.StatusUnknown || observed.NativeStatus != "future_state" || observed.Amount.Amount != 0 {
		t.Fatal("unknown status or zero money was synthesized")
	}
}

// TestAuthorizedAndPendingPaymentsAreNotPaid protects the authorization versus
// settlement distinction for both current nonterminal processing states.
func TestAuthorizedAndPendingPaymentsAreNotPaid(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"authorized", "pending"} {
		if observePayment(t, status).Status != payment.StatusPending {
			t.Fatal("unsettled payment was not pending")
		}
	}
}

// TestPaidPaymentSucceeds permits only the authoritative paid native state to
// establish success; checkout approval never substitutes for this observation.
func TestPaidPaymentSucceeds(t *testing.T) {
	t.Parallel()

	if observePayment(t, "paid").Status != payment.StatusSucceeded {
		t.Fatal("paid payment was not succeeded")
	}
}

// TestOpenPaymentRequiresAction preserves the hosted buyer's outstanding work
// rather than suggesting that an open checkout is an accepted money movement.
func TestOpenPaymentRequiresAction(t *testing.T) {
	t.Parallel()

	if observePayment(t, "open").Status != payment.StatusRequiresAction {
		t.Fatal("open payment must require action")
	}
}

// TestFailedAndExpiredPaymentsFail ensures terminal nonpayment does not remain
// pending merely because the native status differs from the contract token.
func TestFailedAndExpiredPaymentsFail(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"failed", "expired"} {
		if observePayment(t, status).Status != payment.StatusFailed {
			t.Fatal("terminal nonpayment did not fail")
		}
	}
}

// TestCanceledPaymentIsCanceled preserves customer cancellation as distinct
// from a declined or expired payment without dispatching any mutation.
func TestCanceledPaymentIsCanceled(t *testing.T) {
	t.Parallel()

	if observePayment(t, "canceled").Status != payment.StatusCanceled {
		t.Fatal("canceled payment did not remain canceled")
	}
}

// TestReadPaymentMissingAmountIsUnavailable distinguishes an unusable provider
// snapshot from invalid caller input, without implying a money-moving outcome.
func TestReadPaymentMissingAmountIsUnavailable(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"id":"tr_example","status":"paid"}`), nil
	})

	_, err := driver.GetPayment(t.Context(), "tr_example")

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("missing response amount classification = %v", err)
	}

	if errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("provider snapshot failure was attributed to caller input")
	}
}

// TestReadPaymentIdentityMismatchIsUnavailable refuses a different provider
// resource without attributing the reply to invalid caller identity syntax.
func TestReadPaymentIdentityMismatchIsUnavailable(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"id":"tr_other","status":"paid"}`), nil
	})

	_, err := driver.GetPayment(t.Context(), "tr_example")

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("response identity mismatch classification = %v", err)
	}
}
