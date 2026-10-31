package stripepayment_test

import (
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"arkoren.dev/core/service/payment"
)

func TestGetPaymentRequiresMethodIsNotTerminalFailure(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/payment_intents/pi_native" {
			t.Fatal("not a PaymentIntent read")
		}

		return response(http.StatusOK, `{"id":"pi_native","object":"payment_intent","status":"requires_payment_method","amount":2500,"currency":"usd"}`), nil
	})

	result, err := backend.GetPayment(t.Context(), "pi_native")

	if err != nil || result.Status != payment.StatusRequiresAction {
		t.Fatalf("payment: %+v; %v", result, err)
	}

	if result.NativeStatus != "requires_payment_method" || result.ID != "pi_native" {
		t.Fatalf("lost native facts: %+v", result)
	}
}

func TestRefundPreflightThenOneMutationWithoutHydration(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++

		if attempts == 1 {
			expectRequest(t, request, http.MethodGet, "/v1/payment_intents/pi_native")

			return response(http.StatusOK, `{"id":"pi_native","object":"payment_intent","status":"succeeded","amount":2500,"currency":"usd"}`), nil
		}

		if attempts != 2 {
			t.Fatal("unexpected refund replay or hydration")
		}

		expectRequest(t, request, http.MethodPost, "/v1/refunds")

		if request.Header.Get("Idempotency-Key") != "refund-123" {
			t.Fatal("wrong refund key")
		}

		if got := form(t, request); !reflect.DeepEqual(got, url.Values{"payment_intent": {"pi_native"}, "amount": {"500"}}) {
			t.Fatalf("refund form: %v", got)
		}

		return response(http.StatusOK, `{"id":"re_native","object":"refund","payment_intent":"pi_native","status":"pending","amount":500,"currency":"usd"}`), nil
	})

	refund, err := backend.RefundPayment(t.Context(), payment.NewRefundRequest(
		"pi_native", payment.Money{Amount: 500, Currency: "USD"}, "refund-123",
	))

	if err != nil || attempts != 2 || refund.Status != payment.RefundStatusPending {
		t.Fatalf("refund: %+v; %v; attempts=%d", refund, err, attempts)
	}

	if refund.ID != "re_native" || refund.PaymentID != "pi_native" {
		t.Fatalf("native identities: %+v", refund)
	}
}

func TestRefundCurrencyMismatchNeverSubmitsAmount(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++

		if request.Method != http.MethodGet {
			t.Fatal("submitted refund in the wrong currency")
		}

		return response(http.StatusOK, `{"id":"pi_native","object":"payment_intent","status":"succeeded","amount":2500,"currency":"eur"}`), nil
	})

	_, err := backend.RefundPayment(t.Context(), payment.NewRefundRequest(
		"pi_native", payment.Money{Amount: 500, Currency: "USD"}, "refund-123",
	))

	if !errors.Is(err, payment.ErrInvalidRequest) || attempts != 1 {
		t.Fatalf("currency mismatch: %v; attempts=%d", err, attempts)
	}
}

func TestGetRefundIsIndependentRead(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/refunds/re_native" {
			t.Fatal("unexpected refund operation")
		}

		return response(http.StatusOK, `{"id":"re_native","object":"refund","payment_intent":"pi_native","status":"succeeded","amount":500,"currency":"usd"}`), nil
	})

	refund, err := backend.GetRefund(t.Context(), "re_native")

	if err != nil || refund.Status != payment.RefundStatusSucceeded {
		t.Fatalf("refund: %+v; %v", refund, err)
	}
}
