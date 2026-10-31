// Package mailgunwebhook verifies Mailgun JSON event webhook tokens and
// publishes credential-free, typed event definitions. It does not receive
// inbound email Routes forms or Alerts webhooks, and needs no sending API key.
//
// Mailgun signs only timestamp + token, NOT event-data or the raw body. Use
// HTTPS and protect the receiving transport; a captured valid signature can
// accompany altered event data until it expires. Freshness is not one-use token
// protection. Application deduplication suppresses repeated event identities,
// not token reuse with changed data. Do not use these events alone to authorize
// security-sensitive changes. Subaccounts must supply their own signing key;
// the optional parent-signature is not used.
// New rejects empty signing keys, whitespace and control characters with
// webhook.ErrInvalid rather than trimming or repairing credential bytes.
//
// Example:
//
//	source, err := mailgunwebhook.New(signingKey)
//	if err != nil {
//		return err
//	}
//	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[mailgunwebhook.Envelope]) {
//		receiver.Handle(mailgunwebhook.Delivered, acceptDelivered)
//	})
//	if err != nil {
//		return err
//	}
//	routes.Post("/webhooks/mailgun", handler)
package mailgunwebhook
