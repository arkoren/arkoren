package molliepayment_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestInvalidRefundDoesNotDispatch verifies the driver rejects wrong payment
// families before sending any explicit partial-refund mutation.
func TestInvalidRefundDoesNotDispatch(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		t.Fatal("invalid refund dispatched HTTP")

		return nil, io.EOF
	})

	_, err := driver.RefundPayment(t.Context(), payment.NewRefundRequest("re_example",
		payment.Money{Amount: 100, Currency: "EUR"}, "refund-123"))

	if !errors.Is(err, payment.ErrInvalidRequest) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("invalid refund error = %v", err)
	}
}

// TestRefundCancellationAfterDispatchIsUnknown ensures a partial refund is
// never assumed absent merely because its caller canceled the HTTP operation.
func TestRefundCancellationAfterDispatchIsUnknown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	driver := newDriver(t, func(request *http.Request) (*http.Response, error) {
		calls++
		cancel()

		return nil, request.Context().Err()
	})

	_, err := driver.RefundPayment(ctx, payment.NewRefundRequest("tr_example",
		payment.Money{Amount: 100, Currency: "EUR"}, "refund-123"))

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatched refund cancellation error = %v", err)
	}

	if calls != 1 {
		t.Fatal("canceled refund was retried")
	}
}

// TestRefundLostResponseIsUnknownWithoutReplay verifies transport failure
// cannot cause an automatic second partial refund, even with an idempotency key.
func TestRefundLostResponseIsUnknownWithoutReplay(t *testing.T) {
	t.Parallel()

	calls := 0
	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		calls++

		return nil, io.ErrUnexpectedEOF
	})

	_, err := driver.RefundPayment(t.Context(), payment.NewRefundRequest("tr_example",
		payment.Money{Amount: 100, Currency: "EUR"}, "refund-123"))

	if !errors.Is(err, payment.ErrOutcomeUnknown) || calls != 1 {
		t.Fatalf("lost refund response was not safely classified: %v", err)
	}
}

// TestRefundSendsExplicitPartialAmountAndKey exercises the exact currency,
// decimal and idempotency wire values without any hidden payment lookup.
func TestRefundSendsExplicitPartialAmountAndKey(t *testing.T) {
	t.Parallel()

	calls := 0
	driver := newDriver(t, func(request *http.Request) (*http.Response, error) {
		calls++

		if request.URL.Path != "/v2/payments/tr_example/refunds" || request.Header.Get("Idempotency-Key") != "refund-123" {
			t.Fatal("wrong refund endpoint or key")
		}

		var body map[string]any

		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			t.Fatal(err)
		}

		money := body["amount"].(map[string]any)

		if money["currency"] != "EUR" || money["value"] != "1.00" {
			t.Fatal("refund amount must be explicit decimal and currency")
		}

		return response(201, `{"id":"re_example","paymentId":"tr_example","status":"queued","amount":{"currency":"EUR","value":"1.00"}}`), nil
	})

	_, err := driver.RefundPayment(t.Context(), payment.NewRefundRequest("tr_example",
		payment.Money{Amount: 100, Currency: "EUR"}, "refund-123"))

	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Fatal("refund used a hidden lookup")
	}
}

// TestRefundPendingHasOwnReadableCompoundIdentity exercises accepted pending
// progress and a stateless parent-scoped GET against the protocol fixture.
func TestRefundPendingHasOwnReadableCompoundIdentity(t *testing.T) {
	t.Parallel()

	server := &fixture{}
	driver := newDriver(t, server.RoundTrip)

	refund, err := driver.RefundPayment(t.Context(), payment.NewRefundRequest("tr_example",
		payment.Money{Amount: 100, Currency: "EUR"}, "refund-123"))

	if err != nil {
		t.Fatal(err)
	}

	if refund.Status != payment.RefundStatusPending || refund.ID != "tr_example:re_example" {
		t.Fatal("refund was treated as unknown or lost its parent")
	}

	observed, err := driver.GetRefund(t.Context(), refund.ID)

	if err != nil {
		t.Fatal(err)
	}

	if observed != refund {
		t.Fatal("refund lookup did not preserve its own snapshot")
	}
}

// TestRefundRejectsMalformedCompoundIDs proves bare, overlong and path-like
// identities fail before an authenticated request is constructed.
func TestRefundRejectsMalformedCompoundIDs(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		t.Fatal("malformed compound ID dispatched HTTP")

		return nil, io.EOF
	})

	for _, id := range []payment.RefundID{"re_example", "tr_a:tr_b", "re_a:re_b", "tr_a:re_b:re_c",
		"tr_a/../b:re_c", payment.RefundID("tr_" + strings.Repeat("a", 101) + ":re_b")} {
		_, err := driver.GetRefund(t.Context(), id)

		if !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("invalid compound ID error = %v", err)
		}
	}
}

