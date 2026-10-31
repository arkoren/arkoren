package stripepayment_test

import (
	"net/http"
	"testing"

	"arkoren.dev/core/service/payment"
)

func TestSubscriptionCheckoutUsesExistingPriceAndCorrelation(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++
		expectRequest(t, request, http.MethodPost, "/v1/checkout/sessions")
		values := form(t, request)

		if values.Get("mode") != "subscription" || values.Get("line_items[0][price]") != "price_monthly" {
			t.Fatalf("not an external recurring price: %v", values)
		}

		if values.Get("line_items[0][quantity]") != "1" || values.Has("line_items[0][price_data][currency]") {
			t.Fatalf("unexpected catalog mutation: %v", values)
		}

		if values.Get("subscription_data[metadata][reference]") != "signup-123" {
			t.Fatal("missing subscription correlation")
		}

		if request.Header.Get("Idempotency-Key") != "signup-123" {
			t.Fatal("missing subscription checkout key")
		}

		return response(http.StatusOK, `{"id":"cs_subscription","object":"checkout.session","mode":"subscription","status":"open","payment_status":"unpaid"}`), nil
	})

	checkout, err := backend.CreateSubscriptionCheckout(t.Context(), payment.NewSubscriptionCheckoutRequest(
		"price_monthly", "https://shop.example/account", "signup-123",
		func(details *payment.SubscriptionCheckoutRequestBuilder) {
			details.Reference("signup-123")
		},
	))

	if err != nil || checkout.Kind != payment.CheckoutKindSubscription || attempts != 1 {
		t.Fatalf("subscription checkout: %+v; %v", checkout, err)
	}
}

func TestGetSubscriptionWithoutCheckoutOrWebhooks(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		expectRequest(t, request, http.MethodGet, "/v1/subscriptions/sub_external")

		if request.URL.Query().Get("expand[0]") != "items.data.price" {
			t.Fatal("missing price expansion")
		}

		return response(http.StatusOK, `{"id":"sub_external","object":"subscription","status":"active","metadata":{"reference":"signup-123"},"items":{"object":"list","has_more":false,"data":[{"price":{"id":"price_monthly"},"current_period_end":4102444800}]}}`), nil
	})

	subscription, err := backend.GetSubscription(t.Context(), "sub_external")

	if err != nil {
		t.Fatal(err)
	}

	if subscription.ID != "sub_external" || subscription.PlanReference != "price_monthly" {
		t.Fatalf("subscription identities: %+v", subscription)
	}

	if subscription.Status != payment.SubscriptionStatusActive || subscription.Reference != "signup-123" {
		t.Fatalf("subscription lifecycle: %+v", subscription)
	}

	if subscription.NextBillingAt != nil {
		t.Fatalf("active period boundary became a collection time: %+v", subscription)
	}
}

func TestCanceledSubscriptionPeriodEndDoesNotPromiseFutureBilling(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		expectRequest(t, request, http.MethodGet, "/v1/subscriptions/sub_external")

		return response(http.StatusOK, `{"id":"sub_external","object":"subscription","status":"canceled","items":{"object":"list","has_more":false,"data":[{"price":{"id":"price_monthly"},"current_period_end":4102444800}]}}`), nil
	})

	subscription, err := backend.GetSubscription(t.Context(), "sub_external")

	if err != nil {
		t.Fatal(err)
	}

	if subscription.Status != payment.SubscriptionStatusCanceled || subscription.PlanReference != "price_monthly" {
		t.Fatalf("canceled subscription lost native facts: %+v", subscription)
	}

	if subscription.NextBillingAt != nil {
		t.Fatalf("canceled period boundary became a collection time: %+v", subscription)
	}
}

func TestCancelSubscriptionImmediateNoProrationOrHydration(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++

		if attempts == 1 {
			expectRequest(t, request, http.MethodGet, "/v1/subscriptions/sub_external")

			return response(http.StatusOK, `{"id":"sub_external","object":"subscription","status":"active"}`), nil
		}

		if attempts != 2 {
			t.Fatal("unexpected cancel replay or hydration")
		}

		expectRequest(t, request, http.MethodDelete, "/v1/subscriptions/sub_external")
		values := request.URL.Query()

		if values.Get("invoice_now") != "false" || values.Get("prorate") != "false" {
			t.Fatalf("cancellation can invoice or prorate: %v", values)
		}

		if values.Get("cancellation_details[comment]") != "Customer request" {
			t.Fatal("missing reason")
		}

		return response(http.StatusOK, `{"id":"sub_external","object":"subscription","status":"canceled"}`), nil
	})

	err := backend.CancelSubscription(t.Context(), payment.NewCancelSubscriptionRequest("sub_external",
		func(details *payment.CancelSubscriptionRequestBuilder) {
			details.Reason("Customer request")
		},
	))

	if err != nil || attempts != 2 {
		t.Fatalf("cancel: %v; attempts=%d", err, attempts)
	}
}

func TestCancelAlreadyCanceledIsSuccessfulReadOnly(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++

		if request.Method != http.MethodGet {
			t.Fatal("already-canceled subscription mutated")
		}

		return response(http.StatusOK, `{"id":"sub_external","object":"subscription","status":"canceled"}`), nil
	})

	err := backend.CancelSubscription(t.Context(), payment.NewCancelSubscriptionRequest("sub_external"))

	if err != nil || attempts != 1 {
		t.Fatalf("already canceled: %v; attempts=%d", err, attempts)
	}
}
