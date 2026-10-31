// Package resource validates and translates exact Mollie resource snapshots.
package resource

import (
	"errors"
	"strings"

	"arkoren.dev/core/service/payment"
)

// Amount is Mollie's exact decimal wire representation, never a float.
// Missing amounts remain distinguishable through pointers on response records.
type Amount struct {
	// Currency is the ISO 4217 currency code observed from Mollie.
	Currency string `json:"currency"`
	// Value is a major-unit decimal parsed with the core currency exponent.
	Value string `json:"value"`
}

// Payment contains only the response fields needed by the portable
// contract. It never retains card details, billing addresses or error prose.
type Payment struct {
	// ID is a native payment identifier, also used as its hosted checkout ID.
	ID string `json:"id"`
	// Status is a bounded native status token, including future unknown tokens.
	Status string `json:"status"`
	// Amount is absent until explicitly observed, never copied from a request.
	Amount *Amount `json:"amount"`
	// Metadata permits arbitrary vendor metadata without assuming our own shape.
	Metadata any `json:"metadata"`
	// Links contains only the hosted approval link; API links are never followed.
	Links struct {
		// Checkout is an optional hosted payment destination.
		Checkout struct {
			// Href is returned only after absolute HTTPS validation.
			Href string `json:"href"`
		} `json:"checkout"`
	} `json:"_links"`
}

// Refund carries the refund's own status and explicit parent identity.
// The parent is mandatory: no request value is substituted into the snapshot.
type Refund struct {
	// ID is Mollie's refund token, not by itself sufficient for GetRefund.
	ID string `json:"id"`
	// PaymentID is the observed parent used in the opaque compound identity.
	PaymentID string `json:"paymentId"`
	// Status tracks returned-funds progress independently of payment success.
	Status string `json:"status"`
	// Amount is the observed refund amount, not the settlement adjustment.
	Amount *Amount `json:"amount"`
}

// NativeID checks the resource family and bounded alphanumeric suffix before
// path construction. It never includes untrusted input in validation errors.
func NativeID(value, prefix string) error {
	suffix, found := strings.CutPrefix(value, prefix)

	if !found || !ValidToken(suffix) {
		return payment.ErrInvalidRequest
	}

	return nil
}

// RefundParts decodes an opaque identity without storage. Both components are
// independently bounded and checked, so separators cannot escape an API path.
func RefundParts(value string) (string, string, error) {
	parent, refund, found := strings.Cut(value, ":")

	if !found {
		return "", "", payment.ErrInvalidRequest
	}

	if err := NativeID(parent, "tr_"); err != nil {
		return "", "", err
	}

	if err := NativeID(refund, "re_"); err != nil {
		return "", "", err
	}

	return parent, refund, nil
}

// observedMoney requires an explicit currency and decimal from the provider.
// Zero is valid for observations; missing or negative data is a protocol error.
func observedMoney(value *Amount) (payment.Money, error) {
	if value == nil {
		return payment.Money{}, errors.New("mollie response lacks amount")
	}

	money, err := payment.ParseMoney(value.Currency, value.Value)

	if err != nil {
		return payment.Money{}, errors.New("mollie response has invalid amount")
	}

	return money, nil
}

// Snapshot validates response-only payment data. Unmapped but well-formed
// status tokens map to the contract's zero Unknown state, never success.
func (paymentResource Payment) Snapshot() (payment.Payment, error) {
	if err := NativeID(paymentResource.ID, "tr_"); err != nil {
		return payment.Payment{}, errors.New("mollie response lacks valid payment ID")
	}

	if !validStatus(paymentResource.Status) {
		return payment.Payment{}, errors.New("mollie response lacks valid status")
	}

	money, err := observedMoney(paymentResource.Amount)

	if err != nil {
		return payment.Payment{}, err
	}

	statuses := map[string]payment.Status{
		"open": payment.StatusRequiresAction, "pending": payment.StatusPending,
		"authorized": payment.StatusPending, "paid": payment.StatusSucceeded,
		"canceled": payment.StatusCanceled, "expired": payment.StatusFailed,
		"failed": payment.StatusFailed,
	}

	return payment.Payment{ID: payment.ID(paymentResource.ID), Status: statuses[paymentResource.Status],
		NativeStatus: paymentResource.Status, Amount: money}, nil
}

