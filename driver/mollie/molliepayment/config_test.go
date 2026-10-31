package molliepayment_test

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkoren.dev/driver/mollie/molliepayment"
)

// TestConstructionPoliciesAreOnceOrderedAndDetached checks callback timing,
// borrowed-client ownership and publication independently of native HTTP I/O.
func TestConstructionPoliciesAreOnceOrderedAndDetached(t *testing.T) {
	t.Parallel()

	var retained *molliepayment.Builder
	var order []int
	client := &http.Client{Timeout: time.Second}

	driver, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
		order = append(order, 1)
		retained = merchant
		merchant.HTTPClient(client)
		merchant.WebhookURL("https://shop.example/first")
	}, func(merchant *molliepayment.Builder) {
		order = append(order, 2)
		merchant.WebhookURL("https://shop.example/final")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.HTTPClient(&http.Client{Timeout: time.Hour})
	retained.WebhookURL("https://shop.example/changed")
	client.Timeout = time.Minute

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatal("construction policies were not once in order")
	}

	if driver.HTTPClient() == client || driver.HTTPClient().Timeout != time.Second {
		t.Fatal("retained client or draft changed published settings")
	}

	driver.HTTPClient().Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
		var body map[string]any

		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			t.Fatal(err)
		}

		if body["webhookUrl"] != "https://shop.example/final" {
			t.Fatal("retained draft changed callback URL")
		}

		return response(201, `{"id":"tr_example","status":"pending","amount":{"currency":"EUR","value":"12.00"}}`), nil
	})

	if _, err := driver.CreateCheckout(t.Context(), checkoutRequest()); err != nil {
		t.Fatal(err)
	}
}

// TestConstructorNeverDispatches verifies credential validation and callback
// application are entirely local, even with a transport that cannot be used.
func TestConstructorNeverDispatches(t *testing.T) {
	t.Parallel()

	called := 0

	_, err := molliepayment.New("invalid-secret", func(merchant *molliepayment.Builder) {
		called++
		merchant.HTTPClient(&http.Client{Transport: roundTrip(func(_ *http.Request) (*http.Response, error) {
			t.Fatal("constructor performed HTTP")

			return nil, io.EOF
		})})
	})

	if err == nil || called != 1 || strings.Contains(err.Error(), "invalid-secret") {
		t.Fatal("invalid credentials changed construction boundary or leaked")
	}
}

// TestConstructorRejectsUnsafeCallbackURLs checks callbacks cannot contain
// user-info credentials or unsupported insecure destinations.
func TestConstructorRejectsUnsafeCallbackURLs(t *testing.T) {
	t.Parallel()

	for _, destination := range []string{"http://shop.example/callback", "https://secret@shop.example/callback",
		"https://shop.example/callback#secret", "/callback", "https:///callback"} {
		_, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
			merchant.WebhookURL(destination)
		})

		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe callback accepted or echoed")
		}
	}
}

// TestConstructorDefaultsTimeoutAndDoesNotMutateRedirectPolicy ensures the
// driver's fixed redirect safety does not alter a caller's shared HTTP client.
func TestConstructorDefaultsTimeoutAndDoesNotMutateRedirectPolicy(t *testing.T) {
	t.Parallel()

	client := &http.Client{}

	driver, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
		merchant.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.HTTPClient().Timeout != 30*time.Second || client.Timeout != 0 || client.CheckRedirect != nil {
		t.Fatal("default timeout or borrowed client ownership changed")
	}
}
