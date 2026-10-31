package payment

import "context"

// Driver supplies the full portable payment surface. Unsupported operations
// return ErrUnsupported, not optional interfaces. Implementations are safe for
// concurrent use, validate requests and ctx before effects, preserve native
// statuses, and return detached snapshots. GET operations are pure reads.
// Successful mutations use their own response, never post-success hydration.
// A preflight lifecycle read may guard an invalid mutation; a documented
// no-effect state conflict may permit one reconciliation read. Neither permits
// polling or replaying a mutation after an ambiguous result.
//
// Map provider failures to package sentinels while preserving errors.Is.
// A dispatched mutation with an ambiguous result returns ErrOutcomeUnknown,
// including transport cancellation after dispatch when success is uncertain.
// Before dispatch, cancellation remains context.Canceled or DeadlineExceeded.
// Error text must not expose credentials, payloads, approval URLs, or references.
// Idempotency remains provider-scoped and retention-bounded, not exactly once.
//
// Example:
//
//	var backend payment.Driver = driver
//	client := payment.New(backend)
//	_ = client.Driver()
type Driver interface {
	// PreparePayment initializes a client authorization, order, or session for a
	// positive explicit amount. It does not confirm, capture, or complete hosted
	// checkout. Returned references are optional: a session-only response need
	// not identify any financial resource and there is no universal preparation
	// lookup. Return only provider-observed references and a valid required Action.
	// Validate context/request before I/O, never hydrate a successful mutation,
	// and preserve ErrOutcomeUnknown after an ambiguous dispatch. Unsupported
	// drivers validate first and return ErrUnsupported without effects.
	//
	// Example:
	//	prepared, err := backend.PreparePayment(ctx, request)
	//	if err != nil { return err }
	//	_ = prepared.Action.Kind()
	PreparePayment(context.Context, PreparePaymentRequest) (Preparation, error)

	// CreateCheckout starts one-time approval for a positive explicit amount.
	// It does not complete the checkout or read another resource afterwards.
	//
	// Example:
	//	checkout, err := backend.CreateCheckout(ctx, request)
	//	if err != nil { return err }
	//	_ = checkout.ApprovalURL
	CreateCheckout(context.Context, CheckoutRequest) (Checkout, error)

	// CreateSubscriptionCheckout starts approval of an existing provider plan.
	// It does not create catalog entries or infer successful invoice payment.
	//
	// Example:
	//	checkout, err := backend.CreateSubscriptionCheckout(ctx, request)
	//	if err != nil { return err }
	//	_ = checkout.ApprovalURL
	CreateSubscriptionCheckout(context.Context, SubscriptionCheckoutRequest) (Checkout, error)

	// GetCheckout observes a checkout without capture, activation, or completion.
	// Approval and payment status remain separate; missing fields stay absent.
	//
	// Example:
	//	checkout, err := backend.GetCheckout(ctx, payment.CheckoutID("cs_example"))
	//	if err != nil { return err }
	//	_ = checkout.NeedsCompletion
	GetCheckout(context.Context, CheckoutID) (Checkout, error)

	// CompleteCheckout explicitly performs provider-required completion after
	// approval. Unsupported or invalid-state flows return the matching sentinel.
	//
	// Example:
	//	checkout, err := backend.CompleteCheckout(ctx, request)
	//	if err != nil { return err }
	//	_ = checkout.PaymentStatus
	CompleteCheckout(context.Context, CompleteCheckoutRequest) (Checkout, error)

	// GetPayment reads one provider payment without initiating money movement.
	// Pending is not success; NotRequired is reserved for checkout observations.
	//
	// Example:
	//	result, err := backend.GetPayment(ctx, payment.ID("pi_example"))
	//	if err != nil { return err }
	//	_ = result.Status
	GetPayment(context.Context, ID) (Payment, error)

	// RefundPayment requests a positive explicit refund without implicit balance
	// lookup. Providers enforce currency, available balance, and idempotency.
	//
	// Example:
	//	refund, err := backend.RefundPayment(ctx, request)
	//	if err != nil { return err }
	//	_ = refund.ID
	RefundPayment(context.Context, RefundRequest) (Refund, error)

	// GetRefund reads the refund's own progress without resubmitting it.
	// A pending refund is not a successful returned-funds observation.
	//
	// Example:
	//	refund, err := backend.GetRefund(ctx, payment.RefundID("re_example"))
	//	if err != nil { return err }
	//	_ = refund.Status
	GetRefund(context.Context, RefundID) (Refund, error)

	// GetSubscription observes lifecycle without activation or invoice settlement.
	// Active and NextBillingAt do not imply a paid-through date.
	//
	// Example:
	//	subscription, err := backend.GetSubscription(ctx, payment.SubscriptionID("sub_example"))
	//	if err != nil { return err }
	//	_ = subscription.Status
	GetSubscription(context.Context, SubscriptionID) (Subscription, error)

	// CancelSubscription immediately cancels without refunds or proration.
	// It performs no post-success read and no scheduled end-of-period change.
	// A preflight lifecycle check or one documented no-effect conflict read is
	// allowed; an ambiguous dispatch must not trigger a read or replay.
	//
	// Example:
	//	if err := backend.CancelSubscription(ctx, request); err != nil { return err }
	CancelSubscription(context.Context, CancelSubscriptionRequest) error
}
