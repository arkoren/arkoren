package molliepayment_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/paymenttest"
	"arkoren.dev/driver/mollie/molliepayment"
)

// TestDriverDiagnosticsRedactOwnedCredentials covers pointer/value formatting
// and both standard slog handlers without contacting Mollie or exporting keys.
func TestDriverDiagnosticsRedactOwnedCredentials(t *testing.T) {
	t.Parallel()

	driver, err := molliepayment.New("test_diagnosticsecret")

	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer

	slog.New(slog.NewTextHandler(&logs, nil)).InfoContext(t.Context(), "backend", "driver", driver)
	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "backend", "driver", *driver)

	formatted := []string{
		fmt.Sprint(driver), fmt.Sprintf("%+v", driver), fmt.Sprintf("%#v", driver),
		fmt.Sprint(*driver), fmt.Sprintf("%+v", *driver), fmt.Sprintf("%#v", *driver), logs.String(),
	}

	for _, diagnostic := range formatted {
		if strings.Contains(diagnostic, "diagnosticsecret") {
			t.Fatal("driver diagnostic exposed its owned API key")
		}

		if !strings.Contains(diagnostic, "Driver{REDACTED}") {
			t.Fatal("driver diagnostic did not use a redacted representation")
		}
	}
}

// Example demonstrates construction without credentials loading or network
// access. Real requests require your own profile's test_ or live_ API key.
func Example() {
	backend, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
		merchant.WebhookURL("https://shop.example/mollie")
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	client := payment.New(backend)
	fmt.Println(client != nil, backend.HTTPClient().Timeout)
	// Output: true 30s
}

// roundTrip substitutes only the external HTTP boundary, keeping production
// encoding, fixed origins, redirects and outcome classification under test.
type roundTrip func(*http.Request) (*http.Response, error)

// RoundTrip executes a deterministic protocol response without network access.
// Example: client := &http.Client{Transport: roundTrip(fn)}; _ = client
func (transport roundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// response constructs complete protocol replies, including bodies on errors
// so tests can verify that provider prose never appears in returned errors.
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body))}
}

// newDriver builds a real driver around one deterministic HTTP substitute.
// The substitute owns no resources or cleanup and receives the real API URL.
func newDriver(t *testing.T, transport roundTrip) *molliepayment.Driver {
	t.Helper()

	driver, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
		merchant.HTTPClient(&http.Client{Transport: transport})
	})

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// checkoutRequest supplies portable valid input; individual tests change only
// the field whose validation or wire representation is under observation.
func checkoutRequest() payment.CheckoutRequest {
	return payment.NewCheckoutRequest(payment.Money{Amount: 1200, Currency: "USD"},
		"https://shop.example/paid", "order-123")
}

// fixture models payment approval and refund progress independently. Its
// mutex protects state accessed by conformance hooks and HTTP calls.
type fixture struct {
	// mu serializes fixture state; production driver calls remain concurrent-safe.
	mu sync.Mutex
	// status is the simulated payment's native lifecycle token.
	status string
	// amount is copied from the fixture's create request, as a provider would do.
	amount any
	// metadata retains application reference fields for later observations.
	metadata any
	// refund is the independently readable refund resource.
	refund map[string]any
}

// RoundTrip routes a small deterministic Payments v2 protocol. It models no
// subscriptions, capture endpoint or hidden provider-side lookup.
// Example: transport := &fixture{}; _ = transport
func (server *fixture) RoundTrip(request *http.Request) (*http.Response, error) {
	server.mu.Lock()
	defer server.mu.Unlock()

	if request.URL.Host != "api.mollie.com" {
		return nil, fmt.Errorf("unexpected protocol origin")
	}

	if request.Method == http.MethodPost {
		return server.mutate(request)
	}

	if request.URL.Path == "/v2/payments/tr_example/refunds/re_example" {
		return server.encode(server.refund)
	}

	if request.URL.Path == "/v2/payments/tr_example" {
		return server.encode(server.payment())
	}

	return response(http.StatusNotFound, `{"detail":"not found"}`), nil
}

// mutate decodes requests once and persists provider-shaped observations. It
// is invoked under the fixture lock, never concurrently with approval hooks.
func (server *fixture) mutate(request *http.Request) (*http.Response, error) {
	var body map[string]any

	if err := json.UnmarshalRead(request.Body, &body); err != nil {
		return nil, err
	}

	if request.URL.Path == "/v2/payments" {
		server.status = "open"
		server.amount = body["amount"]
		server.metadata = body["metadata"]

		return server.encode(server.payment())
	}

	if request.URL.Path == "/v2/payments/tr_example/refunds" {
		server.refund = map[string]any{"id": "re_example", "paymentId": "tr_example",
			"status": "pending", "amount": body["amount"]}

		return server.encode(server.refund)
	}

	return response(http.StatusNotFound, `{}`), nil
}

// payment returns provider-owned data. JSON serialization detaches this state
// from returned checkout amount pointers before the test can mutate them.
func (server *fixture) payment() map[string]any {
	return map[string]any{"id": "tr_example", "status": server.status,
		"amount": server.amount, "metadata": server.metadata,
		"_links": map[string]any{"checkout": map[string]string{"href": "https://www.mollie.com/checkout/example"}}}
}

// encode serializes fixture responses through the same real JSON wire shape
// used by Mollie, returning encoder failures rather than swallowing them.
func (server *fixture) encode(value any) (*http.Response, error) {
	data, err := json.Marshal(value)

	if err != nil {
		return nil, err
	}

	return response(http.StatusOK, string(data)), nil
}

// TestConformance runs the portable validation, approval, settlement, refund
// and unsupported-operation checks against the real Mollie protocol adapter.
func TestConformance(t *testing.T) {
	t.Parallel()

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		server := &fixture{}
		driver := newDriver(t, server.RoundTrip)

		return paymenttest.Harness{Driver: driver, Amount: payment.Money{Amount: 1200, Currency: "EUR"},
			Approve: func(_ context.Context, _ payment.CheckoutID) error {
				server.mu.Lock()
				defer server.mu.Unlock()

				server.status = "pending"

				return nil
			},
			ResolvePayment: func(_ context.Context, _ payment.ID) error {
				server.mu.Lock()
				defer server.mu.Unlock()

				server.status = "paid"

				return nil
			},
		}
	}, paymenttest.WithoutCompletion(), paymenttest.WithoutSubscriptions(), paymenttest.WithoutPreparation())
}
