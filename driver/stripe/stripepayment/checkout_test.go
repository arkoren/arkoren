package stripepayment_test

import (
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"arkoren.dev/core/service/payment"
)

func TestCreateCheckoutSendsInlineMinorUnitsAndNativeID(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++
		expectRequest(t, request, http.MethodPost, "/v1/checkout/sessions")

		if request.Header.Get("Idempotency-Key") != "order-123" {
			t.Fatal("missing operation key")
		}

		want := url.Values{
			"mode": {"payment"}, "ui_mode": {"hosted_page"},
			"success_url": {"https://shop.example/paid"}, "cancel_url": {"https://shop.example/cart"},
			"client_reference_id": {"order-123"}, "metadata[reference]": {"order-123"},
			"payment_intent_data[description]": {"Book"}, "payment_intent_data[metadata][reference]": {"order-123"},
			"line_items[0][quantity]": {"1"}, "line_items[0][price_data][currency]": {"usd"},
			"line_items[0][price_data][unit_amount]":        {"2500"},
			"line_items[0][price_data][product_data][name]": {"Book"},
			"expand[0]": {"payment_intent"}, "expand[1]": {"subscription"},
		}

		if got := form(t, request); !reflect.DeepEqual(got, want) {
			t.Fatalf("form:\n%v\nwant:\n%v", got, want)
		}

		return response(http.StatusOK, `{"id":"cs_native","object":"checkout.session","mode":"payment","status":"open","payment_status":"unpaid","url":"https://checkout.stripe.com/private","amount_total":2500,"currency":"usd","client_reference_id":"order-123"}`), nil
	})

	checkout, err := backend.CreateCheckout(t.Context(), payment.NewCheckoutRequest(
		payment.Money{Amount: 2500, Currency: "USD"}, "https://shop.example/paid", "order-123",
		func(details *payment.CheckoutRequestBuilder) {
			details.Description("Book")
			details.CancelURL("https://shop.example/cart")
			details.Reference("order-123")
		},
	))

	if err != nil {
		t.Fatal(err)
	}

	if checkout.ID != "cs_native" || checkout.Status != payment.CheckoutStatusOpen || attempts != 1 {
		t.Fatalf("checkout: %+v; attempts=%d", checkout, attempts)
	}

	if checkout.PaymentStatus != payment.StatusPending || checkout.NeedsCompletion {
		t.Fatalf("invented collection or completion: %+v", checkout)
	}
}

func TestCreateCheckoutDefaultNameAndZeroDecimalCurrency(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		values := form(t, request)

		if values.Get("line_items[0][price_data][product_data][name]") != "Payment" {
			t.Fatal("empty description has no safe product name")
		}

		if values.Get("line_items[0][price_data][unit_amount]") != "500" || values.Get("line_items[0][price_data][currency]") != "jpy" {
			t.Fatal("zero-decimal amount converted incorrectly")
		}

		if values.Has("cancel_url") {
			t.Fatal("invented optional cancel URL")
		}

		return response(http.StatusOK, `{"id":"cs_native","object":"checkout.session","mode":"payment","status":"open"}`), nil
	})

	_, err := backend.CreateCheckout(t.Context(), payment.NewCheckoutRequest(
		payment.Money{Amount: 500, Currency: "JPY"}, "https://shop.example/paid", "order-123",
	))

	if err != nil {
		t.Fatal(err)
	}
}

func TestGetCheckoutKeepsCompletionSeparateFromPayment(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++

		if request.Method != http.MethodGet || request.URL.Path != "/v1/checkout/sessions/cs_native" {
			t.Fatalf("unexpected read: %s %s", request.Method, request.URL.Path)
		}

		if len(request.URL.Query()) != 2 {
			t.Fatalf("missing relationship expansion: %s", request.URL.RawQuery)
		}

		return response(http.StatusOK, `{"id":"cs_native","object":"checkout.session","mode":"subscription","status":"complete","payment_status":"unpaid","payment_intent":{"id":"pi_native","status":"processing"},"subscription":"sub_native"}`), nil
	})

	checkout, err := backend.GetCheckout(t.Context(), "cs_native")

	if err != nil {
		t.Fatal(err)
	}

	if checkout.PaymentID != "pi_native" || checkout.SubscriptionID != "sub_native" || attempts != 1 {
		t.Fatalf("relations: %+v", checkout)
	}

	if checkout.Status != payment.CheckoutStatusComplete || checkout.PaymentStatus != payment.StatusPending {
		t.Fatalf("conflated checkout completion and payment: %+v", checkout)
	}
}

func TestCompleteCheckoutOnlyReadsNoPaymentRequired(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		attempts++

		if request.Method != http.MethodGet {
			t.Fatal("CompleteCheckout attempted to charge again")
		}

		return response(http.StatusOK, `{"id":"cs_native","object":"checkout.session","mode":"subscription","status":"complete","payment_status":"no_payment_required"}`), nil
	})

	checkout, err := backend.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest("cs_native", "complete-123"))

	if err != nil || checkout.PaymentStatus != payment.StatusNotRequired || attempts != 1 {
		t.Fatalf("completion: %+v; %v; attempts=%d", checkout, err, attempts)
	}
}

func TestCompleteCheckoutRefusesOpenFlow(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Fatal("completion mutation")
		}

		return response(http.StatusOK, `{"id":"cs_native","object":"checkout.session","status":"open"}`), nil
	})

	_, err := backend.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest("cs_native", "complete-123"))

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatalf("open checkout: %v", err)
	}
}

func TestTrialZeroTotalPaidMeansNoPaymentRequired(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"id":"cs_trial","object":"checkout.session","mode":"subscription","status":"complete","payment_status":"paid","amount_total":0,"currency":"usd","payment_intent":null,"subscription":null}`), nil
	})

	checkout, err := backend.GetCheckout(t.Context(), "cs_trial")

	if err != nil {
		t.Fatal(err)
	}

	if checkout.PaymentStatus != payment.StatusNotRequired || checkout.Status != payment.CheckoutStatusComplete {
		t.Fatalf("trial zero invoice inferred collection: %+v", checkout)
	}

	if checkout.Amount == nil || checkout.Amount.Amount != 0 {
		t.Fatalf("lost proven zero total: %+v", checkout)
	}

	if checkout.PaymentID != "" || checkout.SubscriptionID != "" {
		t.Fatalf("invented trial relationships: %+v", checkout)
	}
}

func TestAbsentCheckoutTotalIsNotProvenZero(t *testing.T) {
	t.Parallel()

	backend := newBackend(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"id":"cs_native","object":"checkout.session","mode":"subscription","status":"complete","payment_status":"paid","currency":"usd"}`), nil
	})

	checkout, err := backend.GetCheckout(t.Context(), "cs_native")

	if err != nil {
		t.Fatal(err)
	}

	if checkout.Amount != nil || checkout.PaymentStatus == payment.StatusNotRequired {
		t.Fatalf("absent total became a zero-payment flow: %+v", checkout)
	}

	if checkout.PaymentID != "" || checkout.SubscriptionID != "" {
		t.Fatalf("invented relationships: %+v", checkout)
	}
}