// Checkout shares the payment's identity but keeps approval state separate
// from money movement. Every returned amount pointer is newly allocated.
func (paymentResource Payment) Checkout() (payment.Checkout, error) {
	observed, err := paymentResource.Snapshot()

	if err != nil {
		return payment.Checkout{}, err
	}

	approval := paymentResource.Links.Checkout.Href

	if approval != "" && !ValidHTTPS(approval) {
		return payment.Checkout{}, errors.New("mollie response has invalid approval URL")
	}

	if paymentResource.Status == "open" && approval == "" {
		return payment.Checkout{}, errors.New("mollie open payment lacks approval URL")
	}

	statuses := map[string]payment.CheckoutStatus{
		"open": payment.CheckoutStatusOpen, "pending": payment.CheckoutStatusComplete,
		"authorized": payment.CheckoutStatusApproved, "paid": payment.CheckoutStatusComplete,
		"canceled": payment.CheckoutStatusCanceled, "expired": payment.CheckoutStatusExpired,
		"failed": payment.CheckoutStatusComplete,
	}

	return payment.Checkout{ID: payment.CheckoutID(paymentResource.ID), Kind: payment.CheckoutKindPayment,
		Status: statuses[paymentResource.Status], NativeStatus: paymentResource.Status, ApprovalURL: approval,
		PaymentID: observed.ID, PaymentStatus: observed.Status, Amount: &observed.Amount,
		Reference: metadataReference(paymentResource.Metadata)}, nil
}

// metadataReference reads only the application's reference from our
// object shape. Unrelated native metadata is ignored rather than coerced.
func metadataReference(metadata any) string {
	object, ok := metadata.(map[string]any)

	if !ok {
		return ""
	}

	reference, _ := object["reference"].(string)

	return reference
}

// Snapshot validates the refund's observed parent and own fields. The compound
// ID carries enough information for a later GET without SQL or a process cache.
func (refundResource Refund) Snapshot() (payment.Refund, error) {
	if err := NativeID(refundResource.ID, "re_"); err != nil {
		return payment.Refund{}, errors.New("mollie response lacks valid refund ID")
	}

	if err := NativeID(refundResource.PaymentID, "tr_"); err != nil {
		return payment.Refund{}, errors.New("mollie response lacks valid refund parent")
	}

	if !validStatus(refundResource.Status) {
		return payment.Refund{}, errors.New("mollie response lacks valid refund status")
	}

	money, err := observedMoney(refundResource.Amount)

	if err != nil {
		return payment.Refund{}, err
	}

	statuses := map[string]payment.RefundStatus{
		"queued": payment.RefundStatusPending, "pending": payment.RefundStatusPending,
		"processing": payment.RefundStatusPending, "refunded": payment.RefundStatusSucceeded,
		"failed": payment.RefundStatusFailed, "canceled": payment.RefundStatusCanceled,
	}

	return payment.Refund{ID: payment.RefundID(refundResource.PaymentID + ":" + refundResource.ID),
		PaymentID: payment.ID(refundResource.PaymentID), Status: statuses[refundResource.Status],
		NativeStatus: refundResource.Status, Amount: money}, nil
}

// validStatus accepts bounded lowercase machine tokens, including future
// underscore-separated values. Free-form native status text is never exposed.
func validStatus(value string) bool {
	return len(value) > 0 && len(value) <= 100 && strings.Trim(value, "abcdefghijklmnopqrstuvwxyz_") == ""
}
