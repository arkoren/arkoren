package stripepayment_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/driver/stripe/stripepayment"
)

// preparationRequest supplies a server-owned attempt with no card details. Each
// test receives an immutable value, with optional construction edits for its case.
func preparationRequest(configure ...payment.PreparePaymentRequestBuilderFunc) payment.PreparePaymentRequest {
	policies := []payment.PreparePaymentRequestBuilderFunc{func(details *payment.PreparePaymentRequestBuilder) {
		details.Reference("order-123")
		details.Description("Order 123")
	}}
	policies = append(policies, configure...)

	return payment.NewPreparePaymentRequest(
		payment.Money{Amount: 2500, Currency: "USD"}, "mobile-order-123", policies...,
	)
}

// TestPreparePaymentSendsOnlyNativeInitializationRequest asserts the complete
// SDK form and absence of confirmation, customers, expansions, and extra reads.
func TestPreparePaymentSendsOnlyNativeInitializationRequest(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++
		expectRequest(t, request, http.MethodPost, "/v1/payment_intents")

		if request.Header.Get("Idempotency-Key") != "mobile-order-123" {
			t.Fatal("missing caller idempotency key")
		}

		if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatal("unexpected request encoding")
		}

		want := url.Values{
			"amount": {"2500"}, "currency": {"usd"}, "capture_method": {"automatic"},
			"confirmation_method": {"automatic"}, "allowed_payment_method_types[0]": {"card"},
			"metadata[reference]": {"order-123"}, "description": {"Order 123"},
		}

		if got := form(t, request); !reflect.DeepEqual(got, want) {
			t.Fatalf("form: %v; want %v", got, want)
		}

		return response(http.StatusOK, `{"id":"pi_native","object":"payment_intent","client_secret":"pi_native_secret_fixture"}`), nil
	})
	request := preparationRequest()
	before := request

	result, err := payment.New(backend).PreparePayment(t.Context(), request)

	if err != nil || result.PaymentID != "pi_native" {
		t.Fatalf("native initialization: %v; %v", result, err)
	}

	authorization, ok := result.Action.Authorization()

	if !ok || authorization.Protocol != payment.ClientProtocol("stripe.payment_intent") {
		t.Fatal("missing Stripe SDK authorization protocol")
	}

	if authorization.Token != "pi_native_secret_fixture" {
		t.Fatal("missing SDK initialization token")
	}

	if result.CheckoutID != "" || result.SessionID != "" {
		t.Fatal("invented checkout or session identity")
	}

	if attempts != 1 || request != before {
		t.Fatal("hydration, replay, or caller request mutation")
	}
}

// TestPreparePaymentZeroDecimalAndEmptyOptionalText preserves minor units
// for JPY and omits empty text rather than sending empty metadata or descriptions.
func TestPreparePaymentZeroDecimalAndEmptyOptionalText(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		want := url.Values{
			"amount": {"1000"}, "currency": {"jpy"}, "capture_method": {"automatic"},
			"confirmation_method": {"automatic"}, "allowed_payment_method_types[0]": {"card"},
		}

		if got := form(t, request); !reflect.DeepEqual(got, want) {
			t.Fatalf("zero-decimal form: %v", got)
		}

		return response(http.StatusOK, `{"id":"pi_native","object":"payment_intent","client_secret":"pi_native_secret_fixture"}`), nil
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 1000, Currency: "JPY"}, "mobile-jpy-123",
	))

	if err != nil {
		t.Fatal(err)
	}
}

// TestPreparationJSONIntentionallyIncludesAuthorizedSDKSecret verifies the common
// action's authorized wire payload carries the selected Stripe SDK token.
func TestPreparationJSONIntentionallyIncludesAuthorizedSDKSecret(t *testing.T) {
	t.Parallel()

	result := payment.Preparation{
		PaymentID: "pi_native",
		Action:    payment.NewClientAuthorization(payment.ClientProtocol("stripe.payment_intent"), "pi_native_secret_fixture"),
	}

	encoded, err := json.Marshal(result)

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(encoded, []byte(`"pi_native_secret_fixture"`)) {
		t.Fatal("authorized transport lost its required SDK initialization fields")
	}
}

