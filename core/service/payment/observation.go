package payment

import (
	"context"
	"time"
)

// ObservationKind identifies a portable provider notification meaning.
// Observations are data only; applications bind delivery and fulfillment policy.
//
// Example: _ = payment.Observation{Kind: payment.PaymentSucceeded}
type ObservationKind string

const (
	// ObservationKindUnknown preserves an unmapped notification meaning.
	// It must not be treated as evidence of successful money movement.
	//
	// Example: _ = payment.Observation{Kind: payment.ObservationKindUnknown}
	ObservationKindUnknown ObservationKind = ""
	// CheckoutApproved reports approval, not completed capture or payment.
	// Use explicit completion where the provider requires it.
	//
	// Example: _ = payment.Observation{Kind: payment.CheckoutApproved}
	CheckoutApproved ObservationKind = "checkout_approved"
	// CheckoutCompleted reports completion of the approval flow only.
	// Payment success remains a separate observation.
	//
	// Example: _ = payment.Observation{Kind: payment.CheckoutCompleted}
	CheckoutCompleted ObservationKind = "checkout_completed"
	// PaymentSucceeded reports a successful individual payment attempt.
	// Keep application fulfillment idempotent across duplicate deliveries.
	//
	// Example: _ = payment.Observation{Kind: payment.PaymentSucceeded}
	PaymentSucceeded ObservationKind = "payment_succeeded"
	// PaymentFailed reports a known unsuccessful individual payment attempt.
	// It does not imply that every attempt for an order failed.
	//
	// Example: _ = payment.Observation{Kind: payment.PaymentFailed}
	PaymentFailed ObservationKind = "payment_failed"
	// RefundUpdated reports a refund status change, not necessarily success.
	// NativeStatus carries provider detail for application interpretation.
	//
	// Example: _ = payment.Observation{Kind: payment.RefundUpdated}
	RefundUpdated ObservationKind = "refund_updated"
	// SubscriptionUpdated reports lifecycle change, not invoice settlement.
	// Active subscriptions must not be assumed paid from this event alone.
	//
	// Example: _ = payment.Observation{Kind: payment.SubscriptionUpdated}
	SubscriptionUpdated ObservationKind = "subscription_updated"
	// InvoicePaid reports the provider's paid-invoice observation.
	// It may represent a zero or externally settled invoice, not a new payment.
	//
	// Example: _ = payment.Observation{Kind: payment.InvoicePaid}
	InvoicePaid ObservationKind = "invoice_paid"
	// InvoicePaymentFailed reports a failed invoice-associated payment attempt.
	// Keep InvoiceID to correlate retries without inferring lifecycle status.
	//
	// Example: _ = payment.Observation{Kind: payment.InvoicePaymentFailed}
	InvoicePaymentFailed ObservationKind = "invoice_payment_failed"
	// InvoicePaymentPaid reports a provider invoice-payment settlement event.
	// Preserve its distinction from the invoice's aggregate paid state.
	//
	// Example: _ = payment.Observation{Kind: payment.InvoicePaymentPaid}
	InvoicePaymentPaid ObservationKind = "invoice_payment_paid"
)

