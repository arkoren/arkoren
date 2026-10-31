package paypalpayment

import (
	"net/http"

	"arkoren.dev/driver/paypal/internal/paypal"
)

// Builder configures one immutable driver publication. Retaining this draft
// cannot change a driver after New returns; transports remain shared objects.
//
// Example:
//
//	backend, err := paypalpayment.New("id", "secret", func(merchant *paypalpayment.Builder) {
//		merchant.Live(true)
//	})
//	if err != nil {
//		return err
//	}
//	_ = backend
type Builder struct {
	// settings is copied into a private client after callbacks finish.
	settings paypal.Settings
}

// BuilderFunc configures a driver once, synchronously in call order. A nil
// entry panics; an expanded empty or nil slice applies no changes.
//
// Example:
//
//	var configure paypalpayment.BuilderFunc = func(merchant *paypalpayment.Builder) {
//		merchant.Live(true)
//	}
//	backend, err := paypalpayment.New("id", "secret", configure)
//	if err != nil {
//		return err
//	}
//	_ = backend
type BuilderFunc = func(*Builder)

// Live selects production when enabled; false restores the sandbox default.
// It changes only the fixed API origin, not the provider's account permissions.
//
// Example:
//
//	backend, err := paypalpayment.New("id", "secret", func(merchant *paypalpayment.Builder) {
//		merchant.Live(true)
//	})
//	if err != nil {
//		return err
//	}
//	_ = backend
func (merchant *Builder) Live(enabled bool) *Builder {
	merchant.settings.Live = enabled

	return merchant
}

// HTTPClient borrows the client's transport, jar and timeout. New copies the
// client, forbids redirects and replaces a nonpositive timeout with thirty seconds.
// Nil selects the default client. The driver never closes the transport.
//
// Example:
//
//	backend, err := paypalpayment.New("id", "secret", func(merchant *paypalpayment.Builder) {
//		merchant.HTTPClient(&http.Client{Timeout: time.Second})
//	})
//	if err != nil {
//		return err
//	}
//	_ = backend
func (merchant *Builder) HTTPClient(client *http.Client) *Builder {
	merchant.settings.HTTPClient = client

	return merchant
}