// TestPreparationFormattingAndStructuredLoggingRedactSecret exercises values,
// pointers, containers, and both slog handlers without changing the result.
func TestPreparationFormattingAndStructuredLoggingRedactSecret(t *testing.T) {
	t.Parallel()

	result := payment.Preparation{
		PaymentID: "pi_native",
		Action:    payment.NewClientAuthorization(payment.ClientProtocol("stripe.payment_intent"), "pi_native_secret_fixture"),
	}

	for _, value := range []any{result, &result, []payment.Preparation{result}} {
		text := fmt.Sprintf("%v %+v %#v %s %q", value, value, value, value, value)

		if strings.Contains(text, "fixture") || !strings.Contains(text, "REDACTED") {
			t.Fatal("fmt leaked native initialization secret")
		}
	}

	var output bytes.Buffer
	slog.New(slog.NewJSONHandler(&output, nil)).InfoContext(t.Context(), "prepared", "payment", result, "pointer", &result)
	slog.New(slog.NewTextHandler(&output, nil)).InfoContext(t.Context(), "prepared", "payment", result)

	if strings.Contains(output.String(), "fixture") || !strings.Contains(output.String(), "REDACTED") {
		t.Fatal("slog leaked native initialization secret")
	}

	authorization, ok := result.Action.Authorization()

	if !ok || authorization.Token != "pi_native_secret_fixture" {
		t.Fatal("redaction mutated the result")
	}
}

// TestPreparationContextValidatedBeforeRequestAndHandle preserves cancellation
// precedence even when both the request and driver handle are invalid.
func TestPreparationContextValidatedBeforeRequestAndHandle(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var backend *stripepayment.Driver

	_, err := backend.PreparePayment(ctx, payment.PreparePaymentRequest{})

	if !errors.Is(err, context.Canceled) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("context precedence: %v", err)
	}
}

// TestPreparationNilContextNeverDispatches prevents a nil context from reaching
// the SDK while retaining the public payment invalid-request classification.
func TestPreparationNilContextNeverDispatches(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("nil context dispatched")

		return nil, errors.New("unexpected dispatch")
	})
	var ctx context.Context

	_, err := payment.New(backend).PreparePayment(ctx, preparationRequest())

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("nil context: %v", err)
	}
}

// TestPreparationCanceledContextNeverDispatches distinguishes pre-dispatch
// cancellation from an uncertain outcome after Stripe may have created an intent.
func TestPreparationCanceledContextNeverDispatches(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled context dispatched")

		return nil, errors.New("unexpected dispatch")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := payment.New(backend).PreparePayment(ctx, preparationRequest())

	if !errors.Is(err, context.Canceled) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("canceled context: %v", err)
	}
}

// TestPreparationRejectsUninitializedHandles checks nil and zero drivers plus
// a borrowed native client that lacks its required PaymentIntent service.
func TestPreparationRejectsUninitializedHandles(t *testing.T) {
	t.Parallel()

	var missing *stripepayment.Driver

	_, err := missing.PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("nil driver: %v", err)
	}

	_, err = new(stripepayment.Driver).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("zero driver: %v", err)
	}

	backend, err := stripepayment.NewFrom(&stripe.Client{})

	if err != nil {
		t.Fatal(err)
	}

	_, err = backend.PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("nil native PaymentIntents service: %v", err)
	}
}

// TestPreparationNonpositiveAmountNeverDispatches rejects both zero and negative
// monetary inputs before the first external side effect.
func TestPreparationNonpositiveAmountNeverDispatches(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid amount dispatched")

		return nil, errors.New("unexpected dispatch")
	})

	for _, amount := range []int64{0, -1} {
		request := payment.NewPreparePaymentRequest(payment.Money{Amount: amount, Currency: "USD"}, "order-123")

		_, err := payment.New(backend).PreparePayment(t.Context(), request)

		if !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("nonpositive amount: %v", err)
		}
	}
}

// TestPreparationUnsupportedCurrencyNeverDispatches requires the core uppercase
// currency vocabulary instead of silently normalizing caller-owned money.
func TestPreparationUnsupportedCurrencyNeverDispatches(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported currency dispatched")

		return nil, errors.New("unexpected dispatch")
	})

	for _, currency := range []string{"usd", "XXX", ""} {
		request := payment.NewPreparePaymentRequest(payment.Money{Amount: 2500, Currency: currency}, "order-123")

		_, err := payment.New(backend).PreparePayment(t.Context(), request)

		if !errors.Is(err, payment.ErrUnsupported) {
			t.Fatalf("unsupported currency: %v", err)
		}
	}
}