// TestRefundMismatchedResponseParentIsUnknown prevents a response from being
// repaired with the requested parent after a mutation has already dispatched.
func TestRefundMismatchedResponseParentIsUnknown(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(201, `{"id":"re_example","paymentId":"tr_other","status":"pending","amount":{"currency":"EUR","value":"1.00"}}`), nil
	})

	_, err := driver.RefundPayment(t.Context(), payment.NewRefundRequest("tr_example",
		payment.Money{Amount: 100, Currency: "EUR"}, "refund-123"))

	if !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("mismatched parent error = %v", err)
	}
}

// TestRefundReadRejectsMismatchedParent keeps observation failures distinct
// from ambiguous mutation outcomes and rejects cross-payment identity repair.
func TestRefundReadRejectsMismatchedParent(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(200, `{"id":"re_example","paymentId":"tr_other","status":"pending","amount":{"currency":"EUR","value":"1.00"}}`), nil
	})

	_, err := driver.GetRefund(t.Context(), "tr_example:re_example")

	if err == nil || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("mismatched observation error = %v", err)
	}
}

// TestRefundMissingFieldsIsUnknown covers accepted but unusable response-only
// records without silently borrowing identity, amount or status from input.
func TestRefundMissingFieldsIsUnknown(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{"paymentId":"tr_example","status":"pending","amount":{"currency":"EUR","value":"1.00"}}`,
		`{"id":"re_example","paymentId":"tr_example","status":"pending"}`,
		`{"id":"re_example","paymentId":"tr_example","amount":{"currency":"EUR","value":"1.00"}}`} {
		driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
			return response(201, body), nil
		})

		_, err := driver.RefundPayment(t.Context(), payment.NewRefundRequest("tr_example",
			payment.Money{Amount: 100, Currency: "EUR"}, "refund-123"))

		if !errors.Is(err, payment.ErrOutcomeUnknown) {
			t.Fatalf("missing refund fields must be unknown: %v", err)
		}
	}
}

// observeRefund decodes a refund independently of the original payment and
// proves pending states do not require a second request to establish success.
func observeRefund(t *testing.T, status string) payment.Refund {
	t.Helper()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(200, fmt.Sprintf(`{"id":"re_example","paymentId":"tr_example","status":%q,"amount":{"currency":"EUR","value":"0.00"}}`, status)), nil
	})

	observed, err := driver.GetRefund(t.Context(), "tr_example:re_example")

	if err != nil {
		t.Fatal(err)
	}

	return observed
}

// TestUnknownRefundStatusRemainsZero preserves native progress tokens without
// interpreting future provider states as proof that funds were returned.
func TestUnknownRefundStatusRemainsZero(t *testing.T) {
	t.Parallel()

	observed := observeRefund(t, "future_state")

	if observed.Status != payment.RefundStatusUnknown || observed.NativeStatus != "future_state" {
		t.Fatal("unknown refund status was synthesized")
	}
}

// TestAcceptedRefundProgressIsPending exercises every current accepted but
// unsettled refund state; none is an error or evidence of returned funds.
func TestAcceptedRefundProgressIsPending(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"queued", "pending", "processing"} {
		if observeRefund(t, status).Status != payment.RefundStatusPending {
			t.Fatal("accepted refund must be pending")
		}
	}
}

// TestRefundedRefundSucceeds establishes successful returned funds only from
// the refund's own refunded state, independently of the payment's paid state.
func TestRefundedRefundSucceeds(t *testing.T) {
	t.Parallel()

	if observeRefund(t, "refunded").Status != payment.RefundStatusSucceeded {
		t.Fatal("refunded refund did not succeed")
	}
}

// TestFailedRefundFails preserves unsuccessful returned-funds progress for
// application reconciliation without changing the original payment's status.
func TestFailedRefundFails(t *testing.T) {
	t.Parallel()

	if observeRefund(t, "failed").Status != payment.RefundStatusFailed {
		t.Fatal("failed refund did not fail")
	}
}

// TestCanceledRefundIsCanceled distinguishes an abandoned refund from one
// that is still processing or has returned money successfully.
func TestCanceledRefundIsCanceled(t *testing.T) {
	t.Parallel()

	if observeRefund(t, "canceled").Status != payment.RefundStatusCanceled {
		t.Fatal("canceled refund did not remain canceled")
	}
}

// TestReadRefundInvalidStatusIsUnavailable checks the independent refund
// snapshot path rather than assuming payment snapshot validation covers it.
func TestReadRefundInvalidStatusIsUnavailable(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK,
			`{"id":"re_example","paymentId":"tr_example","amount":{"currency":"EUR","value":"1.00"}}`), nil
	})

	_, err := driver.GetRefund(t.Context(), "tr_example:re_example")

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("invalid refund snapshot classification = %v", err)
	}
}

// TestReadRefundParentMismatchIsUnavailable keeps the compound refund identity
// authoritative without following links or accepting another payment's refund.
func TestReadRefundParentMismatchIsUnavailable(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"id":"re_example","paymentId":"tr_other"}`), nil
	})

	_, err := driver.GetRefund(t.Context(), "tr_example:re_example")

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("refund parent mismatch classification = %v", err)
	}
}