// Observation is a small credential-free notification DTO for optional provider
// adapters. It contains no delivery ID or native event type: the webhook delivery
// owns that envelope. Events may be duplicated or out of order; this package
// performs no deduplication, verification, queueing, or fulfillment.
//
// Example: _ = payment.Observation{Kind: payment.PaymentSucceeded, PaymentID: "pi_example"}
type Observation struct {
	// Kind is the portable observation meaning, or unknown if unmapped.
	// Do not infer payment success from approval or lifecycle changes.
	//
	// Example: _ = payment.Observation{Kind: payment.PaymentSucceeded}
	Kind ObservationKind `json:"kind,omitzero"`

	// OccurredAt is the provider event instant, not local receipt time.
	// Zero means the provider did not supply a trustworthy instant.
	//
	// Example: _ = payment.Observation{OccurredAt: time.Unix(1700000000, 0)}
	OccurredAt time.Time `json:"occurred_at,omitzero"`

	// CheckoutID is the related provider checkout, if present in the event.
	// Empty means absent, not permission to infer it from another ID.
	//
	// Example: _ = payment.Observation{CheckoutID: "cs_example"}
	CheckoutID CheckoutID `json:"checkout_id,omitzero"`

	// PaymentID is the related provider payment, if supplied by the event.
	// No network hydration occurs solely to populate it.
	//
	// Example: _ = payment.Observation{PaymentID: "pi_example"}
	PaymentID ID `json:"payment_id,omitzero"`

	// RefundID is the related provider refund, if supplied by the event.
	// It remains distinct from the original payment identity.
	//
	// Example: _ = payment.Observation{RefundID: "re_example"}
	RefundID RefundID `json:"refund_id,omitzero"`

	// SubscriptionID identifies the related subscription, when observed.
	// Its presence does not establish an invoice's settlement status.
	//
	// Example: _ = payment.Observation{SubscriptionID: "sub_example"}
	SubscriptionID SubscriptionID `json:"subscription_id,omitzero"`

	// InvoiceID is a provider invoice identity, when present in the event.
	// There is no invoice lookup or invoice management contract here.
	//
	// Example: _ = payment.Observation{InvoiceID: "in_example"}
	InvoiceID string `json:"invoice_id,omitzero"`

	// Amount optionally reports observed money, with nil meaning unknown.
	// Adapters detach the pointer; callers own the returned observation.
	//
	// Example: _ = payment.Observation{Amount: &payment.Money{Amount: 2500, Currency: "USD"}}
	Amount *Money `json:"amount,omitzero"`

	// NativeStatus is an optional provider resource status token.
	// It is not the provider event type or the entire event payload.
	//
	// Example: _ = payment.Observation{NativeStatus: "succeeded"}
	NativeStatus string `json:"native_status,omitzero"`
}

// Checkout is a snapshot of an approval flow, not proof that money moved.
// Optional relationships and amounts remain absent until observed. Drivers
// return detached records; callers own optional pointers in returned snapshots.
//
// Example:
//
//	checkout := payment.Checkout{ID: "cs_example", Status: payment.CheckoutStatusOpen}
//	_ = checkout.PaymentStatus // unknown, not succeeded
type Checkout struct {
	// ID identifies the provider checkout for reads. Preserve it verbatim.
	//
	// Example: _ = payment.Checkout{ID: "cs_example"}
	ID CheckoutID `json:"id"`

	// Kind distinguishes payment and subscription flows. Zero means unknown.
	//
	// Example: _ = payment.Checkout{Kind: payment.CheckoutKindPayment}
	Kind CheckoutKind `json:"kind,omitzero"`

	// Status describes approval, not payment success. Unknown preserves unmapped states.
	//
	// Example: _ = payment.Checkout{Status: payment.CheckoutStatusOpen}
	Status CheckoutStatus `json:"status,omitzero"`

	// NativeStatus preserves the provider status token, never a response or error payload.
	//
	// Example: _ = payment.Checkout{NativeStatus: "CREATED"}
	NativeStatus string `json:"native_status,omitzero"`

	// ApprovalURL is the optional customer approval location. Never log or instrument it.
	//
	// Example: _ = payment.Checkout{ApprovalURL: "https://pay.example/approve"}
	ApprovalURL string `json:"approval_url,omitzero"`

	// NeedsCompletion requires explicit CompleteCheckout. False does not imply payment success.
	//
	// Example: _ = payment.Checkout{NeedsCompletion: true}
	NeedsCompletion bool `json:"needs_completion,omitzero"`

	// PaymentID identifies an observed payment. Unknown stays empty without hidden hydration.
	//
	// Example: _ = payment.Checkout{PaymentID: "pi_example"}
	PaymentID ID `json:"payment_id,omitzero"`

	// SubscriptionID identifies an observed subscription. Unknown stays empty.
	//
	// Example: _ = payment.Checkout{SubscriptionID: "sub_example"}
	SubscriptionID SubscriptionID `json:"subscription_id,omitzero"`

	// PaymentStatus describes money, not approval. NotRequired permits trials and free flows.
	//
	// Example: _ = payment.Checkout{PaymentStatus: payment.StatusPending}
	PaymentStatus Status `json:"payment_status,omitzero"`

	// Amount reports observed minor units. Nil means unknown; the caller owns the pointer.
	//
	// Example: _ = payment.Checkout{Amount: &payment.Money{Amount: 2500, Currency: "USD"}}
	Amount *Money `json:"amount,omitzero"`

	// Reference correlates application data. It is optional, not an ID or idempotency key.
	//
	// Example: _ = payment.Checkout{Reference: "order-123"}
	Reference string `json:"reference,omitzero"`
}