// TestPreparationInvalidKeyNeverDispatchesOrEchoesInput refuses missing, unsafe,
// and overlong mutation identities without exposing their values in errors.
func TestPreparationInvalidKeyNeverDispatchesOrEchoesInput(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid key dispatched")

		return nil, errors.New("unexpected dispatch")
	})

	for _, key := range []string{"", "sensitive\nvalue", "sensitive key", strings.Repeat("s", 39)} {
		request := payment.NewPreparePaymentRequest(payment.Money{Amount: 2500, Currency: "USD"}, key)

		_, err := payment.New(backend).PreparePayment(t.Context(), request)

		if !errors.Is(err, payment.ErrInvalidRequest) || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("key validation: %v", err)
		}
	}
}

// TestPreparationInvalidReferenceNeverDispatches refuses metadata containing
// controls, malformed UTF-8, or more than the documented Unicode rune limit.
func TestPreparationInvalidReferenceNeverDispatches(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid reference dispatched")

		return nil, errors.New("unexpected dispatch")
	})

	for _, reference := range []string{"sensitive\x00", "\xff", strings.Repeat("界", 128)} {
		request := preparationRequest(func(details *payment.PreparePaymentRequestBuilder) {
			details.Reference(reference)
		})

		_, err := payment.New(backend).PreparePayment(t.Context(), request)

		if !errors.Is(err, payment.ErrInvalidRequest) || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("reference validation: %v", err)
		}
	}
}

// TestPreparationInvalidDescriptionNeverDispatches applies the same safe text
// policy to Stripe-visible descriptions as to application correlation metadata.
func TestPreparationInvalidDescriptionNeverDispatches(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid description dispatched")

		return nil, errors.New("unexpected dispatch")
	})

	for _, description := range []string{"sensitive\n", "\xff", strings.Repeat("界", 128)} {
		request := preparationRequest(func(details *payment.PreparePaymentRequestBuilder) {
			details.Description(description)
		})

		_, err := payment.New(backend).PreparePayment(t.Context(), request)

		if !errors.Is(err, payment.ErrInvalidRequest) || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("description validation: %v", err)
		}
	}
}

// TestPreparationTransportFailureIsUnknownAndNeverRetried preserves trusted
// error identity but redacts transport text from all common fmt representations.
func TestPreparationTransportFailureIsUnknownAndNeverRetried(t *testing.T) {
	t.Parallel()

	attempts := 0
	cause := errors.New("pi_native_secret_sensitive")
	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		attempts++

		return nil, cause
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, cause) || attempts != 1 {
		t.Fatalf("transport semantics: %v; attempts=%d", err, attempts)
	}

	if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "sensitive") {
		t.Fatal("formatted error exposed transport text")
	}
}

// TestPreparationCancellationAfterDispatchRemainsUnknown keeps both cancellation
// and uncertain-write classifications when a dispatched request loses its result.
func TestPreparationCancellationAfterDispatchRemainsUnknown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	attempts := 0
	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		attempts++
		cancel()

		return nil, context.Canceled
	})

	_, err := payment.New(backend).PreparePayment(ctx, preparationRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("after-dispatch cancellation: %v; attempts=%d", err, attempts)
	}
}

// TestPreparationServerFailureNeverRetried treats a 503 as uncertain creation
// and prevents provider error text from becoming formatted application output.
func TestPreparationServerFailureNeverRetried(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		attempts++

		return response(http.StatusServiceUnavailable, `{"error":{"type":"api_error","message":"pi_native_secret_sensitive"}}`), nil
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || attempts != 1 {
		t.Fatalf("server failure: %v; attempts=%d", err, attempts)
	}

	if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "sensitive") {
		t.Fatal("formatted error exposed provider text")
	}
}

