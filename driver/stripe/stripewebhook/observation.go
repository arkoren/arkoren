package stripewebhook

import "arkoren.dev/core/service/payment"

// checkoutObservation reports flow completion without treating it as collection.
// Relationships are copied only from this event's authenticated snapshot.
func checkoutObservation(envelope Envelope) (payment.Observation, error) {
	snapshot, err := decodeSnapshot[checkoutSnapshot](envelope)

	if err != nil {
		return payment.Observation{}, err
	}

	result, err := observation(envelope, snapshot.resource, "checkout.session")

	if err != nil {
		return payment.Observation{}, err
	}

	result.Kind = payment.CheckoutCompleted
	result.CheckoutID = payment.CheckoutID(snapshot.ID)
	result.Amount, err = observedMoney(snapshot.AmountTotal, snapshot.Currency)

	if err != nil {
		return payment.Observation{}, err
	}

	intent, err := relatedID(snapshot.PaymentIntent)

	if err != nil {
		return payment.Observation{}, err
	}

	subscription, err := relatedID(snapshot.Subscription)

	if err != nil {
		return payment.Observation{}, err
	}

	result.PaymentID = payment.ID(intent)
	result.SubscriptionID = payment.SubscriptionID(subscription)

	return result, nil
}

// paymentObservation retains the intent's state independently of the attempt's
// event meaning. A failed attempt can leave an intent requiring a payment method.
func paymentObservation(envelope Envelope, kind payment.ObservationKind) (payment.Observation, error) {
	snapshot, err := decodeSnapshot[moneySnapshot](envelope)

	if err != nil {
		return payment.Observation{}, err
	}

	result, err := observation(envelope, snapshot.resource, "payment_intent")

	if err != nil {
		return payment.Observation{}, err
	}

	result.Kind = kind
	result.PaymentID = payment.ID(snapshot.ID)
	result.Amount, err = observedMoney(snapshot.Amount, snapshot.Currency)

	return result, err
}

// refundObservation reports progress of one refund and the observed original
// PaymentIntent relation. A refund update is not assumed to have succeeded.
func refundObservation(envelope Envelope) (payment.Observation, error) {
	snapshot, err := decodeSnapshot[moneySnapshot](envelope)

	if err != nil {
		return payment.Observation{}, err
	}

	result, err := observation(envelope, snapshot.resource, "refund")

	if err != nil {
		return payment.Observation{}, err
	}

	result.Kind = payment.RefundUpdated
	result.RefundID = payment.RefundID(snapshot.ID)
	result.Amount, err = observedMoney(snapshot.Amount, snapshot.Currency)

	if err != nil {
		return payment.Observation{}, err
	}

	intent, err := relatedID(snapshot.PaymentIntent)
	result.PaymentID = payment.ID(intent)

	return result, err
}

// subscriptionObservation reports lifecycle only, without network reads or
// interpreting an active subscription as proof of any invoice payment.
func subscriptionObservation(envelope Envelope) (payment.Observation, error) {
	snapshot, err := decodeSnapshot[resource](envelope)

	if err != nil {
		return payment.Observation{}, err
	}

	result, err := observation(envelope, snapshot, "subscription")
	result.Kind = payment.SubscriptionUpdated
	result.SubscriptionID = payment.SubscriptionID(snapshot.ID)

	return result, err
}

// invoiceObservation distinguishes the aggregate paid invoice from a failed
// collection attempt. Subscription identity is read from parent details only.
func invoiceObservation(envelope Envelope, kind payment.ObservationKind) (payment.Observation, error) {
	snapshot, err := decodeSnapshot[invoiceSnapshot](envelope)

	if err != nil {
		return payment.Observation{}, err
	}

	result, err := observation(envelope, snapshot.resource, "invoice")

	if err != nil {
		return payment.Observation{}, err
	}

	result.Kind = kind
	result.InvoiceID = snapshot.ID
	amount := snapshot.AmountPaid

	if kind == payment.InvoicePaymentFailed {
		amount = snapshot.AmountDue
	}

	result.Amount, err = observedMoney(amount, snapshot.Currency)

	if err != nil {
		return payment.Observation{}, err
	}

	if snapshot.Parent == nil || snapshot.Parent.SubscriptionDetails == nil {
		return result, nil
	}

	subscription, err := relatedID(snapshot.Parent.SubscriptionDetails.Subscription)
	result.SubscriptionID = payment.SubscriptionID(subscription)

	return result, err
}

// invoicePaymentObservation reports one invoice-payment settlement and only its
// explicitly supplied invoice and PaymentIntent links. No relation is hydrated.
func invoicePaymentObservation(envelope Envelope) (payment.Observation, error) {
	snapshot, err := decodeSnapshot[invoicePaymentSnapshot](envelope)

	if err != nil {
		return payment.Observation{}, err
	}

	result, err := observation(envelope, snapshot.resource, "invoice_payment")

	if err != nil {
		return payment.Observation{}, err
	}

	result.Kind = payment.InvoicePaymentPaid
	result.Amount, err = observedMoney(snapshot.AmountPaid, snapshot.Currency)

	if err != nil {
		return payment.Observation{}, err
	}

	result.InvoiceID, err = relatedID(snapshot.Invoice)

	if err != nil {
		return payment.Observation{}, err
	}

	if snapshot.Payment == nil || snapshot.Payment.Type != "payment_intent" {
		return result, nil
	}

	intent, err := relatedID(snapshot.Payment.PaymentIntent)
	result.PaymentID = payment.ID(intent)

	return result, err
}
