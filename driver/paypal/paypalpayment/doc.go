// Package paypalpayment implements payment.Driver with PayPal Orders v2,
// Payments v2 captures/refunds, and Billing Subscriptions v1. Construction is
// network-free and sandbox is the default. Select production explicitly.
//
// Example:
//
//	backend, err := paypalpayment.New("client-id", "client-secret")
//	if err != nil {
//		return err
//	}
//	client := payment.New(backend)
//	_ = client
//
// Checkout IDs retain order: or subscription: prefixes; payment IDs retain
// capture: or sale:. Preserve them verbatim. Native subscription IDs keep I-.
// Sale observations are useful for reconciliation, but sale lookup and refund
// return payment.ErrUnsupported; this package does not guess deprecated routes.
//
// PreparePayment creates plain one-off orders for the native PayPal SDK without
// backend return/cancel URLs or approval links. Empty, iOS and Android channels
// share this profile; web and nonempty ReturnURL return payment.ErrUnsupported.
// Optional DeviceReference supplies bounded visible-ASCII SDK risk data only to
// order creation. SDK callbacks do not prove approval or payment: completion
// still observes provider approval before a separate explicit merchant capture.
//
// Subscription approval does not require an activate call. Completion observes
// approval once, without polling. Active subscriptions and next billing times
// never establish payment or entitlement. Applications own webhook binding,
// reconciliation, fulfillment, retries and idempotency retention policy.
// Malformed successful GET JSON and unusable OAuth responses wrap ErrUnavailable
// with safe error text and retained causes, never ErrOutcomeUnknown. Successful
// mutation decoding failures retain their separate outcome-unknown policy.
package paypalpayment
