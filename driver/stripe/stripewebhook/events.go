package stripewebhook

import (
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/payment"
)

// CheckoutSessionCompleted observes checkout completion, not payment collection.
// Bind it explicitly; NativeStatus and optional related IDs come from the snapshot.
//
// Example:
//
//	receiver.Handle(stripewebhook.CheckoutSessionCompleted, accept)
var CheckoutSessionCompleted = webhook.NewEvent("checkout.session.completed", checkoutObservation)

// PaymentIntentSucceeded observes one successful PaymentIntent, preserving pi_
// identity and its amount without fetching another resource.
//
// Example:
//
//	receiver.Handle(stripewebhook.PaymentIntentSucceeded, accept)
var PaymentIntentSucceeded = webhook.NewEvent("payment_intent.succeeded", func(envelope Envelope) (payment.Observation, error) {
	return paymentObservation(envelope, payment.PaymentSucceeded)
})

// PaymentIntentFailed observes an unsuccessful payment attempt. The intent may
// still be retryable; NativeStatus is not replaced with a synthetic failed state.
//
// Example:
//
//	receiver.Handle(stripewebhook.PaymentIntentFailed, accept)
var PaymentIntentFailed = webhook.NewEvent("payment_intent.payment_failed", func(envelope Envelope) (payment.Observation, error) {
	return paymentObservation(envelope, payment.PaymentFailed)
})

// RefundUpdated observes refund.updated, which may still report pending funds.
// It preserves the refund identity and any native PaymentIntent relationship.
//
// Example:
//
//	receiver.Handle(stripewebhook.RefundUpdated, accept)
var RefundUpdated = webhook.NewEvent("refund.updated", refundObservation)

// SubscriptionCreated observes lifecycle creation, not successful invoice payment.
// The application owns entitlement policy and out-of-order event reconciliation.
//
// Example:
//
//	receiver.Handle(stripewebhook.SubscriptionCreated, accept)
var SubscriptionCreated = webhook.NewEvent("customer.subscription.created", subscriptionObservation)

// SubscriptionUpdated observes lifecycle changes without a paid-through ledger.
// It requires no payment driver or local subscription state.
//
// Example:
//
//	receiver.Handle(stripewebhook.SubscriptionUpdated, accept)
var SubscriptionUpdated = webhook.NewEvent("customer.subscription.updated", subscriptionObservation)

// SubscriptionDeleted observes the native subscription deletion notification.
// It reports lifecycle only and neither refunds nor cancels anything itself.
//
// Example:
//
//	receiver.Handle(stripewebhook.SubscriptionDeleted, accept)
var SubscriptionDeleted = webhook.NewEvent("customer.subscription.deleted", subscriptionObservation)

// InvoicePaid observes an invoice's paid state, including zero or externally
// settled invoices. It is not evidence of a newly collected PaymentIntent.
//
// Example:
//
//	receiver.Handle(stripewebhook.InvoicePaid, accept)
var InvoicePaid = webhook.NewEvent("invoice.paid", func(envelope Envelope) (payment.Observation, error) {
	return invoiceObservation(envelope, payment.InvoicePaid)
})

// InvoicePaymentFailed observes an invoice-associated attempt failure. The
// invoice ID and parent subscription ID are retained when present in the snapshot.
//
// Example:
//
//	receiver.Handle(stripewebhook.InvoicePaymentFailed, accept)
var InvoicePaymentFailed = webhook.NewEvent("invoice.payment_failed", func(envelope Envelope) (payment.Observation, error) {
	return invoiceObservation(envelope, payment.InvoicePaymentFailed)
})

// InvoicePaymentPaid observes invoice_payment.paid, distinct from invoice.paid.
// It reports the linked invoice and PaymentIntent only when the snapshot supplies
// them; charge-only and payment-record settlements do not invent a pi_ identity.
//
// Example:
//
//	receiver.Handle(stripewebhook.InvoicePaymentPaid, accept)
var InvoicePaymentPaid = webhook.NewEvent("invoice_payment.paid", invoicePaymentObservation)
