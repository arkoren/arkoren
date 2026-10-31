package payment_test

import (
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
)

func TestCompleteCheckoutRequiresKey(t *testing.T) {
	t.Parallel()

	request := payment.NewCompleteCheckoutRequest("cs_example", "complete-123")

	if err := request.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}

	request = payment.NewCompleteCheckoutRequest(request.CheckoutID(), "")

	if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestRefundRequiresPositiveExplicitAmount(t *testing.T) {
	t.Parallel()

	request := payment.NewRefundRequest("pi_example", payment.Money{Amount: 500, Currency: "USD"}, "refund-123")

	if err := request.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}

	amount := request.Amount()
	amount.Amount = 0

	if request.Amount().Amount != 500 {
		t.Fatal("getter aliased Money")
	}

	request = payment.NewRefundRequest(request.PaymentID(), amount, request.IdempotencyKey())

	if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestCancelSubscriptionReasonLimit(t *testing.T) {
	t.Parallel()

	request := payment.NewCancelSubscriptionRequest("sub_example", func(details *payment.CancelSubscriptionRequestBuilder) {
		details.Reason(strings.Repeat("é", 128))
	})

	if err := request.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}

	request = payment.NewCancelSubscriptionRequest(request.SubscriptionID(), func(details *payment.CancelSubscriptionRequestBuilder) {
		details.Reason(strings.Repeat("é", 129)).Reason("valid replacement")
	})

	if err := request.Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("Validate = %v", err)
	}
}

// TestCompleteCheckoutRequestCodec preserves the existing explicit-completion
// JSON shape without an exported request DTO or mutable fields.
func TestCompleteCheckoutRequestCodec(t *testing.T) {
	t.Parallel()

	checkRequestCodec(t, payment.NewCompleteCheckoutRequest("cs_1", "capture-1"),
		`{"checkout_id":"cs_1","idempotency_key":"capture-1"}`)
}

// TestRefundRequestCodec preserves exact minor units and existing refund tags.
// There is no conversion to floats or implicit full-refund encoding.
func TestRefundRequestCodec(t *testing.T) {
	t.Parallel()

	checkRequestCodec(t, payment.NewRefundRequest("pi_1", payment.Money{Amount: 100, Currency: "USD"}, "refund-1"),
		`{"payment_id":"pi_1","amount":{"amount":100,"currency":"USD"},"idempotency_key":"refund-1"}`)
}

// TestCancelSubscriptionRequestCodec preserves the immediate-cancellation shape
// and provider-visible reason, without introducing scheduled cancellation.
func TestCancelSubscriptionRequestCodec(t *testing.T) {
	t.Parallel()

	request := payment.NewCancelSubscriptionRequest("sub_1", func(details *payment.CancelSubscriptionRequestBuilder) {
		details.Reason("Customer request")
	})

	checkRequestCodec(t, request, `{"subscription_id":"sub_1","reason":"Customer request"}`)
}

// TestCancellationDraftCallbacksRunOnInvalidIdentity checks ordered direct
// callback execution and publication independence even for invalid declarations.
func TestCancellationDraftCallbacksRunOnInvalidIdentity(t *testing.T) {
	t.Parallel()

	var retained *payment.CancelSubscriptionRequestBuilder
	calls := 0
	request := payment.NewCancelSubscriptionRequest("", func(details *payment.CancelSubscriptionRequestBuilder) {
		calls++
		retained = details
		details.Reason("first")
	}, func(details *payment.CancelSubscriptionRequestBuilder) {
		calls++

		if retained != details {
			t.Fatal("cancellation callbacks used different drafts")
		}

		details.Reason("second")
	})
	retained.Reason("changed")

	if calls != 2 || request.Reason() != "second" {
		t.Fatal("cancellation callback order or ownership changed")
	}

	if !errors.Is(request.Err(), payment.ErrInvalidRequest) {
		t.Fatal("invalid identity repaired")
	}
}
