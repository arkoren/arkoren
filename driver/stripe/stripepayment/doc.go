// Package stripepayment implements payment.Driver with Stripe hosted Checkout,
// PaymentIntents, refunds, and existing recurring Prices. Construction performs
// no I/O; no worker, webhook endpoint, or local subscription state is required.
//
// Checkout completion only reads Stripe: hosted Checkout collects payment itself.
// A complete checkout is not proof of payment. Proven zero-total paid sessions
// report StatusNotRequired, not a newly collected payment. Payment IDs are pi_ IDs;
// checkout, refund, and subscription IDs retain their cs_, re_, and sub_ families.
// PreparePayment is the common alternative to hosted Checkout: it
// prepares a card-backed PaymentIntent for native Stripe SDK confirmation, without
// customers or saved cards. Its client secret is sensitive: JSON is intended only
// for an authorized HTTPS response; formatting and slog redact it. Nonempty
// ReturnURL and DeviceReference are unsupported by this guest intent profile;
// configure the client SDK return URL on the client. Omitted, web, iOS, and Android
// channels use the same server-side initialization protocol. Native SDK
// completion is not fulfillment authority; read GetPayment or verify webhooks.
//
// Native example:
//
//	amount, err := payment.NewMoney("USD", 2500)
//	if err != nil { return err }
//	client := payment.New(backend)
//	prepared, err := client.PreparePayment(ctx, payment.NewPreparePaymentRequest(amount, "order-123"))
//	if err != nil { return err }
//	authorization, ok := prepared.Action.Authorization()
//	if !ok { return payment.ErrInvalidState }
//	_ = authorization.Token // send only to this order's authorized native SDK
//
// Example:
//
//	backend, err := stripepayment.New("sk_test_example")
//	if err != nil { return err }
//	client := payment.New(backend)
//	_ = client.Driver()
//
// New refuses redirects and disables SDK retries, standard HTTP mutation replays,
// telemetry, and logging. Custom transports must not independently replay writes.
// NewFrom preserves SDK and HTTP policies; callers must configure those themselves.
// Unknown mutation outcomes, including an idempotency_key_in_use conflict, require
// reconciliation, not automatic replay. Idempotency is Stripe-scoped
// and retention-bounded. Refunds first read the PaymentIntent to check currency;
// no operation reads again after a successful mutation.
package stripepayment
