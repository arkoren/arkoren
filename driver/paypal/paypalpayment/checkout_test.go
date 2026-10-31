package paypalpayment_test

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestCreateCheckoutWire validates exact money/reference encoding in one
// purchase unit rather than testing a driver-private request DTO.
func TestCreateCheckoutWire(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	_, err := backend.CreateCheckout(t.Context(), payment.NewCheckoutRequest(
		payment.Money{Amount: 1200, Currency: "USD"}, "https://shop.example/paid", "create-123",
		func(details *payment.CheckoutRequestBuilder) {
			details.Reference("order-123")
			details.Description("Book")
		}))

	if err != nil {
		t.Fatal(err)
	}

	calls := provider.calls("/v2/checkout/orders")

	if len(calls) != 1 {
		t.Fatal("create should perform exactly one business call")
	}

	var payload struct {
		Intent string `json:"intent"`
		Units  []struct {
			Amount struct {
				Currency string `json:"currency_code"`
				Value    string `json:"value"`
			} `json:"amount"`
			CustomID    string `json:"custom_id"`
			Description string `json:"description"`
		} `json:"purchase_units"`
	}

	if err := json.Unmarshal(calls[0].body, &payload); err != nil {
		t.Fatal(err)
	}

	if payload.Intent != "CAPTURE" || len(payload.Units) != 1 {
		t.Fatal("expected one CAPTURE purchase unit")
	}

	unit := payload.Units[0]

	if unit.Amount.Value != "12.00" || unit.Amount.Currency != "USD" {
		t.Fatal("incorrect exact amount")
	}

	if unit.CustomID != "order-123" || unit.Description != "Book" {
		t.Fatal("reference and description were lost")
	}
}

// TestCreateReturnsApprovalAndCallerHeaders verifies native identity, the
// payer-action fallback and explicit provider idempotency/representation policy.
func TestCreateReturnsApprovalAndCallerHeaders(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	checkout, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}

	if checkout.ID != "order:ORDER" || checkout.ApprovalURL == "" {
		t.Fatal("missing order family or payer-action approval link")
	}

	calls := provider.calls("/v2/checkout/orders")

	if len(calls) != 1 {
		t.Fatal("create should perform exactly one business call")
	}

	if calls[0].header.Get("PayPal-Request-Id") != "order-123" ||
		calls[0].header.Get("Prefer") != "return=representation" {
		t.Fatal("missing idempotency or representation header")
	}
}

// TestOrderExperienceContext uses the current payment_source location and
// documents the intentional cancellation redirect fallback in actual wire JSON.
func TestOrderExperienceContext(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	if _, err := backend.CreateCheckout(t.Context(), checkoutRequest()); err != nil {
		t.Fatal(err)
	}

	calls := provider.calls("/v2/checkout/orders")

	if len(calls) != 1 {
		t.Fatal("expected one order creation")
	}

	var payload struct {
		Source struct {
			PayPal struct {
				Experience struct {
					ReturnURL string `json:"return_url"`
					CancelURL string `json:"cancel_url"`
					Action    string `json:"user_action"`
				} `json:"experience_context"`
			} `json:"paypal"`
		} `json:"payment_source"`
	}

	if err := json.Unmarshal(calls[0].body, &payload); err != nil {
		t.Fatal(err)
	}

	experience := payload.Source.PayPal.Experience

	if experience.ReturnURL != "https://shop.example/paid" || experience.CancelURL != experience.ReturnURL {
		t.Fatal("return URL or cancellation fallback missing")
	}

	if experience.Action != "PAY_NOW" {
		t.Fatal("missing PAY_NOW")
	}
}

// TestCapturePendingDoesNotBecomePaid ensures order completion does not turn a
// pending capture into a successful payment or add a hydration GET.
func TestCapturePendingDoesNotBecomePaid(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	if err := provider.approve(t.Context(), "order:ORDER"); err != nil {
		t.Fatal(err)
	}

	checkout, err := backend.CompleteCheckout(t.Context(),
		payment.NewCompleteCheckoutRequest("order:ORDER", "capture-123"))

	if err != nil {
		t.Fatal(err)
	}

	if checkout.Status != payment.CheckoutStatusComplete || checkout.PaymentStatus != payment.StatusPending {
		t.Fatal("order completion was confused with paid capture")
	}

	if len(provider.calls("/v2/checkout/orders/ORDER")) != 1 {
		t.Fatal("capture hydrated a successful response")
	}

	captures := provider.calls("/v2/checkout/orders/ORDER/capture")

	if len(captures) != 1 || captures[0].header.Get("PayPal-Request-Id") != "capture-123" {
		t.Fatal("capture was retried or caller key changed")
	}
}