// TestPreparationDefinitiveAPIErrorPreservesIdentityNotText retains the native
// cause for trusted inspection without classifying a definitive 400 as unknown.
func TestPreparationDefinitiveAPIErrorPreservesIdentityNotText(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"pi_native_secret_sensitive"}}`), nil
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrInvalidRequest) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("definitive response: %v", err)
	}

	var native *stripe.Error

	if !errors.As(err, &native) {
		t.Fatal("lost native diagnostics for trusted inspection")
	}

	if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "sensitive") {
		t.Fatal("formatted error exposed provider text")
	}

	var output bytes.Buffer
	slog.New(slog.NewJSONHandler(&output, nil)).ErrorContext(t.Context(), "prepare failed", "error", err)

	if strings.Contains(output.String(), "sensitive") {
		t.Fatal("structured error logging exposed provider text")
	}
}

// TestPreparationConcurrentIdempotencyKeyIsUnknown preserves the possibility
// that another request with this key is already executing the same logical write.
// The native error remains inspectable but its secret-bearing text stays private.
func TestPreparationConcurrentIdempotencyKeyIsUnknown(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++
		expectRequest(t, request, http.MethodPost, "/v1/payment_intents")

		return response(http.StatusConflict, `{"error":{"type":"invalid_request_error","code":"idempotency_key_in_use","message":"pi_native_secret_sensitive"}}`), nil
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, payment.ErrInvalidState) {
		t.Fatalf("concurrent logical mutation: %v", err)
	}

	var native *stripe.Error

	if !errors.As(err, &native) || native.Code != stripe.ErrorCodeIdempotencyKeyInUse {
		t.Fatal("lost native concurrent-idempotency diagnostics")
	}

	if attempts != 1 {
		t.Fatalf("concurrent mutation replayed %d times", attempts)
	}

	if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "sensitive") {
		t.Fatal("formatted concurrent-idempotency error exposed provider text")
	}

	var output bytes.Buffer
	slog.New(slog.NewJSONHandler(&output, nil)).ErrorContext(t.Context(), "prepare failed", "error", err)

	if strings.Contains(output.String(), "sensitive") {
		t.Fatal("structured concurrent-idempotency error exposed provider text")
	}
}

// TestPreparationConcurrentIdempotencyBadRequestIsUnknown does not assume Stripe
// always uses 409 for idempotency_key_in_use. The native code, not its 4xx status,
// preserves the possibility that the same logical mutation is still executing.
func TestPreparationConcurrentIdempotencyBadRequestIsUnknown(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		attempts++

		return response(http.StatusBadRequest, `{"error":{"type":"invalid_request_error","code":"idempotency_key_in_use","message":"pi_native_secret_sensitive"}}`), nil
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("concurrent logical mutation: %v", err)
	}

	if attempts != 1 || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "sensitive") {
		t.Fatal("concurrent mutation replayed or exposed provider text")
	}
}

// TestPaymentReadConcurrentIdempotencyKeyDoesNotClaimUnknown keeps read errors
// outside mutation-outcome semantics, even if Stripe supplies this conflict code.
func TestPaymentReadConcurrentIdempotencyKeyDoesNotClaimUnknown(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++
		expectRequest(t, request, http.MethodGet, "/v1/payment_intents/pi_native")

		return response(http.StatusConflict, `{"error":{"type":"invalid_request_error","code":"idempotency_key_in_use","message":"pi_native_secret_sensitive"}}`), nil
	})

	_, err := backend.GetPayment(t.Context(), "pi_native")

	if !errors.Is(err, payment.ErrInvalidState) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("read conflict classification: %v", err)
	}

	var native *stripe.Error

	if !errors.As(err, &native) || native.Code != stripe.ErrorCodeIdempotencyKeyInUse {
		t.Fatal("lost native read-conflict diagnostics")
	}

	if attempts != 1 || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "sensitive") {
		t.Fatal("read conflict replayed or exposed provider text")
	}
}

// TestPreparationOtherConflictRemainsDefinitive limits uncertain 4xx handling
// to the in-progress logical-mutation code, not unrelated state conflicts.
func TestPreparationOtherConflictRemainsDefinitive(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusConflict, `{"error":{"type":"invalid_request_error","message":"pi_native_secret_sensitive"}}`), nil
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrInvalidState) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("unrelated state conflict: %v", err)
	}
}

// TestPreparationMalformedSuccessIsUnknown rejects a truncated success body
// without disclosing its embedded secret or replaying the mutation.
func TestPreparationMalformedSuccessIsUnknown(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		attempts++

		return response(http.StatusOK, `{"client_secret":"pi_native_secret_sensitive",`), nil
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || attempts != 1 {
		t.Fatalf("malformed response: %v; attempts=%d", err, attempts)
	}

	if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "sensitive") {
		t.Fatal("formatted error exposed response body")
	}
}

// TestPreparationResponseReadFailureNeverRetried preserves uncertainty when
// response I/O fails after the server may already have committed the intent.
func TestPreparationResponseReadFailureNeverRetried(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		attempts++
		result := response(http.StatusOK, "")
		result.Body = brokenBody{}

		return result, nil
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || attempts != 1 {
		t.Fatalf("response read failure: %v; attempts=%d", err, attempts)
	}
}

// TestPreparationEmptySuccessIsUnknown refuses successful bodies lacking the
// essential initialization fields instead of returning an unusable SDK result.
func TestPreparationEmptySuccessIsUnknown(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"null", "{}", `{"id":"pi_native","object":"payment_intent"}`} {
		backend := newBackend(t, func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, body), nil
		})

		result, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

		if !errors.Is(err, payment.ErrOutcomeUnknown) || result != (payment.Preparation{}) {
			t.Fatalf("empty response: %v; %v", result, err)
		}
	}
}

// TestPreparationAcceptsUnicodeTextAtLimitWithoutNormalization checks the rune
// boundary rather than a byte limit and keeps caller-owned text unchanged.
func TestPreparationAcceptsUnicodeTextAtLimitWithoutNormalization(t *testing.T) {
	t.Parallel()

	request := preparationRequest(func(details *payment.PreparePaymentRequestBuilder) {
		details.Reference(strings.Repeat("界", 127))
		details.Description("Native payment – 商品")
	})
	before := request

	if err := request.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}

	if request != before {
		t.Fatal("validation mutated request text")
	}
}

// TestPreparationInvalidNativeIdentityIsUnknown rejects empty, unrelated, and
// unsafe resource IDs while returning no partial initialization data.
func TestPreparationInvalidNativeIdentityIsUnknown(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"", "pi_", "cs_other", "pi_bad/path"} {
		backend := newBackend(t, func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, fmt.Sprintf(`{"id":%q,"object":"payment_intent","client_secret":"pi_native_secret_sensitive"}`, id)), nil
		})

		result, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

		if !errors.Is(err, payment.ErrOutcomeUnknown) || result != (payment.Preparation{}) {
			t.Fatalf("invalid native identity: %v; %v", result, err)
		}
	}
}

// TestPreparationWrongNativeObjectIsUnknown requires a payment_intent object
// even when the other initialization fields have apparently valid shapes.
func TestPreparationWrongNativeObjectIsUnknown(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"id":"pi_native","object":"checkout.session","client_secret":"pi_native_secret_sensitive"}`), nil
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("wrong object: %v", err)
	}
}

