package payment

import (
	"context"
	"encoding/json/v2"
)

// CompleteCheckoutRequest immutably identifies an explicit completion attempt.
// It cannot establish approval; the zero request is invalid.
//
// Example:
//
//	request := payment.NewCompleteCheckoutRequest("cs_example", "capture-1")
//	if err := request.Err(); err != nil { return err }
type CompleteCheckoutRequest struct {
	// payload owns the single private completion wire representation.
	payload completeCheckoutPayload
}

// completeCheckoutPayload preserves completion JSON without a public DTO twin.
// Required identity and key are checked at every dispatch and codec boundary.
type completeCheckoutPayload struct {
	// CheckoutID identifies the hosted checkout within the same driver.
	CheckoutID CheckoutID `json:"checkout_id"`
	// IdempotencyKey deduplicates completion, not approval creation.
	IdempotencyKey string `json:"idempotency_key"`
}

// NewCompleteCheckoutRequest snapshots completion identity and key without I/O.
// Invalid values remain observable through Err and fail validation or JSON.
//
// Example:
//
//	request := payment.NewCompleteCheckoutRequest("cs_example", "capture-1")
//	if err := request.Err(); err != nil { return err }
func NewCompleteCheckoutRequest(id CheckoutID, key string) CompleteCheckoutRequest {
	return CompleteCheckoutRequest{payload: completeCheckoutPayload{CheckoutID: id, IdempotencyKey: key}}
}

// CheckoutID returns the opaque hosted identity verbatim. It is not a payment
// identity and must not be used to infer financial success.
//
// Example:
//
//	_ = request.CheckoutID()
func (completeCheckoutRequest CompleteCheckoutRequest) CheckoutID() CheckoutID {
	return completeCheckoutRequest.payload.CheckoutID
}

// IdempotencyKey returns this completion attempt's key without normalization.
// Reuse it only for the same request within provider retention limits.
//
// Example:
//
//	_ = request.IdempotencyKey()
func (completeCheckoutRequest CompleteCheckoutRequest) IdempotencyKey() string {
	return completeCheckoutRequest.payload.IdempotencyKey
}

// Err validates identity and key without requiring a context. It reports domain
// errors under ErrInvalidRequest and performs no approval-state lookup.
//
// Example:
//
//	if err := request.Err(); err != nil { return err }
func (completeCheckoutRequest CompleteCheckoutRequest) Err() error {
	return firstError(checkIdentity(string(completeCheckoutRequest.payload.CheckoutID), "checkout_id"),
		checkKey(completeCheckoutRequest.payload.IdempotencyKey))
}

// Validate checks context first, then Err. Provider approval requirements remain
// the driver's responsibility after this pure preflight check.
//
// Example:
//
//	if err := request.Validate(ctx); err != nil { return err }
func (completeCheckoutRequest CompleteCheckoutRequest) Validate(ctx context.Context) error {
	if err := CheckContext(ctx); err != nil {
		return err
	}

	return completeCheckoutRequest.Err()
}

// MarshalJSON encodes only valid completion requests with the existing tags.
// Encoding an invalid zero value returns its domain validation error.
//
// Example:
//
//	data, err := json.Marshal(request)
//	if err != nil { return err }
//	_ = data
func (completeCheckoutRequest CompleteCheckoutRequest) MarshalJSON() ([]byte, error) {
	if err := completeCheckoutRequest.Err(); err != nil {
		return nil, err
	}

	return json.Marshal(completeCheckoutRequest.payload)
}

// UnmarshalJSON validates temporary state before replacing the request. Failed
// input never partially overwrites a previously valid completion request.
//
// Example:
//
//	if err := json.Unmarshal(data, &request); err != nil { return err }
func (completeCheckoutRequest *CompleteCheckoutRequest) UnmarshalJSON(data []byte) error {
	var next CompleteCheckoutRequest

	if err := json.Unmarshal(data, &next.payload); err != nil {
		return err
	}

	if err := next.Err(); err != nil {
		return err
	}

	*completeCheckoutRequest = next

	return nil
}

// RefundRequest immutably requests a positive explicit refund. Zero never means
// a full refund; the provider enforces remaining balance and currency agreement.
//
// Example:
//
//	request := payment.NewRefundRequest("pi_example", payment.Money{Amount: 500, Currency: "USD"}, "refund-1")
//	if err := request.Err(); err != nil { return err }
type RefundRequest struct {
	// payload owns payment identity, Money value, and mutation key.
	payload refundPayload
}

