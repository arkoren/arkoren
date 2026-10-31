package payment_test

import (
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
)

func TestIdentifiersRejectEmptyWhitespaceAndControls(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", " leading", "trailing ", "two words", "a\n", "a\x00", strings.Repeat("a", 256)} {
		if err := payment.CheckoutID(value).Validate(t.Context()); !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("Validate(%q) = %v", value, err)
		}
	}
}

func TestIdentifiersPreserveProviderFamilyPrefixes(t *testing.T) {
	t.Parallel()

	id := payment.ID("capture:ABC-123")

	if err := id.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutJSONUsesSnakeCaseAndOmitsUnknownValues(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(payment.Checkout{ID: "cs_example", NeedsCompletion: true, PaymentID: "pi_example"})

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"id":"cs_example","needs_completion":true,"payment_id":"pi_example"}` {
		t.Fatalf("JSON = %s", encoded)
	}
}

func TestObservationJSONOmitsUnknownValues(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(payment.Observation{})

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{}` {
		t.Fatalf("JSON = %s", encoded)
	}
}