// TestPreparationInvalidClientSecretIsUnknownAndNeverExposed requires a nonempty
// secret belonging to this intent and rejects whitespace without echoing input.
func TestPreparationInvalidClientSecretIsUnknownAndNeverExposed(t *testing.T) {
	t.Parallel()

	for _, secret := range []string{"", "pi_other_secret_sensitive", "pi_native_secret_", "pi_native_secret_sensitive\n"} {
		backend := newBackend(t, func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, fmt.Sprintf(`{"id":"pi_native","object":"payment_intent","client_secret":%q}`, secret)), nil
		})

		result, err := payment.New(backend).PreparePayment(t.Context(), preparationRequest())

		if !errors.Is(err, payment.ErrOutcomeUnknown) || result != (payment.Preparation{}) {
			t.Fatalf("invalid SDK secret: %v; %v", result, err)
		}

		if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "sensitive") {
			t.Fatal("invalid client secret leaked through errors")
		}
	}
}

// TestPreparationSupportedChannelsKeepGuestIntentProtocol verifies that client
// platform hints never broaden methods, confirm the intent, or create customers.
func TestPreparationSupportedChannelsKeepGuestIntentProtocol(t *testing.T) {
	t.Parallel()

	for _, channel := range []payment.ClientChannel{
		"", payment.ClientChannelWeb, payment.ClientChannelIOS, payment.ClientChannelAndroid,
	} {
		attempts := 0
		backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
			attempts++
			expectRequest(t, request, http.MethodPost, "/v1/payment_intents")
			want := url.Values{
				"amount": {"2500"}, "currency": {"usd"}, "capture_method": {"automatic"},
				"confirmation_method": {"automatic"}, "allowed_payment_method_types[0]": {"card"},
			}

			if got := form(t, request); !reflect.DeepEqual(got, want) {
				t.Fatalf("channel changed initialization protocol: %v", got)
			}

			return response(http.StatusOK, `{"id":"pi_native","object":"payment_intent","client_secret":"pi_native_secret_fixture"}`), nil
		})
		request := payment.NewPreparePaymentRequest(payment.Money{Amount: 2500, Currency: "USD"}, "order-123",
			func(details *payment.PreparePaymentRequestBuilder) {
				details.Channel(channel)
			},
		)

		_, err := payment.New(backend).PreparePayment(t.Context(), request)

		if err != nil || attempts != 1 {
			t.Fatalf("supported channel: %v; attempts=%d", err, attempts)
		}
	}
}

