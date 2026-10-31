package paypal

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"arkoren.dev/core/service/payment"
)

// Amount reads both v2 currency_code/value and native sale currency/total.
// Decimal strings remain strings until exact minor-unit conversion.
// Example: _ = paypal.Amount{CurrencyCode: "USD", Value: "12.50"}
type Amount struct {
	// CurrencyCode is the v2 ISO currency. Example: _ = paypal.Amount{CurrencyCode: "USD"}
	CurrencyCode string `json:"currency_code"`
	// Value is the v2 exact decimal. Example: _ = paypal.Amount{Value: "12.50"}
	Value string `json:"value"`
	// Currency is the sale ISO currency. Example: _ = paypal.Amount{Currency: "USD"}
	Currency string `json:"currency"`
	// Total is the sale exact decimal. Example: _ = paypal.Amount{Total: "12.50"}
	Total string `json:"total"`
}

// Money converts either native amount shape without floating point or rounding.
// Absent or invalid amounts return an error, never an invented zero amount.
// Only the reviewed wallet subset is accepted. HUF uses whole provider units
// but retains canonical ISO scale two; fractional suffixes, even zeros, fail.
// Example: money, err := (paypal.Amount{CurrencyCode: "USD", Value: "1.00"}).Money(); _ = money; _ = err
func (amount Amount) Money() (payment.Money, error) {
	currency := cmp.Or(amount.CurrencyCode, amount.Currency)
	value := cmp.Or(amount.Value, amount.Total)

	if err := checkCurrency(currency); err != nil {
		return payment.Money{}, err
	}

	if currency == "HUF" && strings.Contains(value, ".") {
		return payment.Money{}, fmt.Errorf("%w: paypal requires whole HUF", payment.ErrInvalidRequest)
	}

	return payment.ParseMoney(currency, value)
}

// Link is an observed provider relationship. No link is fetched by this module.
// Example: _ = paypal.Link{Rel: "approve", Href: "https://www.sandbox.paypal.com/checkoutnow?token=ABC"}
type Link struct {
	// Rel names the relationship. Example: _ = paypal.Link{Rel: "approve"}
	Rel string `json:"rel"`
	// Href is the provider URL, which may contain sensitive approval tokens.
	// Example: _ = paypal.Link{Href: "https://api-m.paypal.com/v2/payments/captures/ABC"}
	Href string `json:"href"`
}

// Movement is the common native money-resource shape used by capture, refund,
// and sale notifications. Callers choose the resource family explicitly; the
// shared status and amount field shapes never establish API compatibility.
// Example: _ = paypal.Movement{ID: "ABC", Status: "COMPLETED"}
type Movement struct {
	// ID is native, without the driver's family prefix. Example: _ = paypal.Movement{ID: "ABC"}
	ID string `json:"id"`
	// CustomID is an explicitly observed capture correlation value, not an
	// application reference inferred from a different resource or network read.
	// Example: _ = paypal.Movement{CustomID: "order-123"}
	CustomID string `json:"custom_id"`
	// Status is a capture/refund state. Example: _ = paypal.Movement{Status: "PENDING"}
	Status string `json:"status"`
	// State is a legacy sale state. Example: _ = paypal.Movement{State: "completed"}
	State string `json:"state"`
	// Amount is exact provider money, never floating point or a rounded total.
	// Example: _ = paypal.Movement{Amount: paypal.Amount{Value: "1.00", CurrencyCode: "USD"}}
	Amount Amount `json:"amount"`
	// Links contain observed relationships, never additional fetch instructions.
	// Example: _ = paypal.Movement{Links: []paypal.Link{{Rel: "up"}}}
	Links []Link `json:"links"`
	// BillingAgreementID optionally links a sale to a native subscription.
	// Example: _ = paypal.Movement{BillingAgreementID: "I-ABC"}
	BillingAgreementID string `json:"billing_agreement_id"`
	// SaleID is the original sale on a native v1 refund resource. It must not be
	// confused with that resource's own refund ID or a v2 capture relationship.
	// Example: _ = paypal.Movement{SaleID: "SALE"}
	SaleID string `json:"sale_id"`
	// SupplementaryData carries only explicitly observed resource relationships.
	// Example: resource.SupplementaryData.RelatedIDs.OrderID = "ABC"
	SupplementaryData struct {
		// RelatedIDs identifies associated resources without deriving them from URLs.
		// Example: resource.SupplementaryData.RelatedIDs.CaptureID = "ABC"
		RelatedIDs struct {
			// OrderID identifies a checkout order. Example: resource.SupplementaryData.RelatedIDs.OrderID = "ABC"
			OrderID string `json:"order_id"`
			// CaptureID identifies a v2 capture. Example: resource.SupplementaryData.RelatedIDs.CaptureID = "ABC"
			CaptureID string `json:"capture_id"`
		} `json:"related_ids"`
	} `json:"supplementary_data"`
}