// Payment is a provider money-movement snapshot, independent of checkout state.
// Refunds are separate resources; NotRequired is not a valid actual payment state.
//
// Example: _ = payment.Payment{ID: "pi_example", Status: payment.StatusSucceeded}
type Payment struct {
	// ID identifies the payment for reads and refunds. It is not a checkout identity.
	//
	// Example: _ = payment.Payment{ID: "pi_example"}
	ID ID `json:"id"`

	// Status describes the payment outcome. Pending and Unknown do not establish success.
	//
	// Example: _ = payment.Payment{Status: payment.StatusSucceeded}
	Status Status `json:"status,omitzero"`

	// NativeStatus preserves the provider status token, never a response or error payload.
	//
	// Example: _ = payment.Payment{NativeStatus: "succeeded"}
	NativeStatus string `json:"native_status,omitzero"`

	// Amount reports integer minor units. Records permit zero, unlike mutation requests.
	//
	// Example: _ = payment.Payment{Amount: payment.Money{Amount: 2500, Currency: "USD"}}
	Amount Money `json:"amount"`

	// SubscriptionID identifies a related subscription. It does not establish invoice payment.
	//
	// Example: _ = payment.Payment{SubscriptionID: "sub_example"}
	SubscriptionID SubscriptionID `json:"subscription_id,omitzero"`
}

// Refund is a snapshot of returned-funds progress against one payment.
// A pending refund does not establish that funds have been returned.
//
// Example: _ = payment.Refund{ID: "re_example", Status: payment.RefundStatusPending}
type Refund struct {
	// ID identifies the refund for reconciliation. It is distinct from the payment ID.
	//
	// Example: _ = payment.Refund{ID: "re_example"}
	ID RefundID `json:"id"`

	// PaymentID identifies the original payment if observed. Preserve provider-family prefixes.
	//
	// Example: _ = payment.Refund{PaymentID: "pi_example"}
	PaymentID ID `json:"payment_id,omitzero"`

	// Status describes this refund, not the payment. Only Succeeded establishes success.
	//
	// Example: _ = payment.Refund{Status: payment.RefundStatusSucceeded}
	Status RefundStatus `json:"status,omitzero"`

	// NativeStatus preserves the provider status token, never a response or error payload.
	//
	// Example: _ = payment.Refund{NativeStatus: "PENDING"}
	NativeStatus string `json:"native_status,omitzero"`

	// Amount reports refund minor units. Negative values never represent a debit.
	//
	// Example: _ = payment.Refund{Amount: payment.Money{Amount: 500, Currency: "USD"}}
	Amount Money `json:"amount"`
}

// Subscription is a provider lifecycle snapshot, not a paid-through ledger.
// Active and NextBillingAt are not proof of payment. The caller owns the optional
// time pointer; drivers must detach it from internal state.
//
// Example: _ = payment.Subscription{ID: "sub_example", Status: payment.SubscriptionStatusActive}
type Subscription struct {
	// ID identifies the subscription for reads and cancellation. It is not a plan reference.
	//
	// Example: _ = payment.Subscription{ID: "sub_example"}
	ID SubscriptionID `json:"id"`

	// PlanReference identifies an observed provider plan or price. Empty means absent.
	//
	// Example: _ = payment.Subscription{PlanReference: "price_monthly"}
	PlanReference string `json:"plan_reference,omitzero"`

	// Status describes lifecycle, not invoice settlement. Applications own entitlement policy.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusActive}
	Status SubscriptionStatus `json:"status,omitzero"`

	// NativeStatus preserves the provider status token, never a response or error payload.
	//
	// Example: _ = payment.Subscription{NativeStatus: "ACTIVE"}
	NativeStatus string `json:"native_status,omitzero"`

	// Reference correlates application data. It is optional, not a customer ID or retry key.
	//
	// Example: _ = payment.Subscription{Reference: "signup-123"}
	Reference string `json:"reference,omitzero"`

	// NextBillingAt is the next billing instant. Nil means unknown, never paid-through time.
	//
	// Example: _ = payment.Subscription{NextBillingAt: nil}
	NextBillingAt *time.Time `json:"next_billing_at,omitzero"`
}

