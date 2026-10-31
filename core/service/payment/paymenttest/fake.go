package paymenttest

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/payment"
)

// ErrUnexpectedCall identifies a valid operation with no queued reply.
// It prevents an unconfigured fake from reporting invented provider success.
//
// Example:
//
//	_, err := paymenttest.New().GetPayment(ctx, "pi_test")
//	if !errors.Is(err, paymenttest.ErrUnexpectedCall) { return err }
var ErrUnexpectedCall = errors.New("unexpected payment call")

// Fake scripts each operation independently without a provider ledger, network,
// or automatic approval. It is safe for concurrent use. Requests are validated
// before histories or failures change; valid attempts are always recorded.
// FailNext takes precedence over FailWith and neither consumes queued replies.
//
// Example:
//
//	fake := paymenttest.New()
//	fake.ReplyGetPayment(payment.Payment{ID: "pi_test", Status: payment.StatusPending}, nil)
//	result, err := fake.GetPayment(ctx, "pi_test")
//	if err != nil { return err }
//	_ = result.Status
type Fake struct {
	// mutex protects every reply queue, history, and injected failure.
	mutex sync.Mutex
	// failNext overrides the next valid operation only.
	failNext error
	// failWith overrides valid operations until cleared.
	failWith error
	// prepare scripts client handoffs independently of hosted checkout and reads.
	prepare script[payment.PreparePaymentRequest, payment.Preparation]
	// create scripts one-time checkout creation.
	create script[payment.CheckoutRequest, payment.Checkout]
	// subscribe scripts subscription checkout creation.
	subscribe script[payment.SubscriptionCheckoutRequest, payment.Checkout]
	// checkout scripts checkout reads independently of mutations.
	checkout script[payment.CheckoutID, payment.Checkout]
	// complete scripts explicit completion responses.
	complete script[payment.CompleteCheckoutRequest, payment.Checkout]
	// payment scripts payment reads independently of completion.
	payment script[payment.ID, payment.Payment]
	// refund scripts explicit refund responses.
	refund script[payment.RefundRequest, payment.Refund]
	// returned scripts refund reads independently of submission.
	returned script[payment.RefundID, payment.Refund]
	// subscription scripts subscription reads independently of approval.
	subscription script[payment.SubscriptionID, payment.Subscription]
	// cancel scripts immediate cancellation outcomes.
	cancel script[payment.CancelSubscriptionRequest, struct{}]
}

// script keeps a typed operation's queue and histories together. Access requires
// the owning Fake mutex; requests contain no mutable containers or pointers.
type script[Request, Result any] struct {
	// replies are consumed FIFO only when no injected failure wins.
	replies []reply[Result]
	// attempted includes every valid invocation, including injected failures.
	attempted []Request
	// accepted includes replies explicitly reporting acceptance.
	accepted []Request
}

// reply can report an accepted operation alongside an ambiguous error. A
// nonempty scripted result identity reports acceptance, not settled funds.
type reply[Result any] struct {
	// result is copied when queued and before it is returned to callers.
	result Result
	// err is the scripted provider outcome, preserved for errors.Is.
	err error
	// accepted distinguishes an accepted effect from a mere attempted call.
	accepted bool
}

// History is a detached snapshot of operation-specific request lists in call
// order per operation. Attempted includes valid failures; Accepted includes
// scripted successes and nonempty result IDs even when paired with an error.
// This makes ambiguous accepted effects explicit without inventing a ledger.
//
// Example:
//
//	fake := paymenttest.New()
//	_ = len(fake.Attempted().CreatedCheckouts)
type History struct {
	// PreparedPayments contains client-initialization requests in operation order.
	// Acceptance means scripted initialization, never confirmed payment.
	// Example: _ = len(fake.Accepted().PreparedPayments)
	PreparedPayments []payment.PreparePaymentRequest
	// CreatedCheckouts contains one-time checkout requests.
	// Example: _ = len(fake.Attempted().CreatedCheckouts)
	CreatedCheckouts []payment.CheckoutRequest
	// CreatedSubscriptionCheckouts contains existing-plan approval requests.
	// Example: _ = len(fake.Attempted().CreatedSubscriptionCheckouts)
	CreatedSubscriptionCheckouts []payment.SubscriptionCheckoutRequest
	// ReadCheckouts contains identities passed to GetCheckout.
	// Example: _ = len(fake.Attempted().ReadCheckouts)
	ReadCheckouts []payment.CheckoutID
	// CompletedCheckouts contains explicit completion requests.
	// Example: _ = len(fake.Accepted().CompletedCheckouts)
	CompletedCheckouts []payment.CompleteCheckoutRequest
	// ReadPayments contains identities passed to GetPayment.
	// Example: _ = len(fake.Attempted().ReadPayments)
	ReadPayments []payment.ID
	// RefundedPayments contains positive explicit refund requests.
	// Example: _ = len(fake.Accepted().RefundedPayments)
	RefundedPayments []payment.RefundRequest
	// ReadRefunds contains identities passed to GetRefund.
	// Example: _ = len(fake.Attempted().ReadRefunds)
	ReadRefunds []payment.RefundID
	// ReadSubscriptions contains identities passed to GetSubscription.
	// Example: _ = len(fake.Attempted().ReadSubscriptions)
	ReadSubscriptions []payment.SubscriptionID
	// CanceledSubscriptions contains immediate cancellation requests.
	// Example: _ = len(fake.Accepted().CanceledSubscriptions)
	CanceledSubscriptions []payment.CancelSubscriptionRequest
}