// refundPayload is the single private tagged refund representation. No mutable
// Money pointer or caller-owned container is retained.
type refundPayload struct {
	// PaymentID identifies the original refundable money movement.
	PaymentID ID `json:"payment_id"`
	// Amount is an explicit positive amount in the original currency.
	Amount Money `json:"amount"`
	// IdempotencyKey identifies the refund, not the original payment.
	IdempotencyKey string `json:"idempotency_key"`
}

// NewRefundRequest copies its explicit amount and identities without I/O.
// Invalid inputs fail Err, dispatch validation, and JSON encoding.
//
// Example:
//
//	request := payment.NewRefundRequest("pi_example", payment.Money{Amount: 500, Currency: "USD"}, "refund-1")
//	if err := request.Err(); err != nil { return err }
func NewRefundRequest(id ID, amount Money, key string) RefundRequest {
	return RefundRequest{payload: refundPayload{PaymentID: id, Amount: amount, IdempotencyKey: key}}
}

// PaymentID returns the opaque original money-movement identity. It does not
// identify a checkout and does not guarantee refundable balance.
//
// Example:
//
//	_ = request.PaymentID()
func (refundRequest RefundRequest) PaymentID() ID { return refundRequest.payload.PaymentID }

// Amount returns a Money copy, never an alias into the request. Zero is invalid
// here even though it is valid in a general Money observation.
//
// Example:
//
//	_ = request.Amount().Currency
func (refundRequest RefundRequest) Amount() Money { return refundRequest.payload.Amount }

// IdempotencyKey returns this refund's key verbatim. Reuse with a different
// amount is invalid even when the provider retains the original request.
//
// Example:
//
//	_ = request.IdempotencyKey()
func (refundRequest RefundRequest) IdempotencyKey() string {
	return refundRequest.payload.IdempotencyKey
}

// Err checks identity, positive money, and key without I/O or a context. It
// cannot check provider balance and never performs an implicit full refund.
//
// Example:
//
//	if err := request.Err(); err != nil { return err }
func (refundRequest RefundRequest) Err() error {
	return firstError(checkIdentity(string(refundRequest.payload.PaymentID), "payment_id"),
		checkPositiveMoney(refundRequest.payload.Amount), checkKey(refundRequest.payload.IdempotencyKey))
}

// Validate preserves context failures before domain errors. Provider balance
// checks occur only after portable validation succeeds.
//
// Example:
//
//	if err := request.Validate(ctx); err != nil { return err }
func (refundRequest RefundRequest) Validate(ctx context.Context) error {
	if err := CheckContext(ctx); err != nil {
		return err
	}

	return refundRequest.Err()
}

// MarshalJSON preserves the refund wire shape and rejects invalid values.
// In particular it cannot encode an implicit zero-amount full refund.
//
// Example:
//
//	data, err := json.Marshal(request)
//	if err != nil { return err }
//	_ = data
func (refundRequest RefundRequest) MarshalJSON() ([]byte, error) {
	if err := refundRequest.Err(); err != nil {
		return nil, err
	}

	return json.Marshal(refundRequest.payload)
}

// UnmarshalJSON validates fresh state and commits only on success. Malformed or
// invalid input leaves the original request unchanged.
//
// Example:
//
//	if err := json.Unmarshal(data, &request); err != nil { return err }
func (refundRequest *RefundRequest) UnmarshalJSON(data []byte) error {
	var next RefundRequest

	if err := json.Unmarshal(data, &next.payload); err != nil {
		return err
	}

	if err := next.Err(); err != nil {
		return err
	}

	*refundRequest = next

	return nil
}

// CancelSubscriptionRequest requests immediate cancellation without refunds or
// proration. Construct it explicitly; the zero request is invalid.
//
// Example:
//
//	request := payment.NewCancelSubscriptionRequest("sub_example")
//	if err := request.Err(); err != nil { return err }
type CancelSubscriptionRequest struct {
	// payload owns the cancellation's scalar wire values.
	payload cancelSubscriptionPayload
	// err remembers invalid optional reason input across replacements.
	err error
}

// cancelSubscriptionPayload preserves the cancellation wire shape privately.
// There is no scheduled end-of-period or hidden refund setting.
type cancelSubscriptionPayload struct {
	// SubscriptionID identifies the subscription to cancel, not its plan.
	SubscriptionID SubscriptionID `json:"subscription_id"`
	// Reason is optional provider-visible text, at most 128 runes.
	Reason string `json:"reason,omitzero"`
}

// CancelSubscriptionRequestBuilder edits a fresh cancellation draft. Publication
// copies it, so later edits through a retained draft cannot change requests.
//
// Example:
//
//	request := payment.NewCancelSubscriptionRequest("sub_example", func(details *payment.CancelSubscriptionRequestBuilder) {
//		details.Reason("Customer request")
//	})
//	_ = request.Reason()
type CancelSubscriptionRequestBuilder struct {
	// request owns draft values and sticky validation state.
	request CancelSubscriptionRequest
}