// CheckoutKind distinguishes one-time payment and subscription approval flows.
// Its zero value is unknown, not a payment flow.
//
// Example:
//
//	checkout := payment.Checkout{Kind: payment.CheckoutKindPayment}
//	_ = checkout.Kind
type CheckoutKind string

const (
	// CheckoutKindUnknown preserves a missing or unrecognized checkout kind.
	// Do not infer a payment from this zero value.
	//
	// Example: _ = payment.Checkout{Kind: payment.CheckoutKindUnknown}
	CheckoutKindUnknown CheckoutKind = ""
	// CheckoutKindPayment identifies a one-time payment approval flow.
	// Completion is distinct from money movement.
	//
	// Example: _ = payment.Checkout{Kind: payment.CheckoutKindPayment}
	CheckoutKindPayment CheckoutKind = "payment"
	// CheckoutKindSubscription identifies a subscription approval flow.
	// Its completion need not imply that an invoice has been paid.
	//
	// Example: _ = payment.Checkout{Kind: payment.CheckoutKindSubscription}
	CheckoutKindSubscription CheckoutKind = "subscription"
)

// CheckoutStatus describes the approval flow, not the payment's success.
// Preserve NativeStatus when a provider state cannot be represented.
//
// Example: _ = payment.Checkout{Status: payment.CheckoutStatusOpen}
type CheckoutStatus string

const (
	// CheckoutStatusUnknown preserves an absent or unmapped provider state.
	// It provides no evidence of approval or payment.
	//
	// Example: _ = payment.Checkout{Status: payment.CheckoutStatusUnknown}
	CheckoutStatusUnknown CheckoutStatus = ""
	// CheckoutStatusOpen means the customer can still approve the checkout.
	// It is not evidence that a payment attempt succeeded.
	//
	// Example: _ = payment.Checkout{Status: payment.CheckoutStatusOpen}
	CheckoutStatusOpen CheckoutStatus = "open"
	// CheckoutStatusApproved means customer approval has been observed.
	// NeedsCompletion determines whether explicit completion remains necessary.
	//
	// Example: _ = payment.Checkout{Status: payment.CheckoutStatusApproved}
	CheckoutStatusApproved CheckoutStatus = "approved"
	// CheckoutStatusComplete means the approval flow has completed.
	// Inspect PaymentStatus separately before fulfilling an order.
	//
	// Example: _ = payment.Checkout{Status: payment.CheckoutStatusComplete}
	CheckoutStatusComplete CheckoutStatus = "complete"
	// CheckoutStatusExpired means the provider's approval window ended.
	// It does not describe an independently observed payment's status.
	//
	// Example: _ = payment.Checkout{Status: payment.CheckoutStatusExpired}
	CheckoutStatusExpired CheckoutStatus = "expired"
	// CheckoutStatusCanceled means the approval flow was canceled.
	// It does not imply a refund of previously moved funds.
	//
	// Example: _ = payment.Checkout{Status: payment.CheckoutStatusCanceled}
	CheckoutStatusCanceled CheckoutStatus = "canceled"
)

// Status describes a payment attempt or a checkout payment observation.
// Only Succeeded confirms success; Pending and RequiresAction do not.
//
// Example: _ = payment.Payment{Status: payment.StatusPending}
type Status string