// New constructs an empty scripted fake. A zero Fake is also ready for use.
// Queue replies before calling an operation or it returns ErrUnexpectedCall.
//
// Example:
//
//	fake := paymenttest.New()
//	_ = payment.New(fake)
func New() *Fake {
	return &Fake{}
}

// takeReply records a valid attempt and consumes one FIFO response. Injected
// failures override scripts without consuming them or recording acceptance.
// Callers hold the Fake mutex and detach mutable response pointers afterwards.
func takeReply[Request, Result any](
	queue *script[Request, Result], request Request, failure error,
) (Result, error) {
	queue.attempted = append(queue.attempted, request)

	if failure != nil {
		return *new(Result), failure
	}

	if len(queue.replies) == 0 {
		return *new(Result), ErrUnexpectedCall
	}

	response := queue.replies[0]
	queue.replies[0] = reply[Result]{}
	queue.replies = queue.replies[1:]

	if response.accepted {
		queue.accepted = append(queue.accepted, request)
	}

	return response.result, response.err
}

// takeFailure consumes the one-shot override before consulting persistent
// failures. Only valid operations reach it, while the caller holds mutex.
func (fake *Fake) takeFailure() error {
	if err := fake.failNext; err != nil {
		fake.failNext = nil

		return err
	}

	return fake.failWith
}

// FailNext makes the next valid operation fail before consuming any script.
// Invalid calls do not consume the failure; nil clears a pending override.
//
// Example:
//
//	fake := paymenttest.New()
//	fake.FailNext(payment.ErrUnavailable)
func (fake *Fake) FailNext(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.failNext = err
}

// FailWith overrides every valid operation until FailWith(nil). A pending
// FailNext still wins once, and queued replies remain untouched.
//
// Example:
//
//	fake := paymenttest.New()
//	fake.FailWith(payment.ErrUnavailable)
//	fake.FailWith(nil)
func (fake *Fake) FailWith(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.failWith = err
}

// Reset clears all queued replies, both histories, and failures atomically.
// Already returned snapshots remain caller-owned and unchanged.
//
// Example:
//
//	fake := paymenttest.New()
//	fake.Reset()
func (fake *Fake) Reset() {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.create = script[payment.CheckoutRequest, payment.Checkout]{}
	fake.prepare = script[payment.PreparePaymentRequest, payment.Preparation]{}
	fake.subscribe = script[payment.SubscriptionCheckoutRequest, payment.Checkout]{}
	fake.checkout = script[payment.CheckoutID, payment.Checkout]{}
	fake.complete = script[payment.CompleteCheckoutRequest, payment.Checkout]{}
	fake.payment = script[payment.ID, payment.Payment]{}
	fake.refund = script[payment.RefundRequest, payment.Refund]{}
	fake.returned = script[payment.RefundID, payment.Refund]{}
	fake.subscription = script[payment.SubscriptionID, payment.Subscription]{}
	fake.cancel = script[payment.CancelSubscriptionRequest, struct{}]{}
	fake.failNext, fake.failWith = nil, nil
}

// Attempted returns copied typed requests for every valid call, including
// injected failures and unexpected calls. Ordering is per operation, not global.
//
// Example:
//
//	fake := paymenttest.New()
//	_ = len(fake.Attempted().ReadPayments)
func (fake *Fake) Attempted() History {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	return History{
		PreparedPayments:             slices.Clone(fake.prepare.attempted),
		CreatedCheckouts:             slices.Clone(fake.create.attempted),
		CreatedSubscriptionCheckouts: slices.Clone(fake.subscribe.attempted),
		ReadCheckouts:                slices.Clone(fake.checkout.attempted),
		CompletedCheckouts:           slices.Clone(fake.complete.attempted),
		ReadPayments:                 slices.Clone(fake.payment.attempted),
		RefundedPayments:             slices.Clone(fake.refund.attempted),
		ReadRefunds:                  slices.Clone(fake.returned.attempted),
		ReadSubscriptions:            slices.Clone(fake.subscription.attempted),
		CanceledSubscriptions:        slices.Clone(fake.cancel.attempted),
	}
}

// Accepted returns copied typed requests whose scripts reported acceptance.
// A reply with a nonempty ID and ErrOutcomeUnknown appears here and in Attempted;
// that records a simulated accepted effect, not a claim of successful settlement.
//
// Example:
//
//	fake := paymenttest.New()
//	_ = len(fake.Accepted().RefundedPayments)
func (fake *Fake) Accepted() History {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()

	return History{
		PreparedPayments:             slices.Clone(fake.prepare.accepted),
		CreatedCheckouts:             slices.Clone(fake.create.accepted),
		CreatedSubscriptionCheckouts: slices.Clone(fake.subscribe.accepted),
		ReadCheckouts:                slices.Clone(fake.checkout.accepted),
		CompletedCheckouts:           slices.Clone(fake.complete.accepted),
		ReadPayments:                 slices.Clone(fake.payment.accepted),
		RefundedPayments:             slices.Clone(fake.refund.accepted),
		ReadRefunds:                  slices.Clone(fake.returned.accepted),
		ReadSubscriptions:            slices.Clone(fake.subscription.accepted),
		CanceledSubscriptions:        slices.Clone(fake.cancel.accepted),
	}
}

