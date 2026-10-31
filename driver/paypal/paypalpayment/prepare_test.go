package paypalpayment_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestPreparePaymentFacadeWire verifies the real Orders v2 mobile profile,
// including risk data and the absence of hosted or fabricated callback URLs.
func TestPreparePaymentFacadeWire(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		attempts++

		if request.Method != http.MethodPost || request.URL.Path != "/v2/checkout/orders" {
			t.Fatal("preparation used another endpoint")
		}

		if request.Header.Get("PayPal-Client-Metadata-Id") != "sdk-risk-123" ||
			request.Header.Get("PayPal-Request-Id") != "prepare-123" {
			t.Fatal("missing caller-owned risk reference or idempotency header")
		}

		var body map[string]any

		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			t.Fatal(err)
		}

		expected := map[string]any{"intent": "CAPTURE", "purchase_units": []any{map[string]any{
			"amount":    map[string]any{"currency_code": "USD", "value": "12.00"},
			"custom_id": "order-123", "description": "Book"}}}

		if !reflect.DeepEqual(body, expected) {
			t.Fatal("native order contains unexpected fields or URLs")
		}

		return response(http.StatusCreated, `{"id":"NATIVE123","status":"CREATED"}`), nil
	}))

	client := payment.New(backend)

	prepared, err := client.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123", func(details *payment.PreparePaymentRequestBuilder) {
			details.Channel(payment.ClientChannelAndroid)
			details.DeviceReference("sdk-risk-123")
			details.Reference("order-123")
			details.Description("Book")
		}))

	if err != nil {
		t.Fatal(err)
	}

	order, ok := prepared.Action.Order()

	if !ok || order.Protocol != "paypal.order" || order.OrderID != "NATIVE123" {
		t.Fatal("missing real provider order action")
	}

	if prepared.CheckoutID != "order:NATIVE123" || prepared.PaymentID != "" || attempts != 1 {
		t.Fatal("preparation fabricated payment identity, hydrated or captured")
	}
}

// TestPreparePaymentNativeChannelsShareProfile confirms that the optional
// channel hint cannot silently select a different authentication or order flow.
func TestPreparePaymentNativeChannelsShareProfile(t *testing.T) {
	t.Parallel()

	var bodies []map[string]any
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		var body map[string]any

		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			t.Fatal(err)
		}

		bodies = append(bodies, body)

		if request.Header.Get("PayPal-Client-Metadata-Id") != "" {
			t.Fatal("omitted device reference acquired a value")
		}

		return response(http.StatusCreated, `{"id":"NATIVE123","status":"CREATED"}`), nil
	}))

	for _, channel := range []payment.ClientChannel{"", payment.ClientChannelIOS, payment.ClientChannelAndroid} {
		_, err := backend.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
			payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123", func(details *payment.PreparePaymentRequestBuilder) {
				details.Channel(channel)
			}))

		if err != nil {
			t.Fatal(err)
		}
	}

	if len(bodies) != 3 {
		t.Fatal("native channel dispatched unexpected requests")
	}

	if !reflect.DeepEqual(bodies[0], bodies[1]) || !reflect.DeepEqual(bodies[1], bodies[2]) {
		t.Fatal("native channels changed the plain one-off order")
	}
}

// TestPreparedOrderRequiresObservedApproval keeps preparation, actual provider
// approval and merchant capture separate even when the SDK reports completion.
func TestPreparedOrderRequiresObservedApproval(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)
	client := payment.New(backend)

	prepared, err := client.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123", func(details *payment.PreparePaymentRequestBuilder) {
			details.DeviceReference("sdk-risk-123")
		}))

	if err != nil {
		t.Fatal(err)
	}

	_, err = client.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest(prepared.CheckoutID, "capture-123"))

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatal("unapproved native order was capturable")
	}

	if len(provider.calls("/v2/checkout/orders/ORDER/capture")) != 0 {
		t.Fatal("preparation or SDK completion inferred approval")
	}

	if len(provider.calls("/v2/checkout/orders/ORDER")) != 1 {
		t.Fatal("completion did not observe actual provider approval")
	}
}

