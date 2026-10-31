package stripepayment_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
)

// checkoutRequest is a valid baseline request for failure-path tests. Every test
// owns its copy, and no remote account or credentials are involved.
func checkoutRequest() payment.CheckoutRequest {
	return payment.NewCheckoutRequest(
		payment.Money{Amount: 2500, Currency: "USD"}, "https://shop.example/paid", "order-123",
	)
}

func TestMutation503IsUnknownAndNeverRetried(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		attempts++

		return response(http.StatusServiceUnavailable, `{"error":{"type":"api_error","message":"sensitive fixture"}}`), nil
	})

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, payment.ErrUnavailable) {
		t.Fatalf("503 classification: %v", err)
	}

	if attempts != 1 || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe replay or text: attempts=%d; %v", attempts, err)
	}

	var native *stripe.Error

	if !errors.As(err, &native) || native.HTTPStatusCode != http.StatusServiceUnavailable {
		t.Fatalf("lost Stripe diagnostic cause: %v", err)
	}
}

// brokenBody fails after dispatch; Stripe may have applied the mutation already.
type brokenBody struct{}

// Read simulates losing a successful response body on the wire.
func (brokenBody) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

// Close releases no resources in this deterministic body substitute.
func (brokenBody) Close() error {
	return nil
}

func TestMutationBodyReadFailureNeverRetried(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		attempts++
		result := response(http.StatusOK, "")
		result.Body = brokenBody{}

		return result, nil
	})

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || attempts != 1 {
		t.Fatalf("body failure: %v; attempts=%d", err, attempts)
	}
}

func TestMutationMalformedSuccessIsUnknown(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"id":"cs_native","object":"wrong_resource"}`), nil
	})

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("malformed success: %v", err)
	}
}

func TestMutationDeclinedPreservesNativeErrorWithoutText(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusPaymentRequired, `{"error":{"type":"card_error","code":"card_declined","message":"secret fixture https://private.example","decline_code":"insufficient_funds"}}`), nil
	})

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrDeclined) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("decline classification: %v", err)
	}

	var native *stripe.Error

	if !errors.As(err, &native) || native.DeclineCode != stripe.DeclineCodeInsufficientFunds {
		t.Fatalf("lost decline diagnostics: %v", err)
	}

	if strings.Contains(err.Error(), "fixture") || strings.Contains(err.Error(), "https://") {
		t.Fatalf("unsafe error text: %v", err)
	}
}

func TestReadNotFoundMappedWithoutOutcomeUnknown(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusNotFound, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"sensitive fixture"}}`), nil
	})

	_, err := backend.GetPayment(t.Context(), "pi_missing")

	if !errors.Is(err, payment.ErrNotFound) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("missing payment: %v", err)
	}

	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("provider message leaked: %v", err)
	}
}

func TestCanceledBeforeDispatchDoesNotMutate(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("pre-canceled operation dispatched")

		return nil, context.Canceled
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := backend.CreateCheckout(ctx, checkoutRequest())

	if !errors.Is(err, context.Canceled) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("pre-dispatch cancellation: %v", err)
	}
}

func TestCancellationAfterDispatchPreservesUnknownAndContext(t *testing.T) {
	t.Parallel()

	// marker distinguishes request context propagation from timeout wrapping.
	type marker struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), marker{}, "request-context"))
	t.Cleanup(cancel)
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		if request.Context().Value(marker{}) != "request-context" {
			t.Fatal("request context value was lost")
		}

		cancel()

		return nil, context.Canceled
	})

	_, err := backend.CreateCheckout(ctx, checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-dispatch cancellation: %v", err)
	}
}

func TestInvalidRequestsNeverReachStripe(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached Stripe")

		return nil, errors.New("unexpected request")
	})

	_, err := backend.CreateCheckout(t.Context(), payment.CheckoutRequest{})

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("empty checkout: %v", err)
	}

	_, err = backend.CreateSubscriptionCheckout(t.Context(), payment.NewSubscriptionCheckoutRequest(
		"plan_not_a_price", "https://shop.example/paid", "signup-123",
	))

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("not a price: %v", err)
	}

	_, err = backend.RefundPayment(t.Context(), payment.NewRefundRequest(
		"cs_not_a_payment", payment.Money{Amount: 500, Currency: "USD"}, "refund-123",
	))

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("not a PaymentIntent: %v", err)
	}
}

func TestUnrelatedIDFamiliesNeverReachStripe(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unrelated identity reached Stripe")

		return nil, errors.New("unexpected request")
	})

	_, err := backend.GetCheckout(t.Context(), "pi_wrong")

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("checkout family: %v", err)
	}

	_, err = backend.GetRefund(t.Context(), "pi_wrong")

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("refund family: %v", err)
	}

	_, err = backend.GetSubscription(t.Context(), "cs_wrong")

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("subscription family: %v", err)
	}

	_, err = backend.GetPayment(t.Context(), "pi_unsafe/path")

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("unsafe path: %v", err)
	}
}

func TestRefund503NeverReplaysAfterPreflight(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++

		if attempts == 1 {
			return response(http.StatusOK, `{"id":"pi_native","object":"payment_intent","status":"succeeded","amount":2500,"currency":"usd"}`), nil
		}

		if request.Method != http.MethodPost {
			t.Fatal("read after refund mutation")
		}

		return response(http.StatusServiceUnavailable, `{"error":{"type":"api_error","message":"fixture"}}`), nil
	})

	_, err := backend.RefundPayment(t.Context(), payment.NewRefundRequest(
		"pi_native", payment.Money{Amount: 500, Currency: "USD"}, "refund-123",
	))

	if !errors.Is(err, payment.ErrOutcomeUnknown) || attempts != 2 {
		t.Fatalf("refund mutation: %v; attempts=%d", err, attempts)
	}
}

func TestCancelMalformedSuccessIsUnknownWithoutHydration(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++

		if attempts == 1 {
			return response(http.StatusOK, `{"id":"sub_native","object":"subscription","status":"active"}`), nil
		}

		if request.Method != http.MethodDelete {
			t.Fatal("hydrated after cancellation")
		}

		return response(http.StatusOK, `{}`), nil
	})

	err := backend.CancelSubscription(t.Context(), payment.NewCancelSubscriptionRequest("sub_native"))

	if !errors.Is(err, payment.ErrOutcomeUnknown) || attempts != 2 {
		t.Fatalf("cancellation mutation: %v; attempts=%d", err, attempts)
	}
}

func TestMalformedSubscriptionItemIsUnavailableNotPanic(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"id":"sub_native","object":"subscription","status":"active","items":{"data":[null]}}`), nil
	})

	_, err := backend.GetSubscription(t.Context(), "sub_native")

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("malformed read: %v", err)
	}
}