// Payment maps an observed capture to a detached payment snapshot. An unknown
// status stays unknown, including future provider statuses. REFUNDED and
// PARTIALLY_REFUNDED preserve successful capture history per payment.StatusSucceeded;
// refund resources, not this snapshot, describe returned funds or balance.
// Example: result, err := resource.Payment(); _ = result; _ = err
func (movement Movement) Payment() (payment.Payment, error) {
	if err := ValidNative(movement.ID); err != nil {
		return payment.Payment{}, err
	}

	money, err := movement.Amount.Money()

	if err != nil {
		return payment.Payment{}, err
	}

	// statuses distinguish capture settlement from order completion.
	statuses := map[string]payment.Status{
		"COMPLETED": payment.StatusSucceeded, "PENDING": payment.StatusPending,
		"REFUNDED": payment.StatusSucceeded, "PARTIALLY_REFUNDED": payment.StatusSucceeded,
		"DECLINED": payment.StatusFailed, "DENIED": payment.StatusFailed,
		"FAILED": payment.StatusFailed,
	}

	return payment.Payment{ID: payment.ID("capture:" + movement.ID),
		Status:       statuses[cmp.Or(movement.Status, strings.ToUpper(movement.State))],
		NativeStatus: cmp.Or(movement.Status, movement.State), Amount: money}, nil
}

// Refund maps a v2 refund and only trusted up-link capture relationships. Missing
// relationships stay absent; arbitrary URLs are neither fetched nor interpreted.
// Example: result, err := resource.Refund(); _ = result; _ = err
func (movement Movement) Refund() (payment.Refund, error) {
	if err := ValidNative(movement.ID); err != nil {
		return payment.Refund{}, err
	}

	money, err := movement.Amount.Money()

	if err != nil {
		return payment.Refund{}, err
	}

	// statuses preserve pending and canceled as distinct from returned funds.
	statuses := map[string]payment.RefundStatus{
		"COMPLETED": payment.RefundStatusSucceeded, "PENDING": payment.RefundStatusPending,
		"CANCELLED": payment.RefundStatusCanceled, "FAILED": payment.RefundStatusFailed,
	}

	return payment.Refund{ID: payment.RefundID("refund:" + movement.ID), PaymentID: captureLink(movement.Links),
		Status: statuses[movement.Status], NativeStatus: movement.Status, Amount: money}, nil
}