// TestCapturePreservesKnownFields retains capture identity, order reference and
// exact amount from the mutation representation, without a hydration read.
func TestCapturePreservesKnownFields(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)
	provider.orderStatus = "APPROVED"

	checkout, err := backend.CompleteCheckout(t.Context(),
		payment.NewCompleteCheckoutRequest("order:ORDER", "capture-123"))

	if err != nil {
		t.Fatal(err)
	}

	if checkout.PaymentID != "capture:CAPTURE" || checkout.Reference != "order-123" {
		t.Fatal("capture family or reference lost")
	}

	if checkout.Amount == nil || checkout.Amount.Amount != 1200 {
		t.Fatal("known amount lost")
	}
}

// TestCompletedOrderDoesNotCaptureAgain returns an observation for an already
// completed native order, never another mutation with a fresh caller key.
func TestCompletedOrderDoesNotCaptureAgain(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)
	provider.orderStatus = "COMPLETED"

	if _, err := backend.CompleteCheckout(t.Context(),
		payment.NewCompleteCheckoutRequest("order:ORDER", "another-capture")); err != nil {
		t.Fatal(err)
	}

	if len(provider.calls("/v2/checkout/orders/ORDER/capture")) != 0 {
		t.Fatal("completed checkout was charged again")
	}
}

// TestSubscriptionCreationReturnsNativeIdentity verifies the native plan and
// customer approval context without claiming any successful payment.
func TestSubscriptionCreationReturnsNativeIdentity(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	checkout, err := backend.CreateSubscriptionCheckout(t.Context(), payment.NewSubscriptionCheckoutRequest(
		"P-MONTHLY", "https://shop.example/account", "subscribe-123",
		func(details *payment.SubscriptionCheckoutRequestBuilder) {
			details.Reference("signup-123")
		}))

	if err != nil {
		t.Fatal(err)
	}

	if checkout.ID != "subscription:I-SUB" || checkout.SubscriptionID != "I-SUB" {
		t.Fatal("native subscription relationship missing before approval")
	}

	created := provider.calls("/v1/billing/subscriptions")

	if len(created) != 1 || !strings.Contains(string(created[0].body), `"user_action":"SUBSCRIBE_NOW"`) {
		t.Fatal("incorrect native subscription creation")
	}
}

// TestSubscriptionCompletionOnlyObservesApproval leaves activation and renewal
// to PayPal without inventing money movement from approved lifecycle.
func TestSubscriptionCompletionOnlyObservesApproval(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	if err := provider.approve(t.Context(), "subscription:I-SUB"); err != nil {
		t.Fatal(err)
	}

	observed, err := backend.CompleteCheckout(t.Context(),
		payment.NewCompleteCheckoutRequest("subscription:I-SUB", "observe-123"))

	if err != nil {
		t.Fatal(err)
	}

	if observed.PaymentStatus != payment.StatusUnknown || observed.NeedsCompletion {
		t.Fatal("subscription approval manufactured a payment")
	}

	if len(provider.calls("/v1/billing/subscriptions/I-SUB/activate")) != 0 {
		t.Fatal("driver invoked activation")
	}
}

// TestWrongFamiliesAndSalesNeverDispatch checks every resource boundary before
// token acquisition, including sale operations whose API compatibility is unknown.
func TestWrongFamiliesAndSalesNeverDispatch(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	_, err := backend.GetPayment(t.Context(), "sale:SALE")

	if !errors.Is(err, payment.ErrUnsupported) {
		t.Fatal(err)
	}

	_, err = backend.RefundPayment(t.Context(), payment.NewRefundRequest("sale:SALE",
		payment.Money{Amount: 100, Currency: "USD"}, "refund-123"))

	if !errors.Is(err, payment.ErrUnsupported) {
		t.Fatal(err)
	}

	_, err = backend.GetPayment(t.Context(), "order:ORDER")

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	_, err = backend.GetCheckout(t.Context(), "capture:CAPTURE")

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	_, err = backend.GetRefund(t.Context(), "sale:SALE")

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	_, err = backend.GetSubscription(t.Context(), "order:ORDER")

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	if len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("invalid/unsupported family acquired credentials")
	}
}

