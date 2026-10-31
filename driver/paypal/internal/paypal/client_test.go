package paypal_test

import (
	"errors"
	"io"
	"net/http"
	"testing"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/paypal/internal/paypal"
)

// forbiddenTransport fails on any network attempt. It proves invalid private
// protocol requests fail before OAuth rather than relying on provider rejection.
type forbiddenTransport struct{}

// RoundTrip rejects unexpected dispatch without making an external connection.
// Example: client := &http.Client{Transport: forbiddenTransport{}}; _ = client
func (forbiddenTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, io.ErrUnexpectedEOF
}

// TestDeviceReferenceCannotReachOtherEndpoints enforces the narrow typed-field
// boundary even if another internal caller mistakenly supplies SDK risk data.
func TestDeviceReferenceCannotReachOtherEndpoints(t *testing.T) {
	t.Parallel()

	client, err := paypal.New("client", "secret", paypal.Settings{
		HTTPClient: &http.Client{Transport: forbiddenTransport{}},
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, operation := range []paypal.Request{
		{Method: http.MethodPost, Path: "/v2/checkout/orders/ORDER/capture", DeviceReference: "private-risk"},
		{Method: http.MethodGet, Path: "/v2/checkout/orders", DeviceReference: "private-risk"},
	} {
		err := client.Call(t.Context(), operation, nil)

		if !errors.Is(err, payment.ErrInvalidRequest) || errors.Is(err, payment.ErrOutcomeUnknown) {
			t.Fatalf("private risk data escaped endpoint validation: %v", err)
		}
	}
}