// Order is a single-purchase-unit CAPTURE order, not a settlement receipt.
// Example: _ = paypal.Order{ID: "ABC", Status: "APPROVED"}
type Order struct {
	// ID is the native order ID. Example: _ = paypal.Order{ID: "ABC"}
	ID string `json:"id"`
	// Status tracks order progress, not capture settlement. Example: _ = paypal.Order{Status: "COMPLETED"}
	Status string `json:"status"`
	// Links can contain approve or payer-action URLs. Example: _ = paypal.Order{Links: []paypal.Link{{Rel: "approve"}}}
	Links []Link `json:"links"`
	// PurchaseUnits must contain at most the one unit created by this driver.
	// Example: _ = paypal.Order{PurchaseUnits: nil}
	PurchaseUnits []struct {
		// Amount is optional on partial representations. Example: unit.Amount = nil
		Amount *Amount `json:"amount"`
		// CustomID preserves the application reference. Example: unit.CustomID = "order-123"
		CustomID string `json:"custom_id"`
		// Payments contains observed captures only. Example: _ = unit.Payments.Captures
		Payments struct {
			// Captures supplies the payment snapshot without hydration. Example: _ = unit.Payments.Captures
			Captures []Movement `json:"captures"`
		} `json:"payments"`
	} `json:"purchase_units"`
}

// Checkout maps one order without equating COMPLETED with paid. Captures are
// read from this representation only. Multiple units/captures are unsupported
// rather than arbitrarily selecting a payment to report or refund.
// Example: checkout, err := order.Checkout(); _ = checkout; _ = err
func (order Order) Checkout() (payment.Checkout, error) {
	if err := ValidNative(order.ID); err != nil {
		return payment.Checkout{}, err
	}

	// statuses deliberately leave unknown provider states unmapped.
	statuses := map[string]payment.CheckoutStatus{
		"CREATED": payment.CheckoutStatusOpen, "SAVED": payment.CheckoutStatusOpen,
		"PAYER_ACTION_REQUIRED": payment.CheckoutStatusOpen, "APPROVED": payment.CheckoutStatusApproved,
		"COMPLETED": payment.CheckoutStatusComplete, "VOIDED": payment.CheckoutStatusCanceled,
	}
	checkout := payment.Checkout{ID: payment.CheckoutID("order:" + order.ID), Kind: payment.CheckoutKindPayment,
		Status: statuses[order.Status], NativeStatus: order.Status, ApprovalURL: ApprovalURL(order.Links),
		NeedsCompletion: order.Status == "APPROVED"}

	if len(order.PurchaseUnits) > 1 {
		return payment.Checkout{}, fmt.Errorf("%w: multiple purchase units", payment.ErrUnsupported)
	}

	if len(order.PurchaseUnits) == 0 {
		return checkout, nil
	}

	unit := order.PurchaseUnits[0]
	checkout.Reference = unit.CustomID

	if unit.Amount != nil {
		money, err := unit.Amount.Money()

		if err != nil {
			return payment.Checkout{}, err
		}

		checkout.Amount = &money
	}

	if err := attachCapture(&checkout, unit.Payments.Captures); err != nil {
		return payment.Checkout{}, err
	}

	return checkout, nil
}

// attachCapture reports the single observed capture independently of the
// order's state. It never performs network hydration or selects among captures.
func attachCapture(checkout *payment.Checkout, captures []Movement) error {
	if len(captures) == 0 {
		return nil
	}

	if len(captures) > 1 {
		return fmt.Errorf("%w: multiple captures", payment.ErrUnsupported)
	}

	result, err := captures[0].Payment()

	if err != nil {
		return err
	}

	checkout.PaymentID = result.ID
	checkout.PaymentStatus = result.Status
	checkout.Reference = cmp.Or(checkout.Reference, captures[0].CustomID)

	if checkout.Amount == nil {
		checkout.Amount = &result.Amount
	}

	return nil
}

