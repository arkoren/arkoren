package paypalwebhook

import (
	"net/http"

	"arkoren.dev/driver/paypal/internal/paypal"
)

// Builder configures one webhook source before publication. Settings are
// detached after callbacks; HTTP transports remain borrowed and shared.
//
// Example:
//
//	source, err := paypalwebhook.New("id", "secret", "hook", func(listener *paypalwebhook.Builder) {
//		listener.Live(true)
//	})
//	if err != nil {
//		return err
//	}
//	_ = source
type Builder struct {
	// settings is copied into the source's private account client.
	settings paypal.Settings
}

// BuilderFunc runs synchronously once in order during New. Nil entries panic;
// expanded nil or empty slices apply no configuration.
//
// Example:
//
//	var configure paypalwebhook.BuilderFunc = func(listener *paypalwebhook.Builder) {
//		listener.Live(true)
//	}
//	source, err := paypalwebhook.New("id", "secret", "hook", configure)
//	if err != nil {
//		return err
//	}
//	_ = source
type BuilderFunc = func(*Builder)

// Live explicitly selects production; false restores the sandbox default.
// Payment drivers and webhook sources have independent environment settings.
//
// Example:
//
//	source, err := paypalwebhook.New("id", "secret", "hook", func(listener *paypalwebhook.Builder) {
//		listener.Live(true)
//	})
//	if err != nil {
//		return err
//	}
//	_ = source
func (listener *Builder) Live(enabled bool) *Builder {
	listener.settings.Live = enabled

	return listener
}

// HTTPClient borrows a transport for remote verification and OAuth. New copies
// the client, disables redirects, and replaces a nonpositive timeout with thirty
// seconds. Nil uses the standard transport. This package closes no transport.
//
// Example:
//
//	source, err := paypalwebhook.New("id", "secret", "hook", func(listener *paypalwebhook.Builder) {
//		listener.HTTPClient(&http.Client{Timeout: time.Second})
//	})
//	if err != nil {
//		return err
//	}
//	_ = source
func (listener *Builder) HTTPClient(client *http.Client) *Builder {
	listener.settings.HTTPClient = client

	return listener
}