// TestPreparedOrderCapturesOnlyAfterApproval checks explicit capture and
// response-only settlement while keeping SDK risk data scoped to creation.
func TestPreparedOrderCapturesOnlyAfterApproval(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)
	client := payment.New(backend)

	prepared, err := client.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123", func(details *payment.PreparePaymentRequestBuilder) {
			details.DeviceReference("sdk-risk-123")
		}))

	if err != nil {
		t.Fatal(err)
	}

	if err := provider.approve(t.Context(), prepared.CheckoutID); err != nil {
		t.Fatal(err)
	}

	checkout, err := client.CompleteCheckout(t.Context(),
		payment.NewCompleteCheckoutRequest(prepared.CheckoutID, "capture-123"))

	if err != nil {
		t.Fatal(err)
	}

	if checkout.PaymentID != "capture:CAPTURE" || checkout.PaymentStatus != payment.StatusPending {
		t.Fatal("explicit capture lost identity or invented settlement")
	}

	captures := provider.calls("/v2/checkout/orders/ORDER/capture")

	if len(captures) != 1 || captures[0].header.Get("PayPal-Client-Metadata-Id") != "" {
		t.Fatal("capture replayed or leaked device risk data")
	}

	if provider.calls("/v1/oauth2/token")[0].header.Get("PayPal-Client-Metadata-Id") != "" {
		t.Fatal("risk data leaked to token acquisition")
	}
}

// TestPreparePaymentWebRejectedBeforeIO makes the unsupported web policy
// explicit rather than silently offering a different authorization experience.
func TestPreparePaymentWebRejectedBeforeIO(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	_, err := backend.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123", func(details *payment.PreparePaymentRequestBuilder) {
			details.Channel(payment.ClientChannelWeb)
		}))

	if !errors.Is(err, payment.ErrUnsupported) || len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("web preparation did not fail locally")
	}
}

// TestPreparePaymentCallbackRejectedBeforeIO prevents silently ignoring a
// native callback that belongs in the SDK's URL configuration, not Orders v2.
func TestPreparePaymentCallbackRejectedBeforeIO(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	_, err := backend.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123", func(details *payment.PreparePaymentRequestBuilder) {
			details.ReturnURL("shopapp://paypal/return")
		}))

	if !errors.Is(err, payment.ErrUnsupported) || len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("unused callback was ignored or dispatched")
	}
}

// TestPreparePaymentContextPrecedesInvalidInput checks the extra operation's
// direct-driver boundary, including nil and already-canceled contexts.
func TestPreparePaymentContextPrecedesInvalidInput(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := backend.PreparePayment(ctx, payment.PreparePaymentRequest{})

	if !errors.Is(err, context.Canceled) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal("canceled context lost precedence")
	}

	var missing context.Context

	_, err = backend.PreparePayment(missing, payment.PreparePaymentRequest{})

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("nil context was accepted")
	}

	if len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("invalid context acquired credentials")
	}
}

// TestPreparePaymentInvalidAmountsNeverDispatch rejects zero, negative and
// fractional provider amounts before any authentication or mutation.
func TestPreparePaymentInvalidAmountsNeverDispatch(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	for _, amount := range []payment.Money{{Amount: 0, Currency: "USD"}, {Amount: -1, Currency: "USD"},
		{Amount: 1234, Currency: "HUF"}} {
		_, err := backend.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(amount, "prepare-123"))

		if !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("invalid amount error = %v", err)
		}
	}

	if len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("invalid amount acquired credentials")
	}
}

// TestPreparePaymentUnsupportedCurrencyNeverDispatch preserves the existing
// PayPal currency allowlist without reclassifying a portable valid amount.
func TestPreparePaymentUnsupportedCurrencyNeverDispatch(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	_, err := backend.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 1000, Currency: "KWD"}, "prepare-123"))

	if !errors.Is(err, payment.ErrUnsupported) || len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("unsupported currency was dispatched or changed category")
	}
}

