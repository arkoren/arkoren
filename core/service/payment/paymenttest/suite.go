package paymenttest

import (
	"context"
	"testing"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/paymenttest/internal/suite"
)

// Harness supplies a fresh driver and deterministic approval/settlement hooks.
// Hooks belong to the test fixture, not payment.Driver. They may update a local
// protocol server or call a simulator control, but must not wait for a real user.
// Run requires payment hooks unless WithoutApproval is selected. Subscription
// hooks and a plan are unnecessary with WithoutSubscriptions. Hooks execute once
// per journey; they must be bounded and deterministic, not arbitrary polling.
//
// Example:
//
//	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
//		driver := memorypayment.New()
//		return paymenttest.Harness{
//			Driver: driver, PlanReference: "plan-monthly", Approve: driver.ApproveCheckout,
//			ResolvePayment: func(ctx context.Context, id payment.ID) error {
//				return driver.ResolvePayment(ctx, id, payment.StatusSucceeded)
//			},
//			ActivateSubscription: func(ctx context.Context, id payment.SubscriptionID) error {
//				return driver.ResolveSubscription(ctx, id, payment.SubscriptionStatusActive, nil)
//			},
//		}
//	})
type Harness struct {
	// Driver is the complete outbound contract under test, never a partial mock.
	// Example: harness := paymenttest.Harness{Driver: memorypayment.New()}; _ = harness
	Driver payment.Driver
	// Amount configures the one-off checkout in integer minor units. The zero
	// value uses 1200 USD for compatibility; a supplied value must be positive
	// and supported by the fixture. Refunds use the observed payment currency.
	// Example: harness := paymenttest.Harness{Amount: payment.Money{Amount: 1200, Currency: "EUR"}}; _ = harness
	Amount payment.Money
	// PlanReference is an existing provider plan or deterministic fixture plan.
	// Example: harness := paymenttest.Harness{PlanReference: "plan-monthly"}; _ = harness
	PlanReference string
	// Approve simulates approval only; it must not claim successful settlement.
	// Example: harness := paymenttest.Harness{Approve: driver.ApproveCheckout}; _ = harness
	Approve func(context.Context, payment.CheckoutID) error
	// ResolvePaymentID observes a delayed payment identity after approval and
	// completion when the checkout has no PaymentID yet. Omit it when the
	// checkout already exposes the identity. The hook runs once and must use
	// bounded fixture control, not wait for an actual customer or poll forever.
	// Example: id, err := harness.ResolvePaymentID(ctx, checkout.ID); if err != nil { return err }; _ = id
	ResolvePaymentID func(context.Context, payment.CheckoutID) (payment.ID, error)
	// ResolvePayment makes the selected payment observable as succeeded.
	// Example: if err := harness.ResolvePayment(ctx, id); err != nil { return err }
	ResolvePayment func(context.Context, payment.ID) error
	// ActivateSubscription makes the selected subscription active, not paid.
	// Example: if err := harness.ActivateSubscription(ctx, id); err != nil { return err }
	ActivateSubscription func(context.Context, payment.SubscriptionID) error
}

// Option declares an explicit suite limitation, never alternate contract
// semantics. Only package-supplied options can configure the private settings.
//
// Example:
//
//	paymenttest.Run(t, factory, paymenttest.WithoutApproval())
type Option func(*settings)

// settings records fixture scope and declared unsupported operations.
type settings struct {
	// withoutApproval omits journeys, not validation or unsupported assertions.
	withoutApproval bool
	// withoutCompletion requires valid completion calls to be unsupported.
	withoutCompletion bool
	// withoutRefunds requires valid refund calls to be unsupported.
	withoutRefunds bool
	// withoutRefundLookup requires valid refund reads to be unsupported.
	withoutRefundLookup bool
	// withoutSubscriptions requires all three subscription operations to be unsupported.
	withoutSubscriptions bool
	// withoutPreparation requires valid initialization calls to be unsupported.
	withoutPreparation bool
}

