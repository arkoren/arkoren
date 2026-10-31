package protocol

import (
	"encoding/json/v2"
	"errors"
	"strings"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
)

// errResponse marks a successful HTTP response that is not a usable resource.
// It contains no provider data and becomes outcome-unknown for mutations.
var errResponse = errors.New("invalid stripe resource response")

// checkResource refuses empty or mismatched resources before mapping snapshots.
// Object and ID families protect against a successful response of another type.
func checkResource(id, prefix, object, expected string) error {
	if object != expected {
		return errResponse
	}

	if err := CheckFamily(id, prefix); err != nil {
		return errResponse
	}

	return nil
}

// Checkout keeps approval and settlement independent. Optional relations
// stay absent rather than issuing network reads solely for enrichment.
func Checkout(session *stripe.CheckoutSession) (payment.Checkout, error) {
	if err := checkResource(session.ID, "cs_", session.Object, "checkout.session"); err != nil {
		return payment.Checkout{}, err
	}

	if session.Status == "" {
		return payment.Checkout{}, errResponse
	}

	amount, err := checkoutAmount(session.LastResponse)

	if err != nil {
		return payment.Checkout{}, err
	}

	result := payment.Checkout{
		ID: payment.CheckoutID(session.ID),
		Kind: map[stripe.CheckoutSessionMode]payment.CheckoutKind{
			"payment": payment.CheckoutKindPayment, "subscription": payment.CheckoutKindSubscription,
		}[session.Mode],
		NativeStatus: string(session.Status), ApprovalURL: session.URL,
		Reference: session.ClientReferenceID,
		Status: map[stripe.CheckoutSessionStatus]payment.CheckoutStatus{
			"open": payment.CheckoutStatusOpen, "complete": payment.CheckoutStatusComplete,
			"expired": payment.CheckoutStatusExpired,
		}[session.Status],
		PaymentStatus: checkoutSettlement(session.PaymentStatus, amount),
		Amount:        amount,
	}

	if session.PaymentIntent != nil {
		result.PaymentID = payment.ID(session.PaymentIntent.ID)
	}

	if session.Subscription != nil {
		result.SubscriptionID = payment.SubscriptionID(session.Subscription.ID)
	}

	return result, nil
}

// checkoutAmount preserves absent versus observed-zero totals. The SDK's scalar
// AmountTotal cannot distinguish those cases, so only these optional money fields
// are read from its response bytes. Native backends without raw bytes leave the
// amount unknown rather than inventing a zero-payment observation.
func checkoutAmount(response *stripe.APIResponse) (*payment.Money, error) {
	if response == nil || len(response.RawJSON) == 0 {
		return nil, nil
	}

	var snapshot struct {
		// AmountTotal remains nil when Stripe did not supply a checkout total.
		AmountTotal *int64 `json:"amount_total"`
		// Currency supplies minor-unit meaning for an explicitly observed total.
		Currency stripe.Currency `json:"currency"`
	}

	if err := json.Unmarshal(response.RawJSON, &snapshot); err != nil {
		return nil, errResponse
	}

	if snapshot.AmountTotal == nil {
		return nil, nil
	}

	amount, err := moneyRecord(*snapshot.AmountTotal, snapshot.Currency)

	if err != nil {
		return nil, err
	}

	return &amount, nil
}

// checkoutSettlement does not call a zero-total trial invoice a newly collected
// payment even when Stripe reports paid. Missing totals remain unknown amounts
// and cannot trigger that exception; checkout progress is mapped independently.
func checkoutSettlement(status stripe.CheckoutSessionPaymentStatus, amount *payment.Money) payment.Status {
	if amount != nil && amount.Amount == 0 && status == "paid" {
		return payment.StatusNotRequired
	}

	return map[stripe.CheckoutSessionPaymentStatus]payment.Status{
		"paid": payment.StatusSucceeded, "unpaid": payment.StatusPending,
		"no_payment_required": payment.StatusNotRequired,
	}[status]
}

