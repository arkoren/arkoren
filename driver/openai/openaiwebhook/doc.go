// Package openaiwebhook authenticates OpenAI webhook notifications with the
// official SDK and publishes credential-free response lifecycle definitions.
// It performs no network I/O and requires an endpoint signing secret, not an
// API key. Routing, handlers, body limits and deduplication belong to webhook.
//
// Example:
//
//	source, err := openaiwebhook.New(secret)
//	if err != nil {
//		return err
//	}
//	endpoint, err := webhook.Handler(source, func(receiver *webhook.Builder[openaiwebhook.Envelope]) {
//		receiver.Handle(openaiwebhook.ResponseCompleted, accept)
//	})
//	if err != nil {
//		return err
//	}
//	_ = endpoint
//
// Authentication precedes JSON parsing and enforces the SDK's five-minute
// timestamp window in both directions. Delivery.ID is the signed webhook-id
// header, OpenAI's documented idempotency key; Envelope.ID is the JSON event ID.
// Freshness alone does not prevent replay within the window: configure core
// deduplication and make application effects idempotent. Invalid signatures
// wrap webhook.ErrRejected; malformed authenticated envelopes or bound payloads
// produce 503. Authenticated unbound events are acknowledged without decoding
// their data. Notifications carry response references, not generated output;
// fetching the response is an explicit application responsibility.
package openaiwebhook
