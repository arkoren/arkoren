package paypalpayment_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestRefundedCapturePreservesSuccessHistory follows PayPal's Payments v2
// capture_status enum. A refund changes returned-funds progress, not the fact
// that the original capture succeeded; Amount remains the observed gross amount.
func TestRefundedCapturePreservesSuccessHistory(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return response(200, `{"id":"CAPTURE","status":"REFUNDED","amount":{"currency_code":"USD","value":"12.00"}}`), nil
	}))

	result, err := backend.GetPayment(t.Context(), "capture:CAPTURE")

	if err != nil {
		t.Fatal(err)
	}

	if result.Status != payment.StatusSucceeded || result.NativeStatus != "REFUNDED" {
		t.Fatal("refunded capture lost its successful payment history or native state")
	}

	if result.Amount != (payment.Money{Amount: 1200, Currency: "USD"}) {
		t.Fatal("refunded capture manufactured a remaining balance")
	}
}

// TestPartiallyRefundedCapturePreservesGrossAmount keeps the historical success
// and original gross money without guessing the size of any separate refund.
func TestPartiallyRefundedCapturePreservesGrossAmount(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return response(200, `{"id":"CAPTURE","status":"PARTIALLY_REFUNDED",
			"amount":{"currency_code":"USD","value":"12.00"}}`), nil
	}))

	result, err := backend.GetPayment(t.Context(), "capture:CAPTURE")

	if err != nil {
		t.Fatal(err)
	}

	if result.Status != payment.StatusSucceeded || result.NativeStatus != "PARTIALLY_REFUNDED" {
		t.Fatal("partially refunded capture lost its successful history or native state")
	}

	if result.Amount != (payment.Money{Amount: 1200, Currency: "USD"}) {
		t.Fatal("partial refund was subtracted from the observed gross capture")
	}
}

// TestProviderZeroDecimalCurrenciesRejectExcessPrecision uses the same strict
// rule for HUF and JPY: even trailing fractional zeros are not silently accepted.
func TestProviderZeroDecimalCurrenciesRejectExcessPrecision(t *testing.T) {
	t.Parallel()

	for _, currency := range []string{"HUF", "JPY"} {
		backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
			return response(200, fmt.Sprintf(`{"id":"CAPTURE","status":"COMPLETED",
				"amount":{"currency_code":%q,"value":"10.00"}}`, currency)), nil
		}))

		_, err := backend.GetPayment(t.Context(), "capture:CAPTURE")

		if !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatal("zero-decimal provider currency was rounded or tolerated")
		}
	}
}

// TestCaptureRepresentationKeepsKnownCaptureAmount handles PayPal capture
// representations that omit purchase-unit amount but include exact capture
// money and custom_id. These are response observations, not post-GET hydration.
func TestCaptureRepresentationKeepsKnownCaptureAmount(t *testing.T) {
	t.Parallel()

	reads := 0
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			reads++

			return response(200, `{"id":"ORDER","status":"APPROVED"}`), nil
		}

		return response(201, `{"id":"ORDER","status":"COMPLETED","purchase_units":[{"payments":{"captures":[{
			"id":"CAPTURE","status":"COMPLETED","custom_id":"order-123",
			"amount":{"currency_code":"USD","value":"12.50"}}]}}]}`), nil
	}))

	checkout, err := backend.CompleteCheckout(t.Context(),
		payment.NewCompleteCheckoutRequest("order:ORDER", "capture-123"))

	if err != nil {
		t.Fatal(err)
	}

	if checkout.Amount == nil || checkout.Amount.Amount != 1250 {
		t.Fatal("observed capture money was discarded")
	}

	if checkout.Reference != "order-123" || reads != 1 {
		t.Fatal("capture reference lost or a hydration read was performed")
	}
}