// Subscription is a native billing lifecycle response, never a paid-through
// record. The next billing time is optional even when the subscription is active.
// Example: _ = paypal.Subscription{ID: "I-ABC", Status: "ACTIVE"}
type Subscription struct {
	// ID is native and retains its I- prefix. Example: _ = paypal.Subscription{ID: "I-ABC"}
	ID string `json:"id"`
	// Status is the native lifecycle status. Example: _ = paypal.Subscription{Status: "ACTIVE"}
	Status string `json:"status"`
	// PlanID identifies the existing PayPal billing plan. Example: _ = paypal.Subscription{PlanID: "P-ABC"}
	PlanID string `json:"plan_id"`
	// CustomID is an optional application reference. Example: _ = paypal.Subscription{CustomID: "signup-123"}
	CustomID string `json:"custom_id"`
	// Links contain observed approval URLs. Example: _ = paypal.Subscription{Links: nil}
	Links []Link `json:"links"`
	// BillingInfo contains scheduling information, not proof of settlement.
	// Example: _ = resource.BillingInfo.NextBillingAt
	BillingInfo struct {
		// NextBillingAt is absent when PayPal has not supplied a next billing time.
		// Example: resource.BillingInfo.NextBillingAt = nil
		NextBillingAt *time.Time `json:"next_billing_time"`
	} `json:"billing_info"`
}

// Snapshot maps lifecycle and scheduling without any payment or entitlement
// inference. Invalid native IDs are treated as malformed provider responses.
// Example: subscription, err := resource.Snapshot(); _ = subscription; _ = err
func (subscription Subscription) Snapshot() (payment.Subscription, error) {
	if err := SubscriptionNative(subscription.ID); err != nil {
		return payment.Subscription{}, err
	}

	// statuses intentionally omit unmapped future provider values.
	statuses := map[string]payment.SubscriptionStatus{
		"APPROVAL_PENDING": payment.SubscriptionStatusPending, "APPROVED": payment.SubscriptionStatusPending,
		"ACTIVE": payment.SubscriptionStatusActive, "SUSPENDED": payment.SubscriptionStatusSuspended,
		"CANCELLED": payment.SubscriptionStatusCanceled, "EXPIRED": payment.SubscriptionStatusExpired,
	}

	return payment.Subscription{ID: payment.SubscriptionID(subscription.ID), PlanReference: subscription.PlanID,
		Status: statuses[subscription.Status], NativeStatus: subscription.Status, Reference: subscription.CustomID,
		NextBillingAt: subscription.BillingInfo.NextBillingAt}, nil
}

// Checkout observes subscription approval without activating it. ACTIVE does
// not establish any successful payment; unknown states remain explicit unknowns.
// Example: checkout, err := resource.Checkout(); _ = checkout; _ = err
func (subscription Subscription) Checkout() (payment.Checkout, error) {
	if err := SubscriptionNative(subscription.ID); err != nil {
		return payment.Checkout{}, err
	}

	// statuses describe only the approval/lifecycle snapshot.
	statuses := map[string]payment.CheckoutStatus{
		"APPROVAL_PENDING": payment.CheckoutStatusOpen, "APPROVED": payment.CheckoutStatusApproved,
		"ACTIVE": payment.CheckoutStatusComplete, "SUSPENDED": payment.CheckoutStatusComplete,
		"CANCELLED": payment.CheckoutStatusCanceled, "EXPIRED": payment.CheckoutStatusExpired,
	}

	return payment.Checkout{ID: payment.CheckoutID("subscription:" + subscription.ID),
		Kind: payment.CheckoutKindSubscription, Status: statuses[subscription.Status], NativeStatus: subscription.Status,
		ApprovalURL: ApprovalURL(subscription.Links), SubscriptionID: payment.SubscriptionID(subscription.ID),
		Reference: subscription.CustomID}, nil
}

// ApprovalURL prefers approve links and otherwise accepts payer-action. URLs
// are returned for the caller's customer redirect only, never used for API I/O.
// Example: _ = paypal.ApprovalURL([]paypal.Link{{Rel: "approve", Href: "https://www.paypal.com/"}})
func ApprovalURL(links []Link) string {
	fallback := ""

	for _, link := range links {
		if link.Rel == "approve" {
			return link.Href
		}

		if link.Rel == "payer-action" {
			fallback = link.Href
		}
	}

	return fallback
}

