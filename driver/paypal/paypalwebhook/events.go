package paypalwebhook

import (
	"cmp"
	"encoding/json/v2"

	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/paypal/internal/paypal"
)

var (
	// CheckoutOrderApproved reports approval, not captured funds. Bind explicitly;
	// the application owns whether and when to complete the checkout.
	// Example: listener.Handle(paypalwebhook.CheckoutOrderApproved, accept)
	CheckoutOrderApproved = webhook.NewEvent("CHECKOUT.ORDER.APPROVED", orderObservation)
	// PaymentCaptureCompleted reports an individual completed capture, retaining
	// capture: identity. It does not establish any subscription entitlement.
	// Example: listener.Handle(paypalwebhook.PaymentCaptureCompleted, accept)
	PaymentCaptureCompleted = webhook.NewEvent("PAYMENT.CAPTURE.COMPLETED", captureObservation)
	// PaymentCaptureDenied reports a denied capture without retrying or creating
	// another checkout. Native status is retained for application policy.
	// Example: listener.Handle(paypalwebhook.PaymentCaptureDenied, accept)
	PaymentCaptureDenied = webhook.NewEvent("PAYMENT.CAPTURE.DENIED", captureObservation)
	// PaymentCaptureDeclined reports the current v2 capture-declined event.
	// It remains distinct from PayPal's older native DENIED routing key.
	// Example: listener.Handle(paypalwebhook.PaymentCaptureDeclined, accept)
	PaymentCaptureDeclined = webhook.NewEvent("PAYMENT.CAPTURE.DECLINED", captureObservation)
	// PaymentCapturePending reports unsettled funds. Its portable kind remains
	// unknown rather than manufacturing a successful or failed payment event.
	// Example: listener.Handle(paypalwebhook.PaymentCapturePending, accept)
	PaymentCapturePending = webhook.NewEvent("PAYMENT.CAPTURE.PENDING", captureObservation)
	// PaymentSaleCompleted reports a native subscription sale, preserving sale:
	// identity and any explicit billing_agreement_id. Sale API reads are unsupported.
	// Example: listener.Handle(paypalwebhook.PaymentSaleCompleted, accept)
	PaymentSaleCompleted = webhook.NewEvent("PAYMENT.SALE.COMPLETED", saleObservation)
	// PaymentSaleRefunded reports the sale's refund-related observation. It does
	// not relabel a sale ID as a v2 refund or capture identity.
	// Example: listener.Handle(paypalwebhook.PaymentSaleRefunded, accept)
	PaymentSaleRefunded = webhook.NewEvent("PAYMENT.SALE.REFUNDED", saleObservation)
	// PaymentCaptureRefunded reports a native v2 refund resource, not a new
	// capture. Optional related capture identity is taken only from the event.
	// Example: listener.Handle(paypalwebhook.PaymentCaptureRefunded, accept)
	PaymentCaptureRefunded = webhook.NewEvent("PAYMENT.CAPTURE.REFUNDED", refundObservation)
	// PaymentRefundPending reports an unsettled refund with its native status;
	// it is not proof that returned funds have reached the payer.
	// Example: listener.Handle(paypalwebhook.PaymentRefundPending, accept)
	PaymentRefundPending = webhook.NewEvent("PAYMENT.REFUND.PENDING", refundObservation)
	// PaymentRefundFailed reports a failed refund without resubmitting it.
	// Native statuses are retained even when new states are not portable.
	// Example: listener.Handle(paypalwebhook.PaymentRefundFailed, accept)
	PaymentRefundFailed = webhook.NewEvent("PAYMENT.REFUND.FAILED", refundObservation)
	// SubscriptionCreated reports lifecycle only; creation does not prove payment.
	// Definitions contain no account credentials and perform no API calls.
	// Example: listener.Handle(paypalwebhook.SubscriptionCreated, accept)
	SubscriptionCreated = webhook.NewEvent("BILLING.SUBSCRIPTION.CREATED", subscriptionObservation)
	// SubscriptionActivated reports lifecycle activation, not a paid-through date.
	// Applications bind renewal and entitlement policy separately.
	// Example: listener.Handle(paypalwebhook.SubscriptionActivated, accept)
	SubscriptionActivated = webhook.NewEvent("BILLING.SUBSCRIPTION.ACTIVATED", subscriptionObservation)
	// SubscriptionUpdated reports the event's native lifecycle snapshot without
	// fetching a newer resource or inferring ordering against other deliveries.
	// Example: listener.Handle(paypalwebhook.SubscriptionUpdated, accept)
	SubscriptionUpdated = webhook.NewEvent("BILLING.SUBSCRIPTION.UPDATED", subscriptionObservation)
	// SubscriptionCanceled preserves PayPal's event spelling and reports lifecycle
	// cancellation only. It does not initiate refunds or scheduled proration.
	// Example: listener.Handle(paypalwebhook.SubscriptionCanceled, accept)
	SubscriptionCanceled = webhook.NewEvent("BILLING.SUBSCRIPTION.CANCELLED", subscriptionObservation)
	// SubscriptionSuspended reports lifecycle suspension without claiming payment
	// failure of any particular sale or capture.
	// Example: listener.Handle(paypalwebhook.SubscriptionSuspended, accept)
	SubscriptionSuspended = webhook.NewEvent("BILLING.SUBSCRIPTION.SUSPENDED", subscriptionObservation)
	// SubscriptionExpired reports lifecycle expiry without inventing paid-through
	// information or locally advancing a subscription state machine.
	// Example: listener.Handle(paypalwebhook.SubscriptionExpired, accept)
	SubscriptionExpired = webhook.NewEvent("BILLING.SUBSCRIPTION.EXPIRED", subscriptionObservation)
)

