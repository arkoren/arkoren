package molliepayment_test

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestCreateCheckoutRestrictsWireScope verifies the hosted card allowlist and
// explicit safe sequence/capture settings, including the required description.
func TestCreateCheckoutRestrictsWireScope(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.mollie.com/v2/payments" || request.Method != http.MethodPost {
			t.Fatal("unexpected create endpoint")
		}

		if request.Header.Get("Authorization") != "Bearer test_example" {
			t.Fatal("missing profile authentication")
		}

		if request.GetBody != nil {
			t.Fatal("POST must not be replayable by net/http")
		}

		var body map[string]any

		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			t.Fatal(err)
		}

		expected := map[string]any{"amount": map[string]any{"currency": "USD", "value": "12.00"},
			"description": "Payment", "redirectUrl": "https://shop.example/paid", "method": "creditcard",
			"sequenceType": "oneoff", "captureMode": "automatic", "metadata": map[string]any{"reference": ""}}

		if !reflect.DeepEqual(body, expected) {
			t.Fatalf("unexpected checkout wire fields: %v", body)
		}

		return response(http.StatusCreated, `{"id":"tr_example","status":"open","amount":{"currency":"USD","value":"12.00"},"_links":{"checkout":{"href":"https://www.mollie.com/pay/example"}}}`), nil
	})

	_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}
}

// TestCheckoutUsesResponseAmount proves a mutation never substitutes request
// amount/reference into a snapshot or hydrates the result with a second call.
func TestCheckoutUsesResponseAmount(t *testing.T) {
	t.Parallel()

	calls := 0
	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		calls++

		return response(201, `{"id":"tr_example","status":"pending","amount":{"currency":"EUR","value":"0.00"},"metadata":{"reference":"observed"}}`), nil
	})

	checkout, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if err != nil {
		t.Fatal(err)
	}

	if checkout.Amount == nil {
		t.Fatal("snapshot lost its response amount")
	}

	if calls != 1 || checkout.Amount.Amount != 0 || checkout.Amount.Currency != "EUR" {
		t.Fatal("snapshot did not preserve response-only zero amount")
	}

	if checkout.Reference != "observed" || checkout.PaymentStatus != payment.StatusPending {
		t.Fatal("snapshot synthesized reference or status")
	}
}

// TestCheckoutMissingResponseFieldsIsUnknown covers incomplete success
// documents that cannot establish a usable authoritative mutation result.
func TestCheckoutMissingResponseFieldsIsUnknown(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{}`, `{"id":"tr_example","status":"pending"}`,
		`{"id":"tr_example","amount":{"currency":"EUR","value":"12.00"}}`,
		`{"id":"tr_example","status":"open","amount":{"currency":"EUR","value":"12.00"}}`} {
		driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
			return response(201, body), nil
		})

		_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

		if !errors.Is(err, payment.ErrOutcomeUnknown) {
			t.Fatalf("incomplete success must be unknown: %v", err)
		}
	}
}

// TestCallerRetryKeepsKeyAndBody verifies no retry is automatic and the caller
// can deliberately repeat exactly one persisted operation with the same key.
func TestCallerRetryKeepsKeyAndBody(t *testing.T) {
	t.Parallel()

	var keys []string
	var bodies []string
	driver := newDriver(t, func(request *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(request.Body)

		if err != nil {
			t.Fatal(err)
		}

		keys = append(keys, request.Header.Get("Idempotency-Key"))
		bodies = append(bodies, string(data))

		return nil, io.ErrUnexpectedEOF
	})

	_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || len(keys) != 1 {
		t.Fatal("lost response was retried or not marked unknown")
	}

	_, err = driver.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || len(keys) != 2 {
		t.Fatal("explicit retry was not one request")
	}

	if keys[0] != "order-123" || keys[1] != keys[0] || bodies[0] != bodies[1] {
		t.Fatal("retry changed key or operation body")
	}
}

// TestCheckoutRejectsUnsafeNativeIDs checks resource-family, separator and
// size validation before credentials or network calls can be dispatched.
func TestCheckoutRejectsUnsafeNativeIDs(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		t.Fatal("invalid ID dispatched HTTP")

		return nil, io.EOF
	})

	for _, id := range []payment.CheckoutID{"re_example", "tr_", "tr_a/b", "tr_a?x=y", "tr_a%2fb", "tr_a:re_b"} {
		_, err := driver.GetCheckout(t.Context(), id)

		if !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("invalid ID error = %v", err)
		}
	}
}

// TestCheckoutRejectsUntrustedApprovalDestinations prevents malformed success
// responses from returning credential-bearing, non-HTTPS or relative links.
func TestCheckoutRejectsUntrustedApprovalDestinations(t *testing.T) {
	t.Parallel()

	for _, destination := range []string{"http://mollie.example/pay", "https://secret@mollie.example/pay", "/pay"} {
		driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
			body := map[string]any{"id": "tr_example", "status": "open",
				"amount": map[string]string{"currency": "EUR", "value": "12.00"},
				"_links": map[string]any{"checkout": map[string]string{"href": destination}}}

			data, err := json.Marshal(body)

			if err != nil {
				t.Fatal(err)
			}

			return response(201, string(data)), nil
		})

		_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

		if !errors.Is(err, payment.ErrOutcomeUnknown) {
			t.Fatalf("invalid approval destination error = %v", err)
		}
	}
}

// TestUnknownCheckoutStatusRemainsUnknown keeps future native lifecycle states
// from implying either completed approval or successful money movement.
func TestUnknownCheckoutStatusRemainsUnknown(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(200, `{"id":"tr_example","status":"future_state","amount":{"currency":"EUR","value":"0.00"}}`), nil
	})

	checkout, err := driver.GetCheckout(t.Context(), "tr_example")

	if err != nil {
		t.Fatal(err)
	}

	if checkout.Status != payment.CheckoutStatusUnknown || checkout.PaymentStatus != payment.StatusUnknown {
		t.Fatal("future checkout status did not stay unknown")
	}

	if checkout.NativeStatus != "future_state" || checkout.NeedsCompletion {
		t.Fatal("unknown checkout status was rewritten or requires unsupported completion")
	}
}

// TestReadCheckoutMissingApprovalIsUnavailable classifies a valid JSON response
// that cannot supply its required open-checkout handoff as a provider failure.
func TestReadCheckoutMissingApprovalIsUnavailable(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK,
			`{"id":"tr_example","status":"open","amount":{"currency":"EUR","value":"12.00"}}`), nil
	})

	_, err := driver.GetCheckout(t.Context(), "tr_example")

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("missing approval classification = %v", err)
	}
}