// TestPreparePaymentUnsafeRiskDataNeverDispatch ensures header-safe bounded
// risk data, including values allowed as prose but not as a metadata identifier.
func TestPreparePaymentUnsafeRiskDataNeverDispatch(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	for _, reference := range []string{
		"private\r\nInjected: secret", "private value", "private-☃", strings.Repeat("x", 256),
	} {
		_, err := backend.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
			payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123", func(details *payment.PreparePaymentRequestBuilder) {
				details.DeviceReference(reference)
			}))

		if !errors.Is(err, payment.ErrInvalidRequest) || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe or disclosed risk-data error: %v", err)
		}
	}

	if len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("unsafe risk data acquired credentials")
	}
}

// TestPreparePaymentMalformedSuccessIsUnknown covers incomplete and inconsistent
// native responses without an automatic retry, hydration or invented action.
func TestPreparePaymentMalformedSuccessIsUnknown(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`not-json`, `{}`, `{"id":"ORDER"}`, `{"id":"bad/id","status":"CREATED"}`,
		`{"id":"ORDER","status":"UNRECOGNIZED"}`, `{"id":"ORDER","status":"COMPLETED"}`,
		`{"id":"ORDER","status":"CREATED","purchase_units":[{"amount":{"currency_code":"EUR","value":"12.00"}}]}`} {
		attempts := 0
		backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
			attempts++

			return response(http.StatusCreated, body), nil
		}))

		prepared, err := backend.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
			payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123"))

		if !errors.Is(err, payment.ErrOutcomeUnknown) || attempts != 1 {
			t.Fatalf("malformed preparation replayed or lost uncertainty: %v", err)
		}

		if prepared.CheckoutID != "" || prepared.PaymentID != "" {
			t.Fatal("malformed representation manufactured an identity")
		}
	}
}

// TestPreparePaymentServerFailureIsSafeAndNeverRetried keeps provider prose,
// SDK risk data and credentials out of errors while requiring reconciliation.
func TestPreparePaymentServerFailureIsSafeAndNeverRetried(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		attempts++

		return response(http.StatusServiceUnavailable,
			`{"name":"INTERNAL_SERVER_ERROR","message":"private-risk-secret"}`), nil
	}))

	_, err := backend.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123", func(details *payment.PreparePaymentRequestBuilder) {
			details.DeviceReference("private-risk-secret")
		}))

	if !errors.Is(err, payment.ErrOutcomeUnknown) || attempts != 1 {
		t.Fatal("uncertain create was replayed or classified as a rejection")
	}

	if strings.Contains(err.Error(), "private-risk-secret") {
		t.Fatal("error disclosed risk data or provider prose")
	}
}

// TestPreparePaymentExplicitRecoveryKeepsCallerKey distinguishes one automatic
// attempt from an application-controlled retry of the same persisted request.
func TestPreparePaymentExplicitRecoveryKeepsCallerKey(t *testing.T) {
	t.Parallel()

	var keys []string
	var bodies []map[string]any
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		var body map[string]any

		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			t.Fatal(err)
		}

		keys = append(keys, request.Header.Get("PayPal-Request-Id"))
		bodies = append(bodies, body)

		return nil, io.ErrUnexpectedEOF
	}))

	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "prepare-123")

	_, err := backend.PreparePayment(t.Context(), request)

	if !errors.Is(err, payment.ErrOutcomeUnknown) || len(keys) != 1 {
		t.Fatal("lost preparation response was retried or not unknown")
	}

	_, err = backend.PreparePayment(t.Context(), request)

	if !errors.Is(err, payment.ErrOutcomeUnknown) || len(keys) != 2 {
		t.Fatal("explicit preparation recovery was not one attempt")
	}

	if keys[0] != "prepare-123" || keys[1] != keys[0] {
		t.Fatal("caller idempotency key changed")
	}

	if !reflect.DeepEqual(bodies[0], bodies[1]) {
		t.Fatal("application-controlled recovery changed the order body")
	}
}