const (
	// StatusUnknown preserves a missing or unmapped payment state.
	// Never interpret it as a successful payment.
	//
	// Example: _ = payment.Payment{Status: payment.StatusUnknown}
	StatusUnknown Status = ""
	// StatusPending means the payment outcome is not final.
	// Applications must wait for a successful observation before fulfillment.
	//
	// Example: _ = payment.Payment{Status: payment.StatusPending}
	StatusPending Status = "pending"
	// StatusRequiresAction means additional customer action is needed.
	// It is neither final success nor final failure.
	//
	// Example: _ = payment.Payment{Status: payment.StatusRequiresAction}
	StatusRequiresAction Status = "requires_action"
	// StatusSucceeded confirms that this payment attempt succeeded.
	// Refunds remain separate resources and do not rewrite this history.
	//
	// Example: _ = payment.Payment{Status: payment.StatusSucceeded}
	StatusSucceeded Status = "succeeded"
	// StatusFailed records a known unsuccessful payment attempt.
	// A later retry may have a distinct provider payment identity.
	//
	// Example: _ = payment.Payment{Status: payment.StatusFailed}
	StatusFailed Status = "failed"
	// StatusCanceled means this payment attempt was canceled.
	// It does not imply a refund of a different successful payment.
	//
	// Example: _ = payment.Payment{Status: payment.StatusCanceled}
	StatusCanceled Status = "canceled"
	// StatusNotRequired is a checkout observation only, for flows such
	// as trials. Drivers must not use it as an actual Payment's status.
	//
	// Example: _ = payment.Checkout{PaymentStatus: payment.StatusNotRequired}
	StatusNotRequired Status = "not_required"
)

// RefundStatus describes a refund's own progress, independently of a payment.
// A submitted or pending refund is not a successful refund.
//
// Example: _ = payment.Refund{Status: payment.RefundStatusPending}
type RefundStatus string

const (
	// RefundStatusUnknown preserves an absent or unmapped refund state.
	// Inspect NativeStatus rather than assuming success.
	//
	// Example: _ = payment.Refund{Status: payment.RefundStatusUnknown}
	RefundStatusUnknown RefundStatus = ""
	// RefundStatusPending means the provider has not finalized the refund.
	// Submission alone does not establish that funds were returned.
	//
	// Example: _ = payment.Refund{Status: payment.RefundStatusPending}
	RefundStatusPending RefundStatus = "pending"
	// RefundStatusRequiresAction means further action is required to refund.
	// It must not be treated as successful funds movement.
	//
	// Example: _ = payment.Refund{Status: payment.RefundStatusRequiresAction}
	RefundStatusRequiresAction RefundStatus = "requires_action"
	// RefundStatusSucceeded means the provider confirmed the refund succeeded.
	// The original Payment retains its own status and identity.
	//
	// Example: _ = payment.Refund{Status: payment.RefundStatusSucceeded}
	RefundStatusSucceeded RefundStatus = "succeeded"
	// RefundStatusFailed means the refund has a known unsuccessful outcome.
	// It differs from an ambiguous mutation transport failure.
	//
	// Example: _ = payment.Refund{Status: payment.RefundStatusFailed}
	RefundStatusFailed RefundStatus = "failed"
	// RefundStatusCanceled means the refund was canceled before success.
	// It does not change the original payment's success status.
	//
	// Example: _ = payment.Refund{Status: payment.RefundStatusCanceled}
	RefundStatusCanceled RefundStatus = "canceled"
)

// SubscriptionStatus describes subscription lifecycle, not invoice settlement.
// Even Active does not prove that the most recent invoice was paid.
//
// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusActive}
type SubscriptionStatus string