// WithoutApproval explicitly omits journeys when a fixture cannot approve a
// checkout. Context and request validation still run for every operation, as do
// standalone unsupported subscription and refund-lookup checks. Completion and
// refund-creation checks need the approved payment journey and are omitted.
// This option does not declare any provider operation unsupported.
//
// Example:
//
//	paymenttest.Run(t, factory, paymenttest.WithoutApproval())
func WithoutApproval() Option {
	return func(config *settings) {
		config.withoutApproval = true
	}
}

// WithoutCompletion declares that CompleteCheckout returns ErrUnsupported for
// valid input. Journeys require the approved checkout not to need completion.
// The suite tests that unsupported completion leaves its checkout unchanged.
//
// Example:
//
//	paymenttest.Run(t, factory, paymenttest.WithoutCompletion())
func WithoutCompletion() Option {
	return func(config *settings) {
		config.withoutCompletion = true
	}
}

// WithoutRefunds declares that RefundPayment returns ErrUnsupported. The suite
// verifies that a valid refund attempt does not change the original payment.
//
// Example:
//
//	paymenttest.Run(t, factory, paymenttest.WithoutRefunds())
func WithoutRefunds() Option {
	return func(config *settings) {
		config.withoutRefunds = true
	}
}

// WithoutRefundLookup declares that GetRefund returns ErrUnsupported for valid
// identities. Refund creation is still tested unless WithoutRefunds is also
// selected; the suite never infers refund success from the original payment.
//
// Example:
//
//	paymenttest.Run(t, factory, paymenttest.WithoutRefundLookup())
func WithoutRefundLookup() Option {
	return func(config *settings) {
		config.withoutRefundLookup = true
	}
}

// WithoutSubscriptions declares that subscription checkout creation, lookup,
// and cancellation return ErrUnsupported for valid input without producing a
// resource. One-off payment journeys still run. No plan or activation hook is
// required, even when the fixture cannot perform hosted approval.
//
// Example:
//
//	paymenttest.Run(t, factory, paymenttest.WithoutSubscriptions())
func WithoutSubscriptions() Option {
	return func(config *settings) {
		config.withoutSubscriptions = true
	}
}

// WithoutPreparation declares that PreparePayment returns ErrUnsupported for
// valid input without effects. Its context and request validation still run.
// Preparation checks are independent of hosted approval, refunds, and plans.
//
// Example:
//
//	paymenttest.Run(t, factory, paymenttest.WithoutPreparation())
func WithoutPreparation() Option {
	return func(config *settings) {
		config.withoutPreparation = true
	}
}

// Run checks each operation's validation boundary, then payment and subscription
// journeys using fresh drivers. The factory owns resource cleanup through t.
// It must not return a shared mutable fixture. WithoutApproval explicitly limits
// coverage for transport fixtures that cannot perform hosted customer approval.
//
// Example:
//
//	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
//		return paymenttest.Harness{Driver: configuredDriver(t)}
//	}, paymenttest.WithoutApproval())
func Run(t *testing.T, factory func(*testing.T) Harness, options ...Option) {
	t.Helper()

	config := settings{}

	for _, option := range options {
		option(&config)
	}

	suite.Run(t, func(child *testing.T) suite.Fixture {
		return projectFixture(factory(child))
	}, suite.Policy{
		WithoutApproval:      config.withoutApproval,
		WithoutCompletion:    config.withoutCompletion,
		WithoutRefunds:       config.withoutRefunds,
		WithoutRefundLookup:  config.withoutRefundLookup,
		WithoutSubscriptions: config.withoutSubscriptions,
		WithoutPreparation:   config.withoutPreparation,
	})
}

// projectFixture copies handles and callbacks without invoking or owning them.
// The public Harness remains the declared contract for consumer test factories.
func projectFixture(harness Harness) suite.Fixture {
	return suite.Fixture{
		Driver:               harness.Driver,
		Amount:               harness.Amount,
		PlanReference:        harness.PlanReference,
		Approve:              harness.Approve,
		ResolvePaymentID:     harness.ResolvePaymentID,
		ResolvePayment:       harness.ResolvePayment,
		ActivateSubscription: harness.ActivateSubscription,
	}
}
