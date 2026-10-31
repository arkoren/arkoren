package paymenttest

import (
	"arkoren.dev/core/service/payment"
)

// ReplyCreateCheckout queues a detached one-time creation response. A nonempty
// ID records an accepted effect even with ErrOutcomeUnknown; an empty result
// plus an error records only an attempt. Reply queues are independent FIFO lists.
//
// Example:
//
//	fake.ReplyCreateCheckout(payment.Checkout{ID: "cs_test"}, payment.ErrOutcomeUnknown)
func (fake *Fake) ReplyCreateCheckout(checkout payment.Checkout, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.create.replies = append(fake.create.replies, reply[payment.Checkout]{
		result: cloneCheckout(checkout), err: err, accepted: err == nil || checkout.ID != "",
	})
}

// ReplyCreateSubscriptionCheckout queues a detached existing-plan response.
// A nonempty ID with an error explicitly reports simulated acceptance, not
// activation or invoice payment. No other operation queue changes.
//
// Example:
//
//	fake.ReplyCreateSubscriptionCheckout(payment.Checkout{ID: "cs_subscription"}, nil)
func (fake *Fake) ReplyCreateSubscriptionCheckout(checkout payment.Checkout, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.subscribe.replies = append(fake.subscribe.replies, reply[payment.Checkout]{
		result: cloneCheckout(checkout), err: err, accepted: err == nil || checkout.ID != "",
	})
}

// ReplyGetCheckout queues one detached read response without changing any
// mutation queue. Nil optional amounts remain absent rather than synthesized.
//
// Example:
//
//	fake.ReplyGetCheckout(payment.Checkout{ID: "cs_test", Status: payment.CheckoutStatusApproved}, nil)
func (fake *Fake) ReplyGetCheckout(checkout payment.Checkout, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.checkout.replies = append(fake.checkout.replies, reply[payment.Checkout]{
		result: cloneCheckout(checkout), err: err, accepted: err == nil || checkout.ID != "",
	})
}

// ReplyCompleteCheckout queues a detached explicit-completion response. A
// nonempty ID with an error reports an accepted completion with uncertain outcome.
// The fake never turns that response into an implicit GetPayment reply.
//
// Example:
//
//	fake.ReplyCompleteCheckout(payment.Checkout{ID: "cs_test", PaymentStatus: payment.StatusPending}, nil)
func (fake *Fake) ReplyCompleteCheckout(checkout payment.Checkout, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.complete.replies = append(fake.complete.replies, reply[payment.Checkout]{
		result: cloneCheckout(checkout), err: err, accepted: err == nil || checkout.ID != "",
	})
}

// ReplyGetPayment queues one payment observation. Status is returned exactly as
// scripted, including unknown, pending, or failed outcomes; no ledger is updated.
//
// Example:
//
//	fake.ReplyGetPayment(payment.Payment{ID: "pi_test", Status: payment.StatusPending}, nil)
func (fake *Fake) ReplyGetPayment(result payment.Payment, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.payment.replies = append(fake.payment.replies, reply[payment.Payment]{
		result: result, err: err, accepted: err == nil || result.ID != "",
	})
}

// ReplyRefundPayment queues an explicit-refund response without tracking a
// refundable balance. A nonempty ID with ErrOutcomeUnknown records a simulated
// accepted refund while preserving the ambiguous error for the application.
//
// Example:
//
//	fake.ReplyRefundPayment(payment.Refund{ID: "re_test"}, payment.ErrOutcomeUnknown)
func (fake *Fake) ReplyRefundPayment(refund payment.Refund, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.refund.replies = append(fake.refund.replies, reply[payment.Refund]{
		result: refund, err: err, accepted: err == nil || refund.ID != "",
	})
}

// ReplyGetRefund queues one refund observation independently of submissions.
// It can represent pending progress without claiming returned funds succeeded.
//
// Example:
//
//	fake.ReplyGetRefund(payment.Refund{ID: "re_test", Status: payment.RefundStatusPending}, nil)
func (fake *Fake) ReplyGetRefund(refund payment.Refund, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.returned.replies = append(fake.returned.replies, reply[payment.Refund]{
		result: refund, err: err, accepted: err == nil || refund.ID != "",
	})
}

// ReplyGetSubscription queues a detached lifecycle response, copying the
// optional next-billing pointer immediately. Later caller edits cannot alter it.
//
// Example:
//
//	fake.ReplyGetSubscription(payment.Subscription{ID: "sub_test", Status: payment.SubscriptionStatusActive}, nil)
func (fake *Fake) ReplyGetSubscription(subscription payment.Subscription, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.subscription.replies = append(fake.subscription.replies, reply[payment.Subscription]{
		result: cloneSubscription(subscription), err: err, accepted: err == nil || subscription.ID != "",
	})
}

// ReplyCancelSubscription queues an immediate-cancellation outcome. Nil means
// accepted; an error means attempted without known acceptance. To model an
// accepted cancellation with a lost response, use ReplyCanceledSubscription.
//
// Example:
//
//	fake.ReplyCancelSubscription(nil)
func (fake *Fake) ReplyCancelSubscription(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.cancel.replies = append(fake.cancel.replies, reply[struct{}]{err: err, accepted: err == nil})
}

// ReplyCanceledSubscription queues an explicitly accepted cancellation even
// when its response is ambiguous. It changes history, not a subscription ledger;
// queue a separate GetSubscription response to model reconciliation.
//
// Example:
//
//	fake.ReplyCanceledSubscription(payment.ErrOutcomeUnknown)
func (fake *Fake) ReplyCanceledSubscription(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.cancel.replies = append(fake.cancel.replies, reply[struct{}]{err: err, accepted: true})
}

// ReplyPreparePayment queues a client handoff without creating related resources.
// A valid required action reports acceptance, including with ErrOutcomeUnknown;
// a zero/partial result with an error records only an attempt. Invalid results
// paired with nil error instead return the result's validation error, preventing
// partial implicit success. Returned references may all be absent.
//
// Example:
//
//	fake.ReplyPreparePayment(payment.Preparation{Action: payment.NewClientSession("vendor.session", "session-1", "data")},
//		payment.ErrOutcomeUnknown)
func (fake *Fake) ReplyPreparePayment(prepared payment.Preparation, err error) {
	validation := prepared.Err()

	if err == nil {
		err = validation
	}

	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.prepare.replies = append(fake.prepare.replies, reply[payment.Preparation]{
		result: prepared, err: err, accepted: validation == nil,
	})
}