const (
	// SubscriptionStatusUnknown preserves an absent or unmapped lifecycle state.
	// It is not evidence that service should be provisioned.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusUnknown}
	SubscriptionStatusUnknown SubscriptionStatus = ""
	// SubscriptionStatusPending means activation is still incomplete.
	// Approval or provider processing may remain outstanding.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusPending}
	SubscriptionStatusPending SubscriptionStatus = "pending"
	// SubscriptionStatusTrialing means the provider reports a trial period.
	// A trial is not evidence of a settled payment.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusTrialing}
	SubscriptionStatusTrialing SubscriptionStatus = "trialing"
	// SubscriptionStatusActive means the provider reports active service.
	// Invoice payment must be established independently.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusActive}
	SubscriptionStatusActive SubscriptionStatus = "active"
	// SubscriptionStatusPastDue means a billing obligation is overdue.
	// The application owns grace periods and entitlement policy.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusPastDue}
	SubscriptionStatusPastDue SubscriptionStatus = "past_due"
	// SubscriptionStatusUnpaid means the provider reports an unpaid subscription.
	// It is distinct from temporary payment retry or processing states.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusUnpaid}
	SubscriptionStatusUnpaid SubscriptionStatus = "unpaid"
	// SubscriptionStatusPaused means billing or service is temporarily paused.
	// The contract does not infer an entitlement decision from this status.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusPaused}
	SubscriptionStatusPaused SubscriptionStatus = "paused"
	// SubscriptionStatusSuspended means the provider suspended the subscription.
	// Preserve NativeStatus for provider-specific causes or recovery rules.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusSuspended}
	SubscriptionStatusSuspended SubscriptionStatus = "suspended"
	// SubscriptionStatusCanceled means the subscription has been canceled.
	// Cancellation does not imply a refund or proration.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusCanceled}
	SubscriptionStatusCanceled SubscriptionStatus = "canceled"
	// SubscriptionStatusExpired means the provider reports the term ended.
	// It carries no inferred paid-through date.
	//
	// Example: _ = payment.Subscription{Status: payment.SubscriptionStatusExpired}
	SubscriptionStatusExpired SubscriptionStatus = "expired"
)

// CheckoutID is an opaque provider checkout identity, scoped to its driver.
// Preserve it verbatim; it is not an application order ID or approval URL.
//
// Example: id := payment.CheckoutID("cs_example"); _ = id
type CheckoutID string

// ID is an opaque provider money-movement identity, scoped to its driver.
// It is not interchangeable with CheckoutID or a provider customer ID.
//
// Example: id := payment.ID("pi_example"); _ = id
type ID string

// RefundID is an opaque provider refund identity, scoped to its driver.
// Keep it separately from the original payment ID for reconciliation.
//
// Example: id := payment.RefundID("re_example"); _ = id
type RefundID string

// SubscriptionID is an opaque provider subscription identity, scoped to its
// driver. It is distinct from the provider's plan or price reference.
//
// Example: id := payment.SubscriptionID("sub_example"); _ = id
type SubscriptionID string

// Validate checks ctx before the portable checkout identifier syntax.
// Nil contexts and invalid IDs wrap ErrInvalidRequest; cancellation is preserved.
//
// Example:
//
//	if err := payment.CheckoutID("cs_example").Validate(ctx); err != nil { return err }
func (checkoutID CheckoutID) Validate(ctx context.Context) error {
	return checkID(ctx, string(checkoutID), "checkout_id")
}

// Validate checks ctx before the portable payment identifier syntax.
// Nil contexts and invalid IDs wrap ErrInvalidRequest; cancellation is preserved.
//
// Example:
//
//	if err := payment.ID("pi_example").Validate(ctx); err != nil { return err }
func (id ID) Validate(ctx context.Context) error {
	return checkID(ctx, string(id), "payment_id")
}

// Validate checks ctx before the portable refund identifier syntax.
// Nil contexts and invalid IDs wrap ErrInvalidRequest; cancellation is preserved.
//
// Example:
//
//	if err := payment.RefundID("re_example").Validate(ctx); err != nil { return err }
func (refundID RefundID) Validate(ctx context.Context) error {
	return checkID(ctx, string(refundID), "refund_id")
}

// Validate checks ctx before the portable subscription identifier syntax.
// Nil contexts and invalid IDs wrap ErrInvalidRequest; cancellation is preserved.
//
// Example:
//
//	if err := payment.SubscriptionID("sub_example").Validate(ctx); err != nil { return err }
func (subscriptionID SubscriptionID) Validate(ctx context.Context) error {
	return checkID(ctx, string(subscriptionID), "subscription_id")
}
