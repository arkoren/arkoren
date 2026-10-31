// Package molliepayment implements payment.Driver using Mollie's Payments v2
// REST API, the standard HTTP client and no SDK dependency. It supports hosted
// creditcard one-off payments with automatic capture, payment/checkout reads,
// and explicit partial/full refunds with independent refund reads. Your profile
// must enable creditcard; Mollie enforces method, currency and amount limits.
// Other methods, native SDK preparation, manual capture, customer/mandate setup
// and subscriptions are deliberately unsupported. PreparePayment validates
// context and input before returning ErrUnsupported without HTTP. No
// constructor performs network I/O.
//
// Create a test-mode backend and use the portable facade:
//
//	backend, err := molliepayment.New("test_example", func(merchant *molliepayment.Builder) {
//		merchant.WebhookURL("https://shop.example/mollie")
//	})
//	if err != nil {
//		return err
//	}
//	client := payment.New(backend)
//	_ = client
//
// # Outcomes and retries
//
// Checkout IDs and payment IDs both contain Mollie's tr_ identity. Refund IDs
// are opaque compound identities carrying tr_ and re_ tokens; preserve them
// verbatim. Snapshots are detached, response-only observations. Missing required
// mutation response fields wrap payment.ErrOutcomeUnknown, as do cancellation
// after dispatch, transport failure, redirects, 5xx and in-flight conflicts.
// Pre-dispatch cancellation does not imply an unknown outcome. Accepted pending
// refunds are normal results. Neither redirects nor mutations are replayed.
//
// Mollie caches Idempotency-Key responses for one hour per API credential.
// Persist your request and key before dispatch; reuse both unchanged for an
// application-controlled retry within that window. Rotating credentials or
// retrying after expiry can create a second effect. Never blindly retry an
// ambiguous refund with a new key. The driver does not manage a durable ledger.
//
// # Classic callback security
//
// Classic payment callbacks are unsigned application/x-www-form-urlencoded
// POSTs containing an id hint, not an authenticated event. This package does
// not fabricate a webhook.Source or install routes. Your application bounds
// the request body, validates exactly one id, matches it to its own persisted
// payment/account, and calls GetPayment with the appropriate merchant key.
// Act only on that authenticated snapshot and commit deduplicated processing
// before acknowledging. A return-page redirect is not proof of payment either.
// Rate-limit unsolicited hints and acknowledge unknown IDs without disclosing
// order information. Refund callbacks carry the payment ID: reconcile refund
// IDs you have persisted using GetRefund. Next-gen webhooks are outside scope.
//
// HTTPClient exposes the actual client; do not weaken redirect policy or mutate
// it concurrently. Borrowed transports are never closed. Custom transports
// must not retry mutations or log credentials. Error strings omit response
// bodies and transport text; explicitly unwrapped causes can contain secrets.
// Driver fmt/slog diagnostics redact the complete owned configuration, including
// the API key and callback destination, without inspecting borrowed HTTP objects.
// Malformed successful GET JSON and unusable GET snapshots wrap ErrUnavailable,
// not ErrOutcomeUnknown. Mutation failures retain the uncertainty policy above.
package molliepayment