// cloneCheckout detaches optional amounts at script capture and return, keeping
// later caller mutations from changing another invocation's queued response.
func cloneCheckout(checkout payment.Checkout) payment.Checkout {
	if checkout.Amount != nil {
		amount := *checkout.Amount
		checkout.Amount = &amount
	}

	return checkout
}

// cloneSubscription detaches the optional billing instant at both boundaries.
// The fake retains no caller-owned mutable time pointers.
func cloneSubscription(subscription payment.Subscription) payment.Subscription {
	if subscription.NextBillingAt != nil {
		nextBillingAt := *subscription.NextBillingAt
		subscription.NextBillingAt = &nextBillingAt
	}

	return subscription
}

// assertRecorded requires one request satisfying every predicate. Empty
// predicates match any accepted request; no request is always a failure.
func assertRecorded[Request any](t testing.TB, requests []Request, predicates []func(Request) bool) {
	t.Helper()

	matched := slices.ContainsFunc(requests, func(request Request) bool {
		return !slices.ContainsFunc(predicates, func(predicate func(Request) bool) bool {
			return !predicate(request)
		})
	})

	if !matched {
		t.Errorf("no accepted payment request matches predicates (%d accepted)", len(requests))
	}
}

// AssertCreatedCheckout requires an accepted one-time checkout matching every
// predicate. It examines acceptance, not failed dispatch attempts.
//
// Example:
//
//	fake.AssertCreatedCheckout(t, func(request payment.CheckoutRequest) bool {
//		return request.Reference() == "order-1"
//	})
func (fake *Fake) AssertCreatedCheckout(t testing.TB, predicates ...func(payment.CheckoutRequest) bool) {
	t.Helper()

	assertRecorded(t, fake.Accepted().CreatedCheckouts, predicates)
}

// AssertCreatedSubscriptionCheckout requires an accepted existing-plan checkout
// matching every predicate. With no predicates any accepted request matches.
//
// Example:
//
//	fake.AssertCreatedSubscriptionCheckout(t)
func (fake *Fake) AssertCreatedSubscriptionCheckout(
	t testing.TB, predicates ...func(payment.SubscriptionCheckoutRequest) bool,
) {
	t.Helper()

	assertRecorded(t, fake.Accepted().CreatedSubscriptionCheckouts, predicates)
}

// AssertCompletedCheckout requires an accepted explicit completion matching
// every predicate. It does not assert that a related payment settled.
//
// Example:
//
//	fake.AssertCompletedCheckout(t)
func (fake *Fake) AssertCompletedCheckout(t testing.TB, predicates ...func(payment.CompleteCheckoutRequest) bool) {
	t.Helper()

	assertRecorded(t, fake.Accepted().CompletedCheckouts, predicates)
}

// AssertRefundedPayment requires an accepted explicit refund request matching
// every predicate. Pending and ambiguous scripted responses can be accepted.
//
// Example:
//
//	fake.AssertRefundedPayment(t)
func (fake *Fake) AssertRefundedPayment(t testing.TB, predicates ...func(payment.RefundRequest) bool) {
	t.Helper()

	assertRecorded(t, fake.Accepted().RefundedPayments, predicates)
}

// AssertCanceledSubscription requires an accepted immediate cancellation
// matching every predicate. This assertion never infers a refund.
//
// Example:
//
//	fake.AssertCanceledSubscription(t)
func (fake *Fake) AssertCanceledSubscription(
	t testing.TB, predicates ...func(payment.CancelSubscriptionRequest) bool,
) {
	t.Helper()

	assertRecorded(t, fake.Accepted().CanceledSubscriptions, predicates)
}

// PreparedPayments returns a detached list of accepted initialization requests.
// Uncertain accepted replies appear here, but never imply successful payment.
// Attempted().PreparedPayments also includes failed and unexpected attempts.
//
// Example:
//
//	_ = len(fake.PreparedPayments())
func (fake *Fake) PreparedPayments() []payment.PreparePaymentRequest {
	return fake.Accepted().PreparedPayments
}

// AssertPreparedPayment requires one accepted initialization matching every
// predicate. It checks client handoff acceptance, not confirmation or settlement.
//
// Example:
//
//	fake.AssertPreparedPayment(t, func(request payment.PreparePaymentRequest) bool {
//		return request.Reference() == "order-1"
//	})
func (fake *Fake) AssertPreparedPayment(t testing.TB, predicates ...func(payment.PreparePaymentRequest) bool) {
	t.Helper()

	assertRecorded(t, fake.Accepted().PreparedPayments, predicates)
}
