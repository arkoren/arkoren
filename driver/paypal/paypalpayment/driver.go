package paypalpayment

import (
	"net/http"

	"arkoren.dev/driver/paypal/internal/paypal"
)

// Driver implements all ten payment operations and is safe for concurrent use.
// It owns a cancelable token cache; no constructor or background goroutine does
// I/O. Requests never retry automatically, including authentication failures.
//
// Example:
//
//	backend, err := paypalpayment.New("id", "secret")
//	if err != nil {
//		return err
//	}
//	_ = payment.New(backend)
type Driver struct {
	// client owns credentials and bounded authenticated API dispatch.
	client *paypal.Client
}

// Error contains safe native Code, Issue, DebugID and HTTP Status fields.
// Its message never prints provider body text; errors.As exposes underlying
// transport/decoding causes. Do not log those causes without redaction.
//
// Example:
//
//	var native *paypalpayment.Error
//	if errors.As(err, &native) {
//		_ = native.DebugID
//	}
type Error = paypal.Error

// New validates nonblank client credentials after applying callbacks exactly
// once, in order. It performs no I/O. Nil callbacks panic; empty slices do not.
// Sandbox is the default; use Builder.Live for production. HTTP clients are
// copied, not closed, and redirect following is disabled even when injected.
// Example:
//
//	backend, err := paypalpayment.New("client-id", "client-secret")
//	if err != nil {
//		return err
//	}
//	_ = payment.New(backend)
func New(clientID, clientSecret string, configure ...BuilderFunc) (*Driver, error) {
	merchant := &Builder{}

	for _, configure := range configure {
		configure(merchant)
	}

	client, err := paypal.New(clientID, clientSecret, merchant.settings)

	if err != nil {
		return nil, err
	}

	return &Driver{client: client}, nil
}

// HTTPClient returns the actual HTTP client, not an SDK. Treat it as read-only
// during use; mutating it can race requests or bypass credential protections.
// Example: _ = backend.HTTPClient().Timeout
func (driver *Driver) HTTPClient() *http.Client {
	return driver.client.HTTPClient()
}
