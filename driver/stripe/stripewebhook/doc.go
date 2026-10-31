// Package stripewebhook authenticates Stripe snapshot events independently of
// stripepayment. The application binds credential-free event definitions to a
// core webhook.Handler; no endpoint, queue, cache, or handler is registered here.
//
// Example:
//
//	source, err := stripewebhook.New("whsec_example")
//	if err != nil { return err }
//	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[stripewebhook.Envelope]) {
//		receiver.Handle(stripewebhook.PaymentIntentSucceeded, accept)
//	})
//	if err != nil { return err }
//	_ = handler
//
// Signatures are checked before JSON parsing. Bound event decoders require the
// SDK's exact Stripe API version and snapshot resource schema; failures answer
// 503 through core webhook. Authenticated unbound events are acknowledged by core
// webhook, including events with other API versions. Thin notifications and
// network hydration are not supported. Invoice paid, checkout completion, and
// subscription activity do not establish a newly collected payment.
// Envelope.CreatedAt preserves native created Unix seconds, while portable
// observations expose OccurredAt; the native JSON member remains unchanged.
package stripewebhook