// TestPreparationUnknownChannelNeverDispatches refuses unrecognized client
// protocol hints instead of silently accepting a profile it does not support.
func TestPreparationUnknownChannelNeverDispatches(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported channel dispatched")

		return nil, errors.New("unexpected dispatch")
	})
	request := preparationRequest(func(details *payment.PreparePaymentRequestBuilder) {
		details.Channel(payment.ClientChannel("unknown"))
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), request)

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("invalid channel: %v", err)
	}
}

// TestPreparationReturnURLNeverDispatches ensures a client-only return URL is
// rejected, not silently discarded as if Stripe consumed it for this mutation.
func TestPreparationReturnURLNeverDispatches(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported return URL dispatched")

		return nil, errors.New("unexpected dispatch")
	})
	request := preparationRequest(func(details *payment.PreparePaymentRequestBuilder) {
		details.ReturnURL("https://shop.example/return")
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), request)

	if !errors.Is(err, payment.ErrUnsupported) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("unsupported return URL: %v", err)
	}
}

// TestPreparationDeviceReferenceNeverDispatches prevents dropping application
// risk data without telling the caller that the selected profile cannot use it.
func TestPreparationDeviceReferenceNeverDispatches(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported device reference dispatched")

		return nil, errors.New("unexpected dispatch")
	})
	request := preparationRequest(func(details *payment.PreparePaymentRequestBuilder) {
		details.DeviceReference("device-risk-123")
	})

	_, err := payment.New(backend).PreparePayment(t.Context(), request)

	if !errors.Is(err, payment.ErrUnsupported) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("unsupported device reference: %v", err)
	}
}

// TestPreparationFacadeRecordsCommonOperationWithoutSecrets exercises the real
// driver behind payment.Client. Instrumentation records only the common operation
// and outcome; SDK initialization data never becomes a span or metric attribute.
func TestPreparationFacadeRecordsCommonOperationWithoutSecrets(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"id":"pi_native","object":"payment_intent","client_secret":"pi_native_secret_fixture"}`), nil
	})
	observation := telemetrytest.New()
	client := payment.New(backend, func(checkout *payment.Builder) {
		checkout.Telemetry(observation.Telemetry(t))
	})

	if _, err := client.PreparePayment(t.Context(), preparationRequest()); err != nil {
		t.Fatal(err)
	}

	spans := observation.Spans()

	if len(spans) != 1 {
		t.Fatalf("preparation spans = %d, want 1", len(spans))
	}

	if spans[0].Name != "payment prepare_payment" {
		t.Fatalf("not a common preparation operation: %q", spans[0].Name)
	}

	snapshot := fmt.Sprint(spans, observation.Metrics())

	for _, secret := range []string{"fixture", "pi_native", "order-123", "Order 123", "2500", "USD"} {
		if strings.Contains(snapshot, secret) {
			t.Fatal("preparation telemetry disclosed request or authorization data")
		}
	}
}