// orderObservation maps only observed order fields. In particular, order
// approval does not set a succeeded payment kind or issue a capture request.
func orderObservation(envelope Envelope) (payment.Observation, error) {
	var resource paypal.Order

	if err := json.Unmarshal(envelope.Resource, &resource); err != nil {
		return payment.Observation{}, paypal.NewError(err)
	}

	checkout, err := resource.Checkout()

	if err != nil {
		return payment.Observation{}, err
	}

	return payment.Observation{Kind: payment.CheckoutApproved, OccurredAt: envelope.CreatedAt,
		CheckoutID: checkout.ID, PaymentID: checkout.PaymentID, Amount: checkout.Amount,
		NativeStatus: checkout.NativeStatus}, nil
}

// captureObservation uses native capture status rather than order completion
// to distinguish money movement. Explicit order relationships are optional.
func captureObservation(envelope Envelope) (payment.Observation, error) {
	var resource paypal.Movement

	if err := json.Unmarshal(envelope.Resource, &resource); err != nil {
		return payment.Observation{}, paypal.NewError(err)
	}

	result, err := resource.Payment()

	if err != nil {
		return payment.Observation{}, err
	}

	// kinds leaves pending and unknown states unknown, never optimistic success.
	kinds := map[payment.Status]payment.ObservationKind{
		payment.StatusSucceeded: payment.PaymentSucceeded, payment.StatusFailed: payment.PaymentFailed,
	}
	observation := payment.Observation{Kind: kinds[result.Status], OccurredAt: envelope.CreatedAt,
		PaymentID: result.ID, Amount: &result.Amount, NativeStatus: result.NativeStatus}

	if id := resource.SupplementaryData.RelatedIDs.OrderID; id != "" {
		if err := paypal.ValidNative(id); err != nil {
			return payment.Observation{}, err
		}

		observation.CheckoutID = payment.CheckoutID("order:" + id)
	}

	return observation, nil
}

// saleObservation keeps sale and capture namespaces separate. A v1 refund
// resource reports its original sale_id; its own ID is not a v2 refund identity.
func saleObservation(envelope Envelope) (payment.Observation, error) {
	var resource paypal.Movement

	if err := json.Unmarshal(envelope.Resource, &resource); err != nil {
		return payment.Observation{}, paypal.NewError(err)
	}

	money, err := resource.Amount.Money()

	if err != nil {
		return payment.Observation{}, err
	}

	// kinds maps the event meaning, not an inferred subscription invoice.
	kinds := map[string]payment.ObservationKind{
		"PAYMENT.SALE.COMPLETED": payment.PaymentSucceeded, "PAYMENT.SALE.REFUNDED": payment.RefundUpdated,
	}
	id, subscriptionID, err := saleRelationships(resource, envelope.Type)

	if err != nil {
		return payment.Observation{}, err
	}

	return payment.Observation{Kind: kinds[envelope.Type], OccurredAt: envelope.CreatedAt,
		PaymentID: id, SubscriptionID: subscriptionID, Amount: &money,
		NativeStatus: cmp.Or(resource.Status, resource.State)}, nil
}

// saleRelationships preserves only observed sale/subscription links. A refund
// resource's own ID is never mislabeled as the original sale or a v2 refund.
func saleRelationships(resource paypal.Movement, eventType string) (payment.ID, payment.SubscriptionID, error) {
	native := resource.ID

	if eventType == "PAYMENT.SALE.REFUNDED" {
		native = resource.SaleID
	}

	var id payment.ID

	if native != "" || eventType == "PAYMENT.SALE.COMPLETED" {
		if err := paypal.ValidNative(native); err != nil {
			return "", "", err
		}

		id = payment.ID("sale:" + native)
	}

	if resource.BillingAgreementID != "" {
		if err := paypal.SubscriptionNative(resource.BillingAgreementID); err != nil {
			return "", "", err
		}
	}

	return id, payment.SubscriptionID(resource.BillingAgreementID), nil
}

// refundObservation reports only native v2 refund identity and relationships.
// It does not hydrate a capture, original sale, or remaining refundable balance.
func refundObservation(envelope Envelope) (payment.Observation, error) {
	var resource paypal.Movement

	if err := json.Unmarshal(envelope.Resource, &resource); err != nil {
		return payment.Observation{}, paypal.NewError(err)
	}

	refund, err := resource.Refund()

	if err != nil {
		return payment.Observation{}, err
	}

	if id := resource.SupplementaryData.RelatedIDs.CaptureID; id != "" {
		if err := paypal.ValidNative(id); err != nil {
			return payment.Observation{}, err
		}

		refund.PaymentID = payment.ID("capture:" + id)
	}

	return payment.Observation{Kind: payment.RefundUpdated, OccurredAt: envelope.CreatedAt,
		RefundID: refund.ID, PaymentID: refund.PaymentID, Amount: &refund.Amount,
		NativeStatus: refund.NativeStatus}, nil
}

// subscriptionObservation reports lifecycle without paid-through inference.
// Checkout relationships use only the native subscription identity in the event.
func subscriptionObservation(envelope Envelope) (payment.Observation, error) {
	var resource paypal.Subscription

	if err := json.Unmarshal(envelope.Resource, &resource); err != nil {
		return payment.Observation{}, paypal.NewError(err)
	}

	subscription, err := resource.Snapshot()

	if err != nil {
		return payment.Observation{}, err
	}

	return payment.Observation{Kind: payment.SubscriptionUpdated, OccurredAt: envelope.CreatedAt,
		SubscriptionID: subscription.ID, CheckoutID: payment.CheckoutID("subscription:" + string(subscription.ID)),
		NativeStatus: subscription.NativeStatus}, nil
}
