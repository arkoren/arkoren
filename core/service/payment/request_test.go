package payment_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestCheckoutUnsupportedCurrencyPreservesIdentity characterizes the existing
// request, dispatch-preflight, and encoding contract without changing money policy.
func TestCheckoutUnsupportedCurrencyPreservesIdentity(t *testing.T) {
	t.Parallel()

	request := payment.NewCheckoutRequest(payment.Money{Amount: 100, Currency: "XYZ"},
		"https://shop.example/paid", "order-unsupported")

	_, err := json.Marshal(request)

	for _, failure := range []error{request.Err(), request.Validate(t.Context()), err} {
		if !errors.Is(failure, payment.ErrUnsupported) {
			t.Fatalf("currency failure = %v, want ErrUnsupported", failure)
		}

		if errors.Is(failure, payment.ErrInvalidRequest) {
			t.Fatal("unsupported currency was reclassified as invalid request")
		}
	}
}

func TestRequestValidationRejectsNilContext(t *testing.T) {
	t.Parallel()

	var ctx context.Context

	for _, request := range []interface{ Validate(context.Context) error }{
		payment.PreparePaymentRequest{}, payment.SessionID("session-1"),
		payment.CheckoutRequest{}, payment.SubscriptionCheckoutRequest{}, payment.CompleteCheckoutRequest{},
		payment.RefundRequest{}, payment.CancelSubscriptionRequest{}, payment.CheckoutID("cs_123"),
		payment.ID("pi_123"), payment.RefundID("re_123"), payment.SubscriptionID("sub_123"),
	} {
		if err := request.Validate(ctx); !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("%T Validate(nil) = %v", request, err)
		}
	}
}

func TestRequestValidationPreservesCancellationBeforeInputErrors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for _, request := range []interface{ Validate(context.Context) error }{
		payment.PreparePaymentRequest{}, payment.SessionID(""),
		payment.CheckoutRequest{}, payment.SubscriptionCheckoutRequest{}, payment.CompleteCheckoutRequest{},
		payment.RefundRequest{}, payment.CancelSubscriptionRequest{}, payment.CheckoutID(""),
		payment.ID(""), payment.RefundID(""), payment.SubscriptionID(""),
	} {
		if err := request.Validate(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("%T Validate(canceled) = %v", request, err)
		}
	}
}

func TestRequestCancellationRetainsContextCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("caller canceled checkout")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)

	err := (payment.CheckoutRequest{}).Validate(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate = %v, want context.Canceled", err)
	}

	if !errors.Is(context.Cause(ctx), cause) {
		t.Fatalf("context cause = %v", context.Cause(ctx))
	}
}

func TestCheckoutAcceptsLiteralSessionPlaceholder(t *testing.T) {
	t.Parallel()

	request := payment.NewCheckoutRequest(payment.Money{Amount: 1, Currency: "USD"},
		"https://shop.example/paid?checkout={CHECKOUT_SESSION_ID}", "8a72151e-c161-4a7e-873f-36a8fe2f706c")

	if err := request.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}

	if request.ReturnURL() != "https://shop.example/paid?checkout={CHECKOUT_SESSION_ID}" {
		t.Fatalf("mutated return URL: %q", request.ReturnURL())
	}
}

func TestCheckoutAcceptsLocalDevelopmentRedirects(t *testing.T) {
	t.Parallel()

	for _, location := range []string{"http://localhost:8080/paid", "http://127.0.0.1/paid", "http://[::1]/paid"} {
		request := payment.NewCheckoutRequest(payment.Money{Amount: 1, Currency: "USD"}, location, "order-123",
			func(details *payment.CheckoutRequestBuilder) {
				details.CancelURL(location)
			})

		if err := request.Validate(t.Context()); err != nil {
			t.Fatalf("Validate(%q) = %v", location, err)
		}
	}
}

func TestCheckoutRejectsUnsafeRedirects(t *testing.T) {
	t.Parallel()

	for _, location := range []string{"", "/paid", "//shop.example/paid", "http://shop.example/paid",
		"https://user:password@shop.example/paid", "https://shop.example/paid#fragment",
		"https://shop.example/paid#", "https://shop.example/\npaid", "https://shop.example/%0Apaid",
		"https://shop.example/paid?value=%00", "http://127.0.0.1.example/paid", "javascript:alert(1)"} {
		request := payment.NewCheckoutRequest(payment.Money{Amount: 1, Currency: "USD"}, location, "order-123")

		if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("Validate(%q) = %v", location, err)
		}
	}
}