// TestCaptureConflictAllowsOneObservation handles the documented concurrent
// already-captured conflict without retrying capture or polling indefinitely.
func TestCaptureConflictAllowsOneObservation(t *testing.T) {
	t.Parallel()

	reads, captures := 0, 0
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			captures++

			return response(422, `{"name":"UNPROCESSABLE_ENTITY","details":[{"issue":"ORDER_ALREADY_CAPTURED"}]}`), nil
		}

		reads++

		if reads == 1 {
			return response(200, `{"id":"ORDER","status":"APPROVED"}`), nil
		}

		return response(200, `{"id":"ORDER","status":"COMPLETED","purchase_units":[{
			"payments":{"captures":[{"id":"CAPTURE","status":"PENDING",
			"amount":{"currency_code":"USD","value":"12.00"}}]}}]}`), nil
	}))

	checkout, err := backend.CompleteCheckout(t.Context(),
		payment.NewCompleteCheckoutRequest("order:ORDER", "capture-123"))

	if err != nil {
		t.Fatal(err)
	}

	if reads != 2 || captures != 1 {
		t.Fatal("capture conflict was retried or reconciled more than once")
	}

	if checkout.PaymentStatus != payment.StatusPending {
		t.Fatal("completed conflicting order manufactured paid capture")
	}
}

// TestUnapprovedOrderCannotCapture verifies the explicit approval guard before
// any money-moving completion operation is sent.
func TestUnapprovedOrderCannotCapture(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	_, err := backend.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest("order:ORDER", "capture-123"))

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatal(err)
	}

	if len(provider.calls("/v2/checkout/orders/ORDER/capture")) != 0 {
		t.Fatal("unapproved order was captured")
	}
}

// TestForintUsesWholeProviderUnits verifies the intentional PayPal/ISO exponent
// difference in both directions, including explicit refund amount encoding.
func TestForintUsesWholeProviderUnits(t *testing.T) {
	t.Parallel()

	var sent []string
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)

		if err != nil {
			return nil, err
		}

		sent = append(sent, string(body))

		if strings.HasSuffix(request.URL.Path, "/refund") {
			return response(201, `{"id":"REFUND","status":"COMPLETED","amount":{"currency_code":"HUF","value":"10"}}`), nil
		}

		return response(201, `{"id":"ORDER","status":"CREATED",
			"purchase_units":[{"amount":{"currency_code":"HUF","value":"10"}}]}`), nil
	}))

	checkout, err := backend.CreateCheckout(t.Context(), payment.NewCheckoutRequest(
		payment.Money{Amount: 1000, Currency: "HUF"}, "https://shop.example/paid", "huf-order"))

	if err != nil {
		t.Fatal(err)
	}

	if checkout.Amount == nil || checkout.Amount.Amount != 1000 {
		t.Fatal("incoming HUF major units were reinterpreted as canonical minor units")
	}

	refund, err := backend.RefundPayment(t.Context(), payment.NewRefundRequest("capture:CAPTURE",
		payment.Money{Amount: 1000, Currency: "HUF"}, "huf-refund"))

	if err != nil {
		t.Fatal(err)
	}

	if refund.Amount.Amount != 1000 {
		t.Fatal("refund did not preserve canonical HUF scale")
	}

	for _, body := range sent {
		if !strings.Contains(body, `"value":"10"`) {
			t.Fatal("HUF wire amount must contain whole forints")
		}
	}
}

// TestFractionalForintRejectedBeforeIO prevents silent rounding in both payment
// creation and explicit refunds while preserving core's ISO exponent two.
func TestFractionalForintRejectedBeforeIO(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	_, err := backend.CreateCheckout(t.Context(), payment.NewCheckoutRequest(
		payment.Money{Amount: 1234, Currency: "HUF"}, "https://shop.example/paid", "huf-order"))

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	_, err = backend.RefundPayment(t.Context(), payment.NewRefundRequest("capture:CAPTURE",
		payment.Money{Amount: 1234, Currency: "HUF"}, "huf-refund"))

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	if len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("fractional forints acquired a token")
	}
}

// TestUnsupportedThreeDecimalCurrenciesRejectedBeforeIO verifies the reviewed
// wallet subset rather than forcing all canonical currencies to two decimals.
func TestUnsupportedThreeDecimalCurrenciesRejectedBeforeIO(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	for _, currency := range []string{"BHD", "KWD", "OMR", "KRW", "INR", "ZAR"} {
		_, err := backend.CreateCheckout(t.Context(), payment.NewCheckoutRequest(
			payment.Money{Amount: 1000, Currency: currency}, "https://shop.example/paid", "unsupported-order"))

		if !errors.Is(err, payment.ErrUnsupported) {
			t.Fatal(err)
		}
	}

	if len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("unsupported currency acquired a token")
	}
}
