// Package paypalwebhook authenticates PayPal notifications with the remote
// verify-webhook-signature API and publishes credential-free event definitions.
// It is independent of paypalpayment; constructing either never constructs the
// other. Fixed sandbox/live origins receive credentials; certificate URLs are
// forwarded as verification data and are never fetched by this package.
//
// Example:
//
//	source, err := paypalwebhook.New("client-id", "secret", "webhook-id")
//	if err != nil {
//		return err
//	}
//	endpoint, err := webhook.Handler(source, func(listener *webhook.Builder[paypalwebhook.Envelope]) {
//		listener.Handle(paypalwebhook.PaymentCaptureCompleted, accept)
//	})
//	if err != nil {
//		return err
//	}
//	_ = endpoint
//
// Authentication precedes routing, including unbound events. Remote failures
// return ordinary errors (503); signature FAILURE wraps webhook.ErrRejected.
// Stable provider event IDs, not transmission IDs, identify deliveries. Event
// conversion is pure and does not fetch resources, infer entitlements, register
// handlers, deduplicate deliveries or advance a renewal engine.
package paypalwebhook
