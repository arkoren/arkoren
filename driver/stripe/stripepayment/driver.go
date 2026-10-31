package stripepayment

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/stripe/stripepayment/internal/protocol"
)

// Driver uses a shared Stripe client without lifecycle resources of its own.
// It is safe for concurrent use when the native client is not mutated.
//
// Example:
//
//	backend, err := stripepayment.New("sk_test_example")
//	if err != nil { return err }
//	var service payment.Driver = backend
//	_ = service
type Driver struct {
	// client is retained; native policies must not change during concurrent use.
	client *stripe.Client
}

// New validates key and constructs a network-free Stripe client. It disables
// redirects, SDK retries, standard HTTP mutation replays, telemetry, and logging.
// Requests have a finite default timeout. Redirect refusal prevents forwarding
// credentials to another origin. Custom transports must not replay independently.
// Keys must be nonempty and contain no whitespace or controls. Nothing is closed
// by the driver; a supplied HTTP client remains caller-owned.
// Invalid keys return before callbacks. With a valid key, callbacks run directly
// once, synchronously in order on one draft; nil entries and panics propagate.
// HTTP settings are validated afterwards, and completed settings are detached
// from the draft without copying the borrowed transport or jar.
//
// Example:
//
//	backend, err := stripepayment.New("sk_test_example")
//	if err != nil { return err }
//	_ = payment.New(backend)
func New(key string, configure ...BuilderFunc) (*Driver, error) {
	if key == "" || strings.ContainsFunc(key, protocol.CredentialSpace) {
		return nil, fmt.Errorf("stripe payment: %w: api key", payment.ErrInvalidRequest)
	}

	connection := &Builder{}

	for _, apply := range configure {
		apply(connection)
	}

	client := http.Client{Timeout: defaultTimeout}

	if connection.client != nil {
		client = *connection.client
	}

	if client.Timeout < 0 {
		return nil, fmt.Errorf("stripe payment: %w: http timeout", payment.ErrInvalidRequest)
	}

	if client.Timeout == 0 {
		client.Timeout = defaultTimeout
	}

	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client.Transport = protocol.NewTransport(client.Transport)
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		HTTPClient:        &client,
		MaxNetworkRetries: stripe.Int64(0),
		EnableTelemetry:   stripe.Bool(false),
		LeveledLogger:     &stripe.LeveledLogger{Level: stripe.LevelNull},
	})

	return &Driver{client: stripe.NewClient(key, stripe.WithBackends(backends))}, nil
}

// NewFrom borrows an initialized native client and performs no network I/O.
// It preserves all caller policies, including redirects, retries, timeouts,
// logging, and telemetry. The caller owns the client and must disable both SDK
// and HTTP-level mutation retries if one-attempt behavior is required. Nil returns
// ErrInvalidRequest; the owned-client mutation replay boundary is not installed.
//
// Example:
//
//	native := stripe.NewClient("sk_test_example")
//	backend, err := stripepayment.NewFrom(native)
//	if err != nil { return err }
//	_ = backend.Client() == native
func NewFrom(client *stripe.Client) (*Driver, error) {
	if client == nil {
		return nil, fmt.Errorf("stripe payment: %w: client", payment.ErrInvalidRequest)
	}

	return &Driver{client: client}, nil
}

// Client exposes the retained SDK client for Stripe-specific operations. Caller
// changes to it affect the driver; do not mutate it during concurrent requests.
// The driver never closes it or changes policies on a NewFrom client.
//
// Example:
//
//	backend, err := stripepayment.New("sk_test_example")
//	if err != nil { return err }
//	native := backend.Client()
//	_ = native.V1PaymentIntents
func (driver *Driver) Client() *stripe.Client {
	return driver.client
}
