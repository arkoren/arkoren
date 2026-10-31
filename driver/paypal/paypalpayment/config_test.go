package paypalpayment_test

import (
	"net/http"
	"testing"
	"time"

	"arkoren.dev/driver/paypal/paypalpayment"
)

// TestNegativeTimeoutUsesFiniteDefault checks normalization on a private copy.
func TestNegativeTimeoutUsesFiniteDefault(t *testing.T) {
	t.Parallel()

	client := &http.Client{Timeout: -time.Second}

	backend, err := paypalpayment.New("id", "secret", func(merchant *paypalpayment.Builder) {
		merchant.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	if backend.HTTPClient().Timeout != 30*time.Second {
		t.Fatalf("timeout = %v, want 30s", backend.HTTPClient().Timeout)
	}

	if client.Timeout != -time.Second {
		t.Fatal("constructor changed caller timeout")
	}
}

// TestPositiveTimeoutIsPreserved keeps the caller's finite request bound.
func TestPositiveTimeoutIsPreserved(t *testing.T) {
	t.Parallel()

	client := &http.Client{Timeout: time.Second}

	backend, err := paypalpayment.New("id", "secret", func(merchant *paypalpayment.Builder) {
		merchant.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	if backend.HTTPClient().Timeout != time.Second {
		t.Fatalf("timeout = %v, want 1s", backend.HTTPClient().Timeout)
	}
}
