package molliepayment

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/mollie/molliepayment/internal/resource"
)

// String redacts the complete driver configuration, including the owned API key
// and callback destination. It does not inspect or change the borrowed HTTP
// transport. Value receivers protect diagnostics of both Driver and *Driver.
//
// Example:
//
//	backend, err := molliepayment.New("test_example")
//	if err != nil {
//		return err
//	}
//	_ = fmt.Sprint(backend) // Driver{REDACTED}
func (driver Driver) String() string {
	return "Driver{REDACTED}"
}

// GoString redacts expanded Go-syntax formatting just as String does. Private
// fields are not a formatting security boundary; no credential or native client
// contents are returned, even when a diagnostic requests the full Go value.
//
// Example:
//
//	backend, err := molliepayment.New("test_example")
//	if err != nil {
//		return err
//	}
//	_ = fmt.Sprintf("%#v", backend) // Driver{REDACTED}
func (driver Driver) GoString() string {
	return driver.String()
}

// LogValue supplies a redacted slog value independently of the selected text or
// JSON handler. It never serializes the driver or follows the borrowed client's
// internals; direct access to HTTPClient still retains its documented ownership.
//
// Example:
//
//	backend, err := molliepayment.New("test_example")
//	if err != nil {
//		return err
//	}
//
//	slog.InfoContext(ctx, "payment backend", "driver", backend)
func (driver Driver) LogValue() slog.Value {
	return slog.StringValue(driver.String())
}

// defaultTimeout bounds requests even when the caller has no deadline or
// supplies a client whose timeout is zero or negative.
const defaultTimeout = 30 * time.Second

// Driver implements the complete payment contract using Mollie's Payments v2
// API. Only hosted creditcard, automatic-capture, one-off payments and explicit
// refunds are supported. Construction performs no network calls. Concurrent
// calls are safe when the borrowed HTTP transport is concurrency-safe.
// Formatting and slog redact the complete owned configuration. HTTPClient still
// exposes native HTTP policy under its separately documented ownership rules.
//
// Example:
//
//	backend, err := molliepayment.New("test_example")
//	if err != nil {
//		return err
//	}
//	client := payment.New(backend)
//	_ = client
type Driver struct {
	// apiKey authenticates fixed-origin requests; no accessor exposes it.
	apiKey string
	// http owns client policy but borrows its underlying transport and jar.
	http *http.Client
	// webhookURL is a published callback destination, not a receiver or route.
	webhookURL string
}

// New validates a profile API key and settings without contacting Mollie.
// Keys must have a test_ or live_ prefix and an alphanumeric suffix; their
// prefix selects test or live mode at Mollie's single fixed API origin.
// Callbacks run once in order, including for invalid keys; nil callbacks panic.
// Never use a live key in tests. OAuth and organization credentials are outside
// this driver's scope.
//
// Example:
//
//	backend, err := molliepayment.New("test_example")
//	if err != nil {
//		return err
//	}
//	_ = payment.New(backend)
func New(apiKey string, configure ...BuilderFunc) (*Driver, error) {
	merchant := &Builder{}

	for _, policy := range configure {
		policy(merchant)
	}

	if !validKey(apiKey) {
		return nil, errors.New("mollie profile API key is required")
	}

	if merchant.webhookURL != "" && !resource.ValidHTTPS(merchant.webhookURL) {
		return nil, errors.New("mollie webhook URL must be absolute HTTPS")
	}

	client := &http.Client{Timeout: defaultTimeout}

	if merchant.client != nil {
		*client = *merchant.client
	}

	if client.Timeout <= 0 {
		client.Timeout = defaultTimeout
	}

	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Driver{apiKey: apiKey, http: client, webhookURL: merchant.webhookURL}, nil
}

// HTTPClient returns the actual client used for requests. It is owned by the
// driver; the underlying transport and jar remain caller-owned and are never
// closed by this package. Do not mutate this client concurrently with requests
// or weaken its redirect policy. The accessor does not expose the API key.
//
// Example:
//
//	backend, err := molliepayment.New("test_example")
//	if err != nil {
//		return err
//	}
//	_ = backend.HTTPClient().Timeout
func (driver *Driver) HTTPClient() *http.Client {
	return driver.http
}

// validKey accepts only profile-scoped mode keys. Validation never echoes the
// secret, and bounds the header before any HTTP request can be constructed.
func validKey(key string) bool {
	suffix, found := strings.CutPrefix(key, "test_")

	if !found {
		suffix, found = strings.CutPrefix(key, "live_")
	}

	return found && resource.ValidToken(suffix)
}

// The compile-time assignment keeps unsupported methods part of the complete
// contract rather than optional capability interfaces.
var _ payment.Driver = (*Driver)(nil)