// Native accepts only an exact family prefix and a path-safe native identity.
// It is private module plumbing, not a public provider codec.
// Example: id, err := paypal.Native("order:ABC", "order:"); _ = id; _ = err
func Native(value, prefix string) (string, error) {
	native, ok := strings.CutPrefix(value, prefix)

	if !ok {
		return "", fmt.Errorf("%w: resource family", payment.ErrInvalidRequest)
	}

	if err := ValidNative(native); err != nil {
		return "", err
	}

	return native, nil
}

// ValidNative rejects empty, oversized, and non-path-safe identities before
// constructing URLs. Provider response IDs are checked with the same rule.
// Example: err := paypal.ValidNative("ABC123"); _ = err
func ValidNative(value string) error {
	if value == "" || len(value) > 255 {
		return fmt.Errorf("%w: native identity", payment.ErrInvalidRequest)
	}

	if strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return fmt.Errorf("%w: native identity", payment.ErrInvalidRequest)
	}

	return nil
}

// SubscriptionNative additionally requires the native I- subscription family,
// preventing order, capture, and plan identities from reaching billing routes.
// Example: err := paypal.SubscriptionNative("I-ABC"); _ = err
func SubscriptionNative(value string) error {
	if !strings.HasPrefix(value, "I-") || len(value) <= 2 {
		return fmt.Errorf("%w: subscription identity", payment.ErrInvalidRequest)
	}

	return ValidNative(value)
}

// MutationError marks semantic decoding failures after successful mutations.
// The original cause remains available without appearing in the safe message.
// Example: err := paypal.MutationError(errors.New("missing result ID")); _ = err
func MutationError(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%w: %w", payment.ErrOutcomeUnknown, NewError(err))
}

// captureLink accepts only an exact capture path on a fixed HTTPS PayPal origin.
// Userinfo, query markers or content, nonempty fragments, retained escaped paths,
// and foreign authorities are not trusted. No link is fetched, even if accepted.
func captureLink(links []Link) payment.ID {
	for _, link := range links {
		if link.Rel != "up" {
			continue
		}

		if id := trustedCapture(link.Href); id != "" {
			return payment.ID("capture:" + id)
		}
	}

	return ""
}

// trustedCapture validates rather than following a provider relationship. It
// checks the parsed location policy before the exact capture path and native ID;
// invalid links return an empty identity without changing the refund's outcome.
func trustedCapture(href string) string {
	link, err := url.Parse(href)

	if err != nil {
		return ""
	}

	if !trustedCaptureLocation(link) {
		return ""
	}

	id, ok := strings.CutPrefix(link.Path, "/v2/payments/captures/")

	if !ok {
		return ""
	}

	if err := ValidNative(id); err != nil {
		return ""
	}

	return id
}

// trustedCaptureLocation checks the URL envelope before its resource path is
// interpreted. link must be a nonnil parsed URL. Only the two fixed HTTPS
// authorities are allowed, without userinfo, query or fragment content, an empty
// query marker, or a retained escaped path. It does not normalize authorities,
// strip ports, decode paths again, or mutate link. A bare fragment marker remains
// acceptable because net/url supplies an empty Fragment for that spelling.
func trustedCaptureLocation(link *url.URL) bool {
	origin := link.Scheme + "://" + link.Host

	switch origin {
	case Sandbox, Live:
	default:
		return false
	}

	if link.User != nil || link.RawQuery != "" || link.Fragment != "" {
		return false
	}

	if link.RawPath != "" || link.ForceQuery {
		return false
	}

	return true
}

// Issue reports a specific confirmed provider conflict without comparing error
// text. It is used only for the two explicit conflict-reconciliation reads.
// Example: _ = paypal.Issue(err, "ORDER_ALREADY_CAPTURED")
func Issue(err error, issue string) bool {
	var failure *Error

	if !errors.As(err, &failure) {
		return false
	}

	return failure.Status == http.StatusUnprocessableEntity && failure.Issue == issue
}
