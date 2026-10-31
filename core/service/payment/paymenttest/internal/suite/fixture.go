package suite

import (
	"context"

	"arkoren.dev/core/service/payment"
)

// Fixture projects one public harness without changing or invoking its hooks.
// The factory retains lifecycle ownership and is called once per scenario.
type Fixture struct {
	// Driver implements the complete outbound payment contract under test.
	Driver payment.Driver

	// Amount is the optional fixture-supported one-off checkout amount.
	Amount payment.Money

	// PlanReference identifies an existing provider or deterministic fixture plan.
	PlanReference string

	// Approve simulates approval without implying settlement.
	Approve func(context.Context, payment.CheckoutID) error

	// ResolvePaymentID performs one bounded observation of delayed identity.
	ResolvePaymentID func(context.Context, payment.CheckoutID) (payment.ID, error)

	// ResolvePayment makes the chosen payment observable as succeeded.
	ResolvePayment func(context.Context, payment.ID) error

	// ActivateSubscription supplies lifecycle state, not invoice settlement.
	ActivateSubscription func(context.Context, payment.SubscriptionID) error
}

// Policy projects only declared fixture limitations. Validation still checks
// every operation, including those explicitly declared unsupported.
type Policy struct {
	// WithoutApproval omits journeys that require hosted customer approval.
	WithoutApproval bool

	// WithoutCompletion requires valid completion to report unsupported.
	WithoutCompletion bool

	// WithoutRefunds requires explicit refund creation to report unsupported.
	WithoutRefunds bool

	// WithoutRefundLookup requires valid refund reads to report unsupported.
	WithoutRefundLookup bool

	// WithoutSubscriptions omits journeys and checks unsupported operations.
	WithoutSubscriptions bool

	// WithoutPreparation requires valid initialization to report unsupported.
	WithoutPreparation bool
}
