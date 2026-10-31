package molliepayment

import "net/http"

// Builder configures a fresh driver without I/O. New copies its completed
// settings; retaining the draft cannot change a published driver. HTTP
// transports remain shared, caller-owned objects.
//
// Example:
//
//	backend, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
//		merchant.WebhookURL("https://shop.example/mollie")
//	})
//	if err != nil {
//		return err
//	}
//	_ = backend
type Builder struct {
	// client supplies borrowed transport settings, copied during publication.
	client *http.Client
	// webhookURL is the optional classic payment status callback destination.
	webhookURL string
}

// BuilderFunc applies construction policy once, synchronously in argument
// order. Nil entries panic; expanding an empty or nil slice applies no edits.
//
// Example:
//
//	var policy molliepayment.BuilderFunc = func(merchant *molliepayment.Builder) {
//		merchant.HTTPClient(&http.Client{Timeout: time.Second})
//	}
//	backend, err := molliepayment.New("test_example", policy)
//	if err != nil {
//		return err
//	}
//	_ = backend
type BuilderFunc = func(*Builder)

// HTTPClient borrows the supplied client's transport, jar and timeout. New
// copies the client, disables redirects and defaults nonpositive timeouts to
// thirty seconds. Nil selects standard defaults. The driver never closes the
// transport; custom transports must not retry mutations or log credentials.
//
// Example:
//
//	backend, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
//		merchant.HTTPClient(&http.Client{Timeout: 10 * time.Second})
//	})
//	if err != nil {
//		return err
//	}
//	_ = backend
func (merchant *Builder) HTTPClient(client *http.Client) *Builder {
	merchant.client = client

	return merchant
}

// WebhookURL replaces the optional classic callback URL. Empty disables
// callbacks; nonempty values must be absolute HTTPS without credentials or
// fragments. Mollie must be able to reach it. Callbacks contain unsigned ID
// hints, never authenticated payment status; see the package security notes.
//
// Example:
//
//	backend, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
//		merchant.WebhookURL("https://shop.example/mollie")
//	})
//	if err != nil {
//		return err
//	}
//	_ = backend
func (merchant *Builder) WebhookURL(destination string) *Builder {
	merchant.webhookURL = destination

	return merchant
}