// CancelSubscriptionRequestBuilderFunc edits cancellation synchronously. All
// callbacks run once in order even on invalid input; nil entries panic.
//
// Example:
//
//	var configure payment.CancelSubscriptionRequestBuilderFunc = func(details *payment.CancelSubscriptionRequestBuilder) {
//		details.Reason("Customer request")
//	}
//	_ = configure
type CancelSubscriptionRequestBuilderFunc = func(*CancelSubscriptionRequestBuilder)

// NewCancelSubscriptionRequest snapshots an immediate-cancellation request.
// Callbacks run directly once in order even on invalid identity; nil entries
// panic and expanded nil/empty slices omit optional edits. No I/O occurs.
//
// Example:
//
//	request := payment.NewCancelSubscriptionRequest("sub_example")
//	if err := request.Err(); err != nil { return err }
func NewCancelSubscriptionRequest(id SubscriptionID, configure ...CancelSubscriptionRequestBuilderFunc) CancelSubscriptionRequest {
	details := &CancelSubscriptionRequestBuilder{request: CancelSubscriptionRequest{
		payload: cancelSubscriptionPayload{SubscriptionID: id},
	}}

	details.request.err = details.request.Err()

	for _, configure := range configure {
		configure(details)
	}

	return details.request
}

// Reason replaces optional provider-visible text, at most 128 runes with no
// controls. Invalid settings remain errors even after a valid replacement.
//
// Example:
//
//	var configure payment.CancelSubscriptionRequestBuilderFunc = func(details *payment.CancelSubscriptionRequestBuilder) {
//		details.Reason("Customer request")
//	}
//	_ = configure
func (details *CancelSubscriptionRequestBuilder) Reason(value string) *CancelSubscriptionRequestBuilder {
	details.request.err = firstError(details.request.err, checkText(value, "reason", 128))
	details.request.payload.Reason = value

	return details
}

// SubscriptionID returns the opaque subscription identity without a lookup.
// It is not a plan reference or proof that cancellation is currently allowed.
//
// Example:
//
//	_ = request.SubscriptionID()
func (cancelSubscriptionRequest CancelSubscriptionRequest) SubscriptionID() SubscriptionID {
	return cancelSubscriptionRequest.payload.SubscriptionID
}

// Reason returns optional provider-visible cancellation text verbatim. Empty
// means omitted; no default explanation is fabricated.
//
// Example:
//
//	_ = request.Reason()
func (cancelSubscriptionRequest CancelSubscriptionRequest) Reason() string {
	return cancelSubscriptionRequest.payload.Reason
}

// Err checks identity and sticky reason validation without context or I/O.
// It does not infer lifecycle, calculate proration, or issue refunds.
//
// Example:
//
//	if err := request.Err(); err != nil { return err }
func (cancelSubscriptionRequest CancelSubscriptionRequest) Err() error {
	return firstError(cancelSubscriptionRequest.err,
		checkIdentity(string(cancelSubscriptionRequest.payload.SubscriptionID), "subscription_id"),
		checkText(cancelSubscriptionRequest.payload.Reason, "reason", 128))
}

// Validate checks context first, then Err. An expired operation never reaches
// the driver even if the request also contains invalid input.
//
// Example:
//
//	if err := request.Validate(ctx); err != nil { return err }
func (cancelSubscriptionRequest CancelSubscriptionRequest) Validate(ctx context.Context) error {
	if err := CheckContext(ctx); err != nil {
		return err
	}

	return cancelSubscriptionRequest.Err()
}

// MarshalJSON preserves cancellation tags and omits empty reason. Any remembered
// construction error prevents encoding, even after a later valid replacement.
//
// Example:
//
//	data, err := json.Marshal(request)
//	if err != nil { return err }
//	_ = data
func (cancelSubscriptionRequest CancelSubscriptionRequest) MarshalJSON() ([]byte, error) {
	if err := cancelSubscriptionRequest.Err(); err != nil {
		return nil, err
	}

	return json.Marshal(cancelSubscriptionRequest.payload)
}

// UnmarshalJSON validates a fresh temporary value before replacing the receiver.
// A failure leaves its previous payload and validation state untouched.
//
// Example:
//
//	if err := json.Unmarshal(data, &request); err != nil { return err }
func (cancelSubscriptionRequest *CancelSubscriptionRequest) UnmarshalJSON(data []byte) error {
	var next CancelSubscriptionRequest

	if err := json.Unmarshal(data, &next.payload); err != nil {
		return err
	}

	if err := next.Err(); err != nil {
		return err
	}

	*cancelSubscriptionRequest = next

	return nil
}
