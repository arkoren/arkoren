package paymenttest_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/memorypayment"
	"arkoren.dev/core/service/payment/paymenttest"
)

// TestSuiteDeclaredUnsupportedOperations verifies suite options assert errors
// and no effects instead of treating unsupported operations as silent success.
func TestSuiteDeclaredUnsupportedOperations(t *testing.T) {
	t.Parallel()

	paymenttest.Run(t, scriptedHarness, paymenttest.WithoutCompletion(), paymenttest.WithoutRefunds())
}

// scriptedHarness models an auto-completing fixture whose refund and explicit
// completion operations are unsupported. Each factory call owns its own queues.
func scriptedHarness(t *testing.T) paymenttest.Harness {
	t.Helper()

	fake := paymenttest.New()
	fake.ReplyPreparePayment(payment.Preparation{
		Action: payment.NewClientSession("fixture.session", "session-1", "opaque-data"),
	}, nil)

	checkout := payment.Checkout{
		ID: "cs_test", Status: payment.CheckoutStatusComplete, PaymentID: "pi_test", SubscriptionID: "sub_test",
	}
	fake.ReplyCreateCheckout(payment.Checkout{ID: "cs_test", Status: payment.CheckoutStatusOpen}, nil)
	fake.ReplyGetCheckout(checkout, nil)
	fake.ReplyGetCheckout(checkout, nil)
	fake.ReplyCompleteCheckout(payment.Checkout{}, payment.ErrUnsupported)

	result := payment.Payment{ID: "pi_test", Status: payment.StatusSucceeded,
		Amount: payment.Money{Amount: 1200, Currency: "USD"}}
	fake.ReplyGetPayment(result, nil)
	fake.ReplyGetPayment(result, nil)
	fake.ReplyRefundPayment(payment.Refund{}, payment.ErrUnsupported)
	fake.ReplyCreateSubscriptionCheckout(payment.Checkout{
		ID: "cs_test", Status: payment.CheckoutStatusOpen, SubscriptionID: "sub_test",
	}, nil)
	fake.ReplyGetSubscription(payment.Subscription{ID: "sub_test", Status: payment.SubscriptionStatusActive}, nil)
	fake.ReplyCancelSubscription(nil)
	fake.ReplyCancelSubscription(nil)
	fake.ReplyGetSubscription(payment.Subscription{ID: "sub_test", Status: payment.SubscriptionStatusCanceled}, nil)

	return paymenttest.Harness{
		Driver: fake, PlanReference: "plan-test",
		Approve: func(context.Context, payment.CheckoutID) error {
			return nil
		},
		ResolvePayment: func(context.Context, payment.ID) error {
			return nil
		},
		ActivateSubscription: func(context.Context, payment.SubscriptionID) error {
			return nil
		},
	}
}

// hostedOnlyDriver models a provider that cannot prepare native client handoffs.
// Embedding forwards the complete existing contract, never a capability subset.
type hostedOnlyDriver struct {
	// Driver supplies hosted operations and simulator controls unchanged.
	*memorypayment.Driver
	// preparations counts valid unsupported calls, excluding preflight failures.
	preparations int
}

// PreparePayment validates unsupported preparation before reporting no effect.
// It does not fall back to creating a hosted checkout or pending payment.
func (driver *hostedOnlyDriver) PreparePayment(
	ctx context.Context, request payment.PreparePaymentRequest,
) (payment.Preparation, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Preparation{}, err
	}

	driver.preparations++

	return payment.Preparation{}, payment.ErrUnsupported
}

// TestSuiteWithoutPreparationChecksValidUnsupportedIndependently requires the
// unsupported assertion even when hosted approval and subscription setup omit
// journeys. Validation remains required and must not count as dispatch.
func TestSuiteWithoutPreparationChecksValidUnsupportedIndependently(t *testing.T) {
	t.Parallel()

	var drivers []*hostedOnlyDriver

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		t.Helper()

		driver := &hostedOnlyDriver{Driver: memorypayment.New()}
		drivers = append(drivers, driver)

		return paymenttest.Harness{Driver: driver}
	}, paymenttest.WithoutApproval(), paymenttest.WithoutPreparation())

	preparations := 0

	for _, driver := range drivers {
		preparations += driver.preparations
	}

	if preparations != 1 {
		t.Fatal("unsupported preparation assertion omitted")
	}
}