// Payment maps the PaymentIntent's own status, not a checkout's status.
// Retryable collection requirements must not become terminal payment failures.
func Payment(intent *stripe.PaymentIntent) (payment.Payment, error) {
	if err := checkResource(intent.ID, "pi_", intent.Object, "payment_intent"); err != nil {
		return payment.Payment{}, err
	}

	if intent.Status == "" {
		return payment.Payment{}, errResponse
	}

	amount, err := moneyRecord(intent.Amount, intent.Currency)

	if err != nil {
		return payment.Payment{}, err
	}

	return payment.Payment{
		ID: payment.ID(intent.ID), NativeStatus: string(intent.Status), Amount: amount,
		Status: map[stripe.PaymentIntentStatus]payment.Status{
			"requires_payment_method": payment.StatusRequiresAction,
			"requires_action":         payment.StatusRequiresAction,
			"requires_confirmation":   payment.StatusRequiresAction,
			"requires_capture":        payment.StatusPending,
			"processing":              payment.StatusPending,
			"succeeded":               payment.StatusSucceeded,
			"canceled":                payment.StatusCanceled,
		}[intent.Status],
	}, nil
}

// Refund detaches one refund and its optional PaymentIntent relationship.
// A pending or unknown status never implies successful returned funds.
func Refund(refund *stripe.Refund) (payment.Refund, error) {
	if err := checkResource(refund.ID, "re_", refund.Object, "refund"); err != nil {
		return payment.Refund{}, err
	}

	if refund.Status == "" {
		return payment.Refund{}, errResponse
	}

	amount, err := moneyRecord(refund.Amount, refund.Currency)

	if err != nil {
		return payment.Refund{}, err
	}

	result := payment.Refund{
		ID: payment.RefundID(refund.ID), NativeStatus: string(refund.Status), Amount: amount,
		Status: map[stripe.RefundStatus]payment.RefundStatus{
			"pending": payment.RefundStatusPending, "requires_action": payment.RefundStatusRequiresAction,
			"succeeded": payment.RefundStatusSucceeded, "failed": payment.RefundStatusFailed,
			"canceled": payment.RefundStatusCanceled,
		}[refund.Status],
	}

	if refund.PaymentIntent != nil {
		result.PaymentID = payment.ID(refund.PaymentIntent.ID)
	}

	return result, nil
}

// Subscription maps lifecycle without inferring entitlement or settlement.
// A single non-paginated item is required to expose one portable plan. Period
// boundaries do not establish the next collection time; NextBillingAt stays nil.
func Subscription(subscription *stripe.Subscription) (payment.Subscription, error) {
	if err := checkResource(subscription.ID, "sub_", subscription.Object, "subscription"); err != nil {
		return payment.Subscription{}, err
	}

	if subscription.Status == "" {
		return payment.Subscription{}, errResponse
	}

	result := payment.Subscription{
		ID: payment.SubscriptionID(subscription.ID), NativeStatus: string(subscription.Status),
		Reference: subscription.Metadata["reference"],
		Status: map[stripe.SubscriptionStatus]payment.SubscriptionStatus{
			"incomplete": payment.SubscriptionStatusPending, "incomplete_expired": payment.SubscriptionStatusExpired,
			"trialing": payment.SubscriptionStatusTrialing, "active": payment.SubscriptionStatusActive,
			"past_due": payment.SubscriptionStatusPastDue, "unpaid": payment.SubscriptionStatusUnpaid,
			"paused": payment.SubscriptionStatusPaused, "canceled": payment.SubscriptionStatusCanceled,
		}[subscription.Status],
	}

	if subscription.Items == nil {
		return result, nil
	}

	if len(subscription.Items.Data) != 1 || subscription.Items.HasMore {
		return result, nil
	}

	item := subscription.Items.Data[0]

	if item == nil {
		return payment.Subscription{}, errResponse
	}

	if item.Price != nil {
		result.PlanReference = item.Price.ID
	}

	return result, nil
}

// moneyRecord validates the core's audited currency set for response snapshots.
// ISK and UGX are deliberately unsupported: Stripe represents whole-unit charges
// using two-decimal amounts for those currencies. No implicit conversion occurs.
func moneyRecord(amount int64, currency stripe.Currency) (payment.Money, error) {
	result := payment.Money{Amount: amount, Currency: strings.ToUpper(string(currency))}

	if err := result.Validate(); err != nil {
		return payment.Money{}, err
	}

	return result, nil
}