func TestCheckoutRejectsUnsafeCancelRedirect(t *testing.T) {
	t.Parallel()

	request := payment.NewCheckoutRequest(payment.Money{Amount: 1, Currency: "USD"}, "https://shop.example/paid", "order-123",
		func(details *payment.CheckoutRequestBuilder) {
			details.CancelURL("http://shop.example/cart")
		})

	if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestCheckoutRejectsEncodedRedirectControls(t *testing.T) {
	t.Parallel()

	for _, control := range []string{"%00", "%09", "%0a", "%0D%0a", "%7f", "%C2%85"} {
		request := payment.NewCheckoutRequest(payment.Money{Amount: 1, Currency: "USD"},
			"https://shop.example/paid?value="+control, "order-123")

		if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("Validate(%q) = %v", control, err)
		}
	}
}

func TestCheckoutRejectsNonpositiveAmounts(t *testing.T) {
	t.Parallel()

	for _, amount := range []int64{0, -1} {
		request := payment.NewCheckoutRequest(payment.Money{Amount: amount, Currency: "USD"}, "https://shop.example/paid", "order-123")

		if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("Validate(%d) = %v", amount, err)
		}
	}
}

func TestCheckoutRejectsInvalidIdempotencyKeys(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"", "a/b", "space key", "é", strings.Repeat("a", 39)} {
		request := payment.NewCheckoutRequest(payment.Money{Amount: 1, Currency: "USD"}, "https://shop.example/paid", key)

		if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("Validate(key %q) = %v", key, err)
		}
	}
}

func TestCheckoutTextLimitsCountRunes(t *testing.T) {
	t.Parallel()

	var retained *payment.CheckoutRequestBuilder
	request := payment.NewCheckoutRequest(payment.Money{Amount: 1, Currency: "USD"},
		"https://shop.example/paid", strings.Repeat("a", 38), func(details *payment.CheckoutRequestBuilder) {
			retained = details
			details.Description(strings.Repeat("é", 127)).Reference(strings.Repeat("é", 127))
		})

	if err := request.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}

	retained.Description(strings.Repeat("é", 128))

	if err := request.Err(); err != nil {
		t.Fatalf("retained draft changed publication: %v", err)
	}

	request = payment.NewCheckoutRequest(request.Amount(), request.ReturnURL(), request.IdempotencyKey(),
		func(details *payment.CheckoutRequestBuilder) {
			details.Description(strings.Repeat("é", 128)).Description("valid replacement")
		})

	if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestCheckoutRejectsOverlongReference(t *testing.T) {
	t.Parallel()

	request := payment.NewCheckoutRequest(payment.Money{Amount: 1, Currency: "USD"}, "https://shop.example/paid", "order-123",
		func(details *payment.CheckoutRequestBuilder) {
			details.Reference(strings.Repeat("é", 128))
		})

	if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestSubscriptionCheckoutValidatesPlanAndRedirects(t *testing.T) {
	t.Parallel()

	request := payment.NewSubscriptionCheckoutRequest("price_monthly", "https://shop.example/account", "signup-123")

	if err := request.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}

	request = payment.NewSubscriptionCheckoutRequest("", request.ReturnURL(), request.IdempotencyKey())

	if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("Validate = %v", err)
	}
}

// TestCheckoutRequestCodec preserves the previous hosted JSON shape while
// round-tripping through the immutable private payload.
func TestCheckoutRequestCodec(t *testing.T) {
	t.Parallel()

	request := payment.NewCheckoutRequest(payment.Money{Amount: 1200, Currency: "USD"}, "https://shop.example/paid", "order-1",
		func(details *payment.CheckoutRequestBuilder) {
			details.Description("Book").CancelURL("https://shop.example/cart").Reference("order-1")
		})

	checkRequestCodec(t, request, `{"amount":{"amount":1200,"currency":"USD"},"description":"Book","return_url":"https://shop.example/paid","cancel_url":"https://shop.example/cart","reference":"order-1","idempotency_key":"order-1"}`)
}

// TestSubscriptionCheckoutRequestCodec preserves existing subscription tags and
// keeps optional hosted values after an immutable request round trip.
func TestSubscriptionCheckoutRequestCodec(t *testing.T) {
	t.Parallel()

	request := payment.NewSubscriptionCheckoutRequest("plan-1", "https://shop.example/account", "signup-1",
		func(details *payment.SubscriptionCheckoutRequestBuilder) {
			details.CancelURL("https://shop.example/plans").Reference("signup-1")
		})

	checkRequestCodec(t, request, `{"plan_reference":"plan-1","return_url":"https://shop.example/account","cancel_url":"https://shop.example/plans","reference":"signup-1","idempotency_key":"signup-1"}`)
}