// TestUnsupportedPreparationContextPrecedesRequestAndCapability checks the new
// operation on a provider with no native support, before any fixture mutation.
func TestUnsupportedPreparationContextPrecedesRequestAndCapability(t *testing.T) {
	t.Parallel()

	driver := &hostedOnlyDriver{Driver: memorypayment.New()}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.PreparePayment(ctx, payment.PreparePaymentRequest{})

	if !errors.Is(err, context.Canceled) || driver.preparations != 0 {
		t.Fatal("unsupported preparation bypassed preflight")
	}
}

// oneOffDriver models a provider with no subscription API while preserving the
// memory driver's validation and one-off ledger, including idempotency.
type oneOffDriver struct {
	// Driver supplies the unchanged operations and in-process fixture controls.
	*memorypayment.Driver
	// subscriptionCreates counts valid unsupported creation attempts only.
	subscriptionCreates int
	// subscriptionReads counts valid unsupported lookups independently of mutations.
	subscriptionReads int
	// subscriptionCancels counts valid unsupported cancellation attempts only.
	subscriptionCancels int
}

// CreateSubscriptionCheckout validates even unsupported calls before returning
// no resource. It never delegates to the simulator's subscription mutation.
func (driver *oneOffDriver) CreateSubscriptionCheckout(
	ctx context.Context, request payment.SubscriptionCheckoutRequest,
) (payment.Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Checkout{}, err
	}

	driver.subscriptionCreates++

	return payment.Checkout{}, payment.ErrUnsupported
}

// GetSubscription accepts valid identities without claiming to read a resource
// or using a partial capability interface to hide the unsupported operation.
func (driver *oneOffDriver) GetSubscription(
	ctx context.Context, id payment.SubscriptionID,
) (payment.Subscription, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Subscription{}, err
	}

	driver.subscriptionReads++

	return payment.Subscription{}, payment.ErrUnsupported
}

// CancelSubscription rejects valid cancellations without creating or changing
// subscription state. Context and request errors still take precedence.
func (driver *oneOffDriver) CancelSubscription(ctx context.Context, request payment.CancelSubscriptionRequest) error {
	if err := request.Validate(ctx); err != nil {
		return err
	}

	driver.subscriptionCancels++

	return payment.ErrUnsupported
}

// oneOffHarness needs no plan or subscription activation control. Its EUR amount
// catches a suite that assumes a USD-only checkout or refund currency.
func oneOffHarness(t *testing.T) paymenttest.Harness {
	t.Helper()

	driver := &oneOffDriver{Driver: memorypayment.New()}

	return paymenttest.Harness{
		Driver: driver, Amount: payment.Money{Amount: 1200, Currency: "EUR"},
		Approve: driver.ApproveCheckout,
		ResolvePayment: func(ctx context.Context, id payment.ID) error {
			return driver.ResolvePayment(ctx, id, payment.StatusSucceeded)
		},
	}
}

// TestSuiteWithoutSubscriptionsStillRunsEuroPayments requires the complete
// one-off journey while omitting all subscription fixture setup.
func TestSuiteWithoutSubscriptionsStillRunsEuroPayments(t *testing.T) {
	t.Parallel()

	paymenttest.Run(t, oneOffHarness, paymenttest.WithoutSubscriptions())
}

// TestSuiteWithoutApprovalStillAssertsUnsupportedSubscriptions separates a
// fixture's inability to approve from provider operations that are unsupported.
func TestSuiteWithoutApprovalStillAssertsUnsupportedSubscriptions(t *testing.T) {
	t.Parallel()

	var drivers []*oneOffDriver

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		t.Helper()

		driver := &oneOffDriver{Driver: memorypayment.New()}
		drivers = append(drivers, driver)

		return paymenttest.Harness{Driver: driver}
	}, paymenttest.WithoutApproval(), paymenttest.WithoutSubscriptions())

	creates, reads, cancels := 0, 0, 0

	for _, driver := range drivers {
		creates += driver.subscriptionCreates
		reads += driver.subscriptionReads
		cancels += driver.subscriptionCancels
	}

	if creates != 2 || reads != 2 {
		t.Fatalf("unsupported creates=%d reads=%d, want two of each", creates, reads)
	}

	if cancels != 2 {
		t.Fatalf("unsupported cancellations=%d, want two", cancels)
	}
}

