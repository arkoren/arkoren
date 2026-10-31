package stripewebhook_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/stripe/stripewebhook"
)

// observe exercises the real signature and handler path for one bound native
// event. The callback must run exactly once before the successful receipt.
func observe(
	t *testing.T,
	event webhook.Event[stripewebhook.Envelope, payment.Observation],
	resource string,
) payment.Observation {
	t.Helper()

	var result payment.Observation
	accepted := 0
	handler := newHandler(t, event, func(_ context.Context, delivery webhook.Delivery[payment.Observation]) error {
		accepted++
		result = delivery.Payload

		return nil
	})
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(eventBody(event.Key(), resource)))

	if recorder.Code != http.StatusOK || accepted != 1 {
		t.Fatalf("receipt: %d; accepted=%d; %s", recorder.Code, accepted, recorder.Body.String())
	}

	return result
}

func TestCheckoutCompletedPreservesUnsettledNativeFacts(t *testing.T) {
	t.Parallel()

	result := observe(t, stripewebhook.CheckoutSessionCompleted,
		`{"id":"cs_native","object":"checkout.session","status":"complete","payment_status":"unpaid","payment_intent":"pi_native","subscription":{"id":"sub_native"},"amount_total":2500,"currency":"usd"}`)

	if result.Kind != payment.CheckoutCompleted || result.CheckoutID != "cs_native" {
		t.Fatalf("checkout observation: %+v", result)
	}

	if result.PaymentID != "pi_native" || result.SubscriptionID != "sub_native" {
		t.Fatalf("native relationships: %+v", result)
	}

	if result.NativeStatus != "complete" || result.Amount == nil {
		t.Fatalf("missing snapshot facts: %+v", result)
	}
}

func TestPaymentFailedPreservesRetryableIntentState(t *testing.T) {
	t.Parallel()

	result := observe(t, stripewebhook.PaymentIntentFailed,
		`{"id":"pi_native","object":"payment_intent","status":"requires_payment_method","amount":2500,"currency":"usd"}`)

	if result.Kind != payment.PaymentFailed || result.NativeStatus != "requires_payment_method" {
		t.Fatalf("failed attempt observation: %+v", result)
	}
}

func TestRefundUpdatedKeepsPendingStatusAndPaymentRelation(t *testing.T) {
	t.Parallel()

	if stripewebhook.RefundUpdated.Key() != "refund.updated" {
		t.Fatal("not the native refund event key")
	}

	result := observe(t, stripewebhook.RefundUpdated,
		`{"id":"re_native","object":"refund","status":"pending","payment_intent":{"id":"pi_native"},"amount":500,"currency":"usd"}`)

	if result.Kind != payment.RefundUpdated || result.RefundID != "re_native" {
		t.Fatalf("refund observation: %+v", result)
	}

	if result.PaymentID != "pi_native" || result.NativeStatus != "pending" {
		t.Fatalf("refund facts: %+v", result)
	}
}

func TestSubscriptionCreatedWithoutPaymentFacts(t *testing.T) {
	t.Parallel()

	result := observe(t, stripewebhook.SubscriptionCreated,
		`{"id":"sub_native","object":"subscription","status":"incomplete"}`)

	if result.Kind != payment.SubscriptionUpdated || result.SubscriptionID != "sub_native" {
		t.Fatalf("subscription observation: %+v", result)
	}

	if result.PaymentID != "" || result.Amount != nil {
		t.Fatalf("invented payment: %+v", result)
	}
}

func TestSubscriptionUpdatedRetainsLifecycleOnly(t *testing.T) {
	t.Parallel()

	result := observe(t, stripewebhook.SubscriptionUpdated,
		`{"id":"sub_native","object":"subscription","status":"active"}`)

	if result.NativeStatus != "active" || result.PaymentID != "" {
		t.Fatalf("active does not imply payment: %+v", result)
	}
}

func TestSubscriptionDeletedRetainsCanceledState(t *testing.T) {
	t.Parallel()

	result := observe(t, stripewebhook.SubscriptionDeleted,
		`{"id":"sub_native","object":"subscription","status":"canceled"}`)

	if result.Kind != payment.SubscriptionUpdated || result.NativeStatus != "canceled" {
		t.Fatalf("deleted lifecycle: %+v", result)
	}
}

func TestInvoicePaidKeepsParentAndDoesNotInventPayment(t *testing.T) {
	t.Parallel()

	result := observe(t, stripewebhook.InvoicePaid,
		`{"id":"in_native","object":"invoice","status":"paid","amount_paid":0,"currency":"usd","parent":{"type":"subscription_details","subscription_details":{"subscription":"sub_native"}}}`)

	if result.Kind != payment.InvoicePaid || result.InvoiceID != "in_native" || result.SubscriptionID != "sub_native" {
		t.Fatalf("invoice identity: %+v", result)
	}

	if result.PaymentID != "" || result.Amount == nil || result.Amount.Amount != 0 {
		t.Fatalf("zero paid invoice invented a collection: %+v", result)
	}
}

func TestInvoicePaymentFailedUsesAmountDueAndExpandedParent(t *testing.T) {
	t.Parallel()

	result := observe(t, stripewebhook.InvoicePaymentFailed,
		`{"id":"in_native","object":"invoice","status":"open","amount_paid":0,"amount_due":2500,"currency":"usd","parent":{"subscription_details":{"subscription":{"id":"sub_native"}}}}`)

	if result.Kind != payment.InvoicePaymentFailed || result.SubscriptionID != "sub_native" {
		t.Fatalf("invoice failed attempt: %+v", result)
	}

	if result.Amount == nil || result.Amount.Amount != 2500 {
		t.Fatalf("failed amount: %+v", result)
	}
}

func TestInvoicePaymentPaidKeepsExplicitInvoiceAndIntentOnly(t *testing.T) {
	t.Parallel()

	if stripewebhook.InvoicePaymentPaid.Key() != "invoice_payment.paid" {
		t.Fatal("wrong native invoice-payment key")
	}

	result := observe(t, stripewebhook.InvoicePaymentPaid,
		`{"id":"inpay_native","object":"invoice_payment","status":"paid","amount_paid":2500,"currency":"usd","invoice":{"id":"in_native"},"payment":{"type":"payment_intent","payment_intent":"pi_native"}}`)

	if result.Kind != payment.InvoicePaymentPaid || result.InvoiceID != "in_native" || result.PaymentID != "pi_native" {
		t.Fatalf("invoice-payment facts: %+v", result)
	}

	if result.SubscriptionID != "" {
		t.Fatalf("invented subscription relation: %+v", result)
	}
}

func TestChargeOnlyInvoicePaymentDoesNotInventIntent(t *testing.T) {
	t.Parallel()

	result := observe(t, stripewebhook.InvoicePaymentPaid,
		`{"id":"inpay_native","object":"invoice_payment","status":"paid","amount_paid":2500,"currency":"usd","payment":{"type":"charge","charge":"ch_native"}}`)

	if result.PaymentID != "" || result.InvoiceID != "" {
		t.Fatalf("invented relationships: %+v", result)
	}
}

func TestMalformedRelationIsUnavailableNotAcknowledged(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, stripewebhook.RefundUpdated, func(context.Context, webhook.Delivery[payment.Observation]) error {
		t.Fatal("malformed relation accepted")

		return nil
	})
	body := eventBody("refund.updated", `{"id":"re_native","object":"refund","status":"pending","payment_intent":{"not_id":"pi_native"}}`)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("malformed relation receipt: %d", recorder.Code)
	}
}
