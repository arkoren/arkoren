package paypalwebhook_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/paypal/paypalwebhook"
)

// receive executes a credential-free event through the actual verified source
// and handler. The transport rejects resource hydration or certificate fetches.
func receive(
	t *testing.T, event webhook.Event[paypalwebhook.Envelope, payment.Observation], body string,
) webhook.Delivery[payment.Observation] {
	t.Helper()

	verifications := 0
	source := sourceWith(t, func(request *http.Request) (*http.Response, error) {
		verifications++

		if request.URL.Path != "/v1/notifications/verify-webhook-signature" {
			return nil, errors.New("event decoder attempted network hydration")
		}

		return response(200, `{"verification_status":"SUCCESS"}`), nil
	})

	var observed webhook.Delivery[payment.Observation]
	endpoint, err := webhook.Handler(source, func(listener *webhook.Builder[paypalwebhook.Envelope]) {
		listener.Handle(event, func(_ context.Context, delivery webhook.Delivery[payment.Observation]) error {
			observed = delivery

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, signedRequest(body))

	if recorder.Code != 200 || verifications != 1 {
		t.Fatal("native event was not converted purely after verification")
	}

	return observed
}

// TestSaleObservationRetainsNativeFamilyAndForintScale verifies the shared
// exact money conversion and optional subscription relationship on native sales.
func TestSaleObservationRetainsNativeFamilyAndForintScale(t *testing.T) {
	t.Parallel()

	observation := receive(t, paypalwebhook.PaymentSaleCompleted,
		`{"id":"WH-SALE","event_type":"PAYMENT.SALE.COMPLETED","create_time":"2026-10-24T03:04:05Z",
		"resource":{"id":"SALE","state":"completed","billing_agreement_id":"I-SUB",
		"amount":{"currency":"HUF","total":"10"}}}`).Payload

	if observation.PaymentID != "sale:SALE" || observation.SubscriptionID != "I-SUB" {
		t.Fatal("sale or subscription identity was guessed or lost")
	}

	if observation.Kind != payment.PaymentSucceeded || observation.NativeStatus != "completed" {
		t.Fatal("native sale outcome was not retained")
	}

	if observation.Amount == nil || observation.Amount.Amount != 1000 {
		t.Fatal("HUF was not parsed using canonical ISO scale")
	}
}

// TestSaleRefundDoesNotBecomeV2Refund retains the explicit original sale_id
// without promising that a v1 refund can be looked up through Payments v2.
func TestSaleRefundDoesNotBecomeV2Refund(t *testing.T) {
	t.Parallel()

	observation := receive(t, paypalwebhook.PaymentSaleRefunded,
		`{"id":"WH-REFUND","event_type":"PAYMENT.SALE.REFUNDED",
		"resource":{"id":"NATIVE-REFUND","sale_id":"SALE","state":"completed",
		"amount":{"currency":"USD","total":"1.00"}}}`).Payload

	if observation.PaymentID != "sale:SALE" || observation.RefundID != "" {
		t.Fatal("v1 refund identity was mislabeled as sale or v2 refund")
	}

	if observation.Kind != payment.RefundUpdated {
		t.Fatal("sale refund was treated as a fresh successful payment")
	}
}

// TestActivationObservationIsLifecycleOnly never manufactures an amount,
// individual payment, paid-through date or invoice from ACTIVE lifecycle.
func TestActivationObservationIsLifecycleOnly(t *testing.T) {
	t.Parallel()

	observation := receive(t, paypalwebhook.SubscriptionActivated,
		`{"id":"WH-ACTIVE","event_type":"BILLING.SUBSCRIPTION.ACTIVATED",
		"resource":{"id":"I-SUB","status":"ACTIVE","billing_info":{"next_billing_time":"2027-01-02T03:04:05Z"}}}`).Payload

	if observation.Kind != payment.SubscriptionUpdated || observation.SubscriptionID != "I-SUB" {
		t.Fatal("native lifecycle lost")
	}

	if observation.PaymentID != "" || observation.Amount != nil || observation.InvoiceID != "" {
		t.Fatal("active subscription manufactured settlement")
	}
}

// TestOrderApprovalIsNotPayment preserves the provider's order approval without
// inferring successful funds or automatically invoking capture.
func TestOrderApprovalIsNotPayment(t *testing.T) {
	t.Parallel()

	observation := receive(t, paypalwebhook.CheckoutOrderApproved,
		`{"id":"WH-APPROVED","event_type":"CHECKOUT.ORDER.APPROVED","resource":{"id":"ORDER","status":"APPROVED"}}`).Payload

	if observation.Kind != payment.CheckoutApproved || observation.CheckoutID != "order:ORDER" {
		t.Fatal("order approval mapping lost")
	}

	if observation.PaymentID != "" || observation.Amount != nil {
		t.Fatal("order approval manufactured a capture")
	}
}

// TestPendingCaptureDoesNotReportPaymentSucceeded keeps unsettled native
// captures explicit without adding a portable event kind the core does not own.
func TestPendingCaptureDoesNotReportPaymentSucceeded(t *testing.T) {
	t.Parallel()

	observation := receive(t, paypalwebhook.PaymentCapturePending,
		`{"id":"WH-PENDING","event_type":"PAYMENT.CAPTURE.PENDING",
		"resource":{"id":"CAPTURE","status":"PENDING","amount":{"currency_code":"JPY","value":"100"},
		"supplementary_data":{"related_ids":{"order_id":"ORDER"}}}}`).Payload

	if observation.Kind != payment.ObservationKindUnknown || observation.NativeStatus != "PENDING" {
		t.Fatal("pending capture became succeeded or failed")
	}

	if observation.CheckoutID != "order:ORDER" || observation.PaymentID != "capture:CAPTURE" {
		t.Fatal("explicit native relationships lost")
	}

	if observation.Amount == nil || observation.Amount.Amount != 100 {
		t.Fatal("zero-decimal JPY was rescaled")
	}
}

// TestVerifiedDeliveryPreservesIdentityAndTime retains the native stable event
// ID rather than the attempt ID, and parses exact money and provider occurrence.
func TestVerifiedDeliveryPreservesIdentityAndTime(t *testing.T) {
	t.Parallel()

	delivery := receive(t, paypalwebhook.PaymentCaptureCompleted,
		`{"id":"WH-STABLE","event_type":"PAYMENT.CAPTURE.COMPLETED","create_time":"2026-10-24T03:04:05Z",
		"resource":{"id":"CAPTURE","status":"COMPLETED","amount":{"currency_code":"USD","value":"12.34"}}}`)

	if delivery.ID != "WH-STABLE" || delivery.Type != "PAYMENT.CAPTURE.COMPLETED" {
		t.Fatal("attempt ID replaced stable event ID or native key was rewritten")
	}

	if delivery.Payload.PaymentID != "capture:CAPTURE" || delivery.Payload.Kind != payment.PaymentSucceeded {
		t.Fatal("capture observation lost identity or native outcome")
	}

	if delivery.Payload.Amount == nil || delivery.Payload.Amount.Amount != 1234 {
		t.Fatal("capture observation lost exact minor units")
	}

	if !delivery.Payload.OccurredAt.Equal(time.Date(2026, 10, 24, 3, 4, 5, 0, time.UTC)) {
		t.Fatal("provider occurrence time was not retained")
	}
}