// sparseRefundDriver exposes pending refunds with no optional parent identity.
// The underlying memory ledger still enforces amount, currency and balance.
type sparseRefundDriver struct {
	// Driver provides all other operations without additional behavior switches.
	payment.Driver
}

// RefundPayment retains the requested amount and refund identity while modeling
// a provider that accepts refunds asynchronously and omits the parent field.
func (driver *sparseRefundDriver) RefundPayment(
	ctx context.Context, request payment.RefundRequest,
) (payment.Refund, error) {
	refund, err := driver.Driver.RefundPayment(ctx, request)
	refund.PaymentID = ""
	refund.Status = payment.RefundStatusPending

	return refund, err
}

// GetRefund preserves the refund's own progress without synthesizing a parent
// relationship; no suite hook or polling is needed for a pending outcome.
func (driver *sparseRefundDriver) GetRefund(ctx context.Context, id payment.RefundID) (payment.Refund, error) {
	refund, err := driver.Driver.GetRefund(ctx, id)
	refund.PaymentID = ""
	refund.Status = payment.RefundStatusPending

	return refund, err
}

// TestSuiteAcceptsPendingRefundWithoutObservedParent prevents an optional
// relationship or asynchronous progress from becoming a conformance requirement.
func TestSuiteAcceptsPendingRefundWithoutObservedParent(t *testing.T) {
	t.Parallel()

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		harness := oneOffHarness(t)
		harness.Driver = &sparseRefundDriver{Driver: harness.Driver}

		return harness
	}, paymenttest.WithoutSubscriptions())
}

// noRefundLookupDriver models a narrow provider that creates refunds but cannot
// read their own resources through this API. All other operations remain intact.
type noRefundLookupDriver struct {
	// Driver supplies actual memory-backed refunds and their balance accounting.
	payment.Driver
}

// GetRefund validates the caller boundary before explicitly refusing lookup.
// It neither consults the ledger nor invents a successful refund observation.
func (driver *noRefundLookupDriver) GetRefund(ctx context.Context, id payment.RefundID) (payment.Refund, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	return payment.Refund{}, payment.ErrUnsupported
}

// TestSuiteDeclaresUnsupportedRefundLookup still exercises refund creation,
// independently from the unavailable lookup route.
func TestSuiteDeclaresUnsupportedRefundLookup(t *testing.T) {
	t.Parallel()

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		harness := oneOffHarness(t)
		harness.Driver = &noRefundLookupDriver{Driver: harness.Driver}

		return harness
	}, paymenttest.WithoutSubscriptions(), paymenttest.WithoutRefundLookup())
}

// delayedIdentityDriver models a checkout whose payment identity arrives via
// another bounded observation, not in its checkout response.
type delayedIdentityDriver struct {
	// Driver keeps the underlying ledger and complete nine-operation contract.
	payment.Driver
}

// GetCheckout omits only the optional payment identity. Amount ownership and
// approval semantics still come directly from the memory implementation.
func (driver *delayedIdentityDriver) GetCheckout(ctx context.Context, id payment.CheckoutID) (payment.Checkout, error) {
	checkout, err := driver.Driver.GetCheckout(ctx, id)
	checkout.PaymentID = ""

	return checkout, err
}

// CompleteCheckout also omits the delayed identity without changing the
// completion result or initiating a second payment.
func (driver *delayedIdentityDriver) CompleteCheckout(
	ctx context.Context, request payment.CompleteCheckoutRequest,
) (payment.Checkout, error) {
	checkout, err := driver.Driver.CompleteCheckout(ctx, request)
	checkout.PaymentID = ""

	return checkout, err
}

// TestSuiteResolvesDelayedIdentityOnce uses one explicit fixture observation
// rather than provider polling or assuming the checkout always contains an ID.
func TestSuiteResolvesDelayedIdentityOnce(t *testing.T) {
	t.Parallel()

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		harness := oneOffHarness(t)
		driver := harness.Driver
		harness.Driver = &delayedIdentityDriver{Driver: driver}
		calls := 0
		harness.ResolvePaymentID = func(ctx context.Context, id payment.CheckoutID) (payment.ID, error) {
			calls++

			if calls > 1 {
				t.Fatal("payment identity fixture was polled")
			}

			checkout, err := driver.GetCheckout(ctx, id)

			return checkout.PaymentID, err
		}

		return harness
	}, paymenttest.WithoutSubscriptions())
}
