// Package resendwebhook authenticates Resend's inbound Svix notifications and
// publishes credential-free typed email event definitions. It requires only an
// endpoint signing secret, independently of resendmail or a Resend API client.
// No network calls, routes, queues, cache or application handlers are created.
// New requires a whsec_ secret with unbroken standard base64; malformed or
// line-broken credentials return webhook.ErrInvalid and are never repaired.
//
// Example:
//
//	source, err := resendwebhook.New("whsec_c2lnbmluZy1zZWNyZXQ=")
//	if err != nil {
//		return err
//	}
//	accept := func(ctx context.Context, delivery webhook.Delivery[resendwebhook.Email]) error {
//		return emails.RecordDelivery(ctx, delivery.Payload.EmailID)
//	}
//	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
//		receiver.Handle(resendwebhook.EmailDelivered, accept)
//	})
//	if err != nil {
//		return err
//	}
//	_ = handler
//
// Authentication uses the original body and an attempt timestamp within five
// minutes of local time. Keep clocks synchronized. The signed svix-id remains
// stable across retries; the email_id is not a webhook identity. Authentication
// alone does not stop replays inside that window: configure core webhook's
// deduplication and make application effects idempotent. Unknown authenticated
// event types are acknowledged by core; malformed authenticated envelopes or
// bound email data answer 503. Invalid signatures answer 400. Email metadata
// contains personal information and is not safe for routine logs or metrics.
package resendwebhook