// checkRequestCodec asserts exact wire compatibility, round-trip stability, and
// atomic replacement on invalid or malformed input for every request family.
func checkRequestCodec[Request any](t *testing.T, request Request, expected string) {
	t.Helper()

	encoded, err := json.Marshal(request)

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != expected {
		t.Fatalf("wire = %s", encoded)
	}

	var decoded Request

	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	reencoded, err := json.Marshal(decoded)

	if err != nil {
		t.Fatal(err)
	}

	if string(reencoded) != expected {
		t.Fatal("request round trip changed payload")
	}

	for _, invalid := range []string{`{}`, `null`, `{"broken":`} {
		if err := json.Unmarshal([]byte(invalid), &decoded); err == nil {
			t.Fatal("invalid request decoded")
		}

		reencoded, err := json.Marshal(decoded)

		if err != nil {
			t.Fatal(err)
		}

		if string(reencoded) != expected {
			t.Fatal("failed decode replaced request")
		}
	}
}

// TestRequestZeroValuesCannotEncode keeps private empty payloads from silently
// becoming apparently valid public JSON requests.
func TestRequestZeroValuesCannotEncode(t *testing.T) {
	t.Parallel()

	for _, request := range []any{payment.CheckoutRequest{}, payment.SubscriptionCheckoutRequest{},
		payment.CompleteCheckoutRequest{}, payment.RefundRequest{}, payment.CancelSubscriptionRequest{},
		payment.PreparePaymentRequest{}} {
		if _, err := json.Marshal(request); !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("%T encoded invalid zero request: %v", request, err)
		}
	}
}

// TestStickyRequestErrorPreventsEncoding checks that replacing invalid text
// cannot hide construction errors through the private JSON payload.
func TestStickyRequestErrorPreventsEncoding(t *testing.T) {
	t.Parallel()

	request := payment.NewCheckoutRequest(payment.Money{Amount: 100, Currency: "USD"}, "https://shop.example/paid", "order-1",
		func(details *payment.CheckoutRequestBuilder) {
			details.Description("invalid\n").Description("valid")
		})

	if _, err := json.Marshal(request); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("sticky request encoded")
	}

	if err := json.Unmarshal([]byte(`{"amount":{"amount":100,"currency":"USD"},"return_url":"https://shop.example/paid","idempotency_key":"order-2"}`), &request); err != nil {
		t.Fatal(err)
	}

	if request.Description() != "" || request.Err() != nil {
		t.Fatal("successful decode retained old state")
	}
}

// TestHostedCheckoutStillRejectsNativeCallbacks prevents native preparation
// support from weakening the hosted return and cancellation redirect policy.
func TestHostedCheckoutStillRejectsNativeCallbacks(t *testing.T) {
	t.Parallel()

	request := payment.NewCheckoutRequest(payment.Money{Amount: 100, Currency: "USD"}, "myapp:/payment", "order-1")

	if !errors.Is(request.Err(), payment.ErrInvalidRequest) {
		t.Fatal("hosted native callback accepted")
	}
}

// TestSubscriptionDraftDetachesAndKeepsFirstError checks optional subscription
// edits without introducing a mutable published request or a repair escape hatch.
func TestSubscriptionDraftDetachesAndKeepsFirstError(t *testing.T) {
	t.Parallel()

	var retained *payment.SubscriptionCheckoutRequestBuilder
	calls := 0
	request := payment.NewSubscriptionCheckoutRequest("plan-1", "https://shop.example/account", "signup-1",
		func(details *payment.SubscriptionCheckoutRequestBuilder) {
			calls++
			retained = details
			details.Reference("first")
		}, func(details *payment.SubscriptionCheckoutRequestBuilder) {
			calls++

			if retained != details {
				t.Fatal("subscription callbacks used different drafts")
			}

			details.Reference("second").CancelURL("https://shop.example/plans")
		})
	retained.Reference("changed").CancelURL("javascript:alert(1)")

	if calls != 2 || request.Reference() != "second" {
		t.Fatal("subscription callback order or ownership changed")
	}

	if request.CancelURL() != "https://shop.example/plans" || request.Err() != nil {
		t.Fatal("draft changed published subscription")
	}

	invalid := payment.NewSubscriptionCheckoutRequest("plan-1", "https://shop.example/account", "signup-2",
		func(details *payment.SubscriptionCheckoutRequestBuilder) {
			details.CancelURL("javascript:alert(1)").CancelURL("")
			details.Reference("bad\n").Reference("valid")
		})

	if !errors.Is(invalid.Err(), payment.ErrInvalidRequest) {
		t.Fatal("subscription errors repaired")
	}
}
