// Package postmarkwebhook authenticates Postmark webhook requests using HTTP
// Basic Auth and provides credential-free typed notification definitions.
// New receives outbound notifications; NewInbound receives parsed inbound email
// on a separate route. Neither constructor performs network I/O or needs an API
// token. The application owns bindings, body limits, durable acceptance and
// deduplication through core/http/webhook.
//
// HTTPS is required at the public endpoint. Basic Auth does not sign payloads
// or provide freshness/replay protection; protect credentials and allowlist
// Postmark IP ranges at a trusted edge where possible. Forward Authorization
// unchanged through trusted TLS-terminating proxies. Sender claims in received
// email remain untrusted, regardless of endpoint authentication.
// New and NewInbound reject empty credentials, control characters, and a colon
// in the username with webhook.ErrInvalid. Valid password bytes are not trimmed.
//
// Example:
//
//	source, err := postmarkwebhook.New("webhooks", "endpoint-secret")
//	if err != nil {
//		return err
//	}
//	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[postmarkwebhook.Envelope]) {
//		receiver.Handle(postmarkwebhook.Delivered, func(ctx context.Context, delivery webhook.Delivery[postmarkwebhook.Delivery]) error {
//			return nil // Persist or durably enqueue before returning nil.
//		})
//	})
//	if err != nil {
//		return err
//	}
//	_ = handler
package postmarkwebhook
