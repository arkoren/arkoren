package payment

import (
	"context"
	"encoding/json/v2"
)

// CheckoutRequest is an immutable hosted approval request. Its zero value is
// invalid; JSON preserves the hosted wire shape and rejects invalid values.
//
// Example:
//
//	request := payment.NewCheckoutRequest(amount, "https://shop.example/paid", "order-1")
//	if err := request.Err(); err != nil { return err }
type CheckoutRequest struct {
	// payload owns scalar wire values, detached from the construction draft.
	payload checkoutPayload
	// err remembers the first invalid input, even after replacement.
	err error
}

// checkoutPayload is the single private hosted wire representation. Money is
// copied by value, so the payload contains no caller-owned mutable state.
type checkoutPayload struct {
	// Amount is positive integer money in reviewed currency minor units.
	Amount Money `json:"amount"`
	// Description is optional provider-visible text, at most 127 runes.
	Description string `json:"description,omitzero"`
	// ReturnURL requires HTTPS, or loopback HTTP for local development.
	ReturnURL string `json:"return_url"`
	// CancelURL optionally follows the same policy as ReturnURL.
	CancelURL string `json:"cancel_url,omitzero"`
	// Reference is optional application correlation text, at most 127 runes.
	Reference string `json:"reference,omitzero"`
	// IdempotencyKey is the required 1–38 byte provider-common mutation key.
	IdempotencyKey string `json:"idempotency_key"`
}

// CheckoutRequestBuilder edits one fresh hosted request draft. Retaining it
// cannot change published requests; invalid settings remain sticky.
//
// Example:
//
//	request := payment.NewCheckoutRequest(amount, "https://shop.example/paid", "order-1",
//		func(details *payment.CheckoutRequestBuilder) {
//			details.Description("Book")
//		})
//	_ = request.Description()
type CheckoutRequestBuilder struct {
	// request holds draft-owned values and the first construction error.
	request CheckoutRequest
}

// CheckoutRequestBuilderFunc edits a hosted request synchronously. Callbacks run
// once in order, including invalid declarations; supplied nil entries panic.
//
// Example:
//
//	var describe payment.CheckoutRequestBuilderFunc = func(details *payment.CheckoutRequestBuilder) {
//		details.Description("Book")
//	}
//	_ = payment.NewCheckoutRequest(amount, "https://shop.example/paid", "order-1", describe)
type CheckoutRequestBuilderFunc = func(*CheckoutRequestBuilder)

// NewCheckoutRequest publishes a detached hosted request without I/O. Required
// input errors precede optional errors. Callbacks run once synchronously in order
// even on invalid input; nil entries panic and empty callback slices omit edits.
//
// Example:
//
//	request := payment.NewCheckoutRequest(payment.Money{Amount: 2500, Currency: "USD"},
//		"https://shop.example/paid", "order-1")
//	if err := request.Err(); err != nil { return err }
func NewCheckoutRequest(amount Money, returnURL, key string, configure ...CheckoutRequestBuilderFunc) CheckoutRequest {
	details := &CheckoutRequestBuilder{request: CheckoutRequest{payload: checkoutPayload{
		Amount: amount, ReturnURL: returnURL, IdempotencyKey: key,
	}}}

	details.request.err = details.request.Err()

	for _, configure := range configure {
		configure(details)
	}

	return details.request
}

// Description replaces optional text, limited to 127 runes without controls.
// Invalid input remains an error even if another call replaces the value.
//
// Example:
//
//	var describe payment.CheckoutRequestBuilderFunc = func(details *payment.CheckoutRequestBuilder) {
//		details.Description("Book order")
//	}
//	_ = describe
func (details *CheckoutRequestBuilder) Description(value string) *CheckoutRequestBuilder {
	details.request.err = firstError(details.request.err, checkText(value, "description", 127))
	details.request.payload.Description = value

	return details
}

// CancelURL replaces the optional hosted cancellation location. Empty omits it;
// nonempty input requires HTTPS or loopback HTTP, never native schemes.
//
// Example:
//
//	var cancel payment.CheckoutRequestBuilderFunc = func(details *payment.CheckoutRequestBuilder) {
//		details.CancelURL("https://shop.example/cart")
//	}
//	_ = cancel
func (details *CheckoutRequestBuilder) CancelURL(value string) *CheckoutRequestBuilder {
	details.request.err = firstError(details.request.err, checkOptionalHostedURL(value))
	details.request.payload.CancelURL = value

	return details
}

// Reference replaces optional application correlation text, at most 127 runes.
// It must contain no controls or secrets and is not an idempotency key.
//
// Example:
//
//	var correlate payment.CheckoutRequestBuilderFunc = func(details *payment.CheckoutRequestBuilder) {
//		details.Reference("order-123")
//	}
//	_ = correlate
func (details *CheckoutRequestBuilder) Reference(value string) *CheckoutRequestBuilder {
	details.request.err = firstError(details.request.err, checkText(value, "reference", 127))
	details.request.payload.Reference = value

	return details
}

// Amount returns a Money copy. Changing that copy cannot mutate the request.
// Currency and amount remain exactly as supplied, without normalization.
//
// Example:
//
//	amount := request.Amount()
//	_ = amount.Currency
func (checkoutRequest CheckoutRequest) Amount() Money { return checkoutRequest.payload.Amount }

// Description returns optional provider-visible text verbatim. Empty means no
// description was supplied, not a provider-generated description.
//
// Example:
//
//	_ = request.Description()
func (checkoutRequest CheckoutRequest) Description() string {
	return checkoutRequest.payload.Description
}

// ReturnURL returns the hosted return location verbatim, preserving the literal
// {CHECKOUT_SESSION_ID} placeholder supported by hosted providers.
//
// Example:
//
//	_ = request.ReturnURL()
func (checkoutRequest CheckoutRequest) ReturnURL() string { return checkoutRequest.payload.ReturnURL }

// CancelURL returns the optional cancellation location. Empty means omitted;
// individual providers can require a nonempty value.
//
// Example:
//
//	_ = request.CancelURL()
func (checkoutRequest CheckoutRequest) CancelURL() string { return checkoutRequest.payload.CancelURL }

// Reference returns application correlation text, not provider resource identity.
// Its presence never implies authorization or a successful lookup.
//
// Example:
//
//	_ = request.Reference()
func (checkoutRequest CheckoutRequest) Reference() string { return checkoutRequest.payload.Reference }

// IdempotencyKey returns the mutation key verbatim. Reuse only for the same
// payload within the provider's account scope and retention period.
//
// Example:
//
//	_ = request.IdempotencyKey()
func (checkoutRequest CheckoutRequest) IdempotencyKey() string {
	return checkoutRequest.payload.IdempotencyKey
}

// Err reports the first invalid construction input or invalid zero value.
// Unsupported currencies wrap ErrUnsupported; other portable validation failures
// wrap ErrInvalidRequest. Diagnostics disclose no input values and require no
// context or I/O. The original error stays sticky after later builder edits.
//
// Example:
//
//	request := payment.NewCheckoutRequest(payment.Money{Amount: 100, Currency: "XYZ"},
//		"https://shop.example/paid", "order-1")
//	if err := request.Err(); err != nil {
//		if errors.Is(err, payment.ErrUnsupported) {
//			return fmt.Errorf("select a supported currency: %w", err)
//		}
//		return err
//	}
func (checkoutRequest CheckoutRequest) Err() error {
	return firstError(checkoutRequest.err, checkPositiveMoney(checkoutRequest.payload.Amount),
		checkCheckoutText(checkoutRequest.payload.Description, checkoutRequest.payload.Reference,
			checkoutRequest.payload.IdempotencyKey),
		checkRedirects(checkoutRequest.payload.ReturnURL, checkoutRequest.payload.CancelURL))
}

// Validate checks context before Err, preserving cancellation before domain
// errors or dispatch. It performs no provider or lifecycle lookup.
//
// Example:
//
//	if err := request.Validate(ctx); err != nil { return err }
func (checkoutRequest CheckoutRequest) Validate(ctx context.Context) error {
	if err := CheckContext(ctx); err != nil {
		return err
	}

	return checkoutRequest.Err()
}

// MarshalJSON emits the existing snake_case hosted shape only when valid.
// Invalid construction errors are returned rather than serialized.
//
// Example:
//
//	data, err := json.Marshal(request)
//	if err != nil { return err }
//	_ = data
func (checkoutRequest CheckoutRequest) MarshalJSON() ([]byte, error) {
	if err := checkoutRequest.Err(); err != nil {
		return nil, err
	}

	return json.Marshal(checkoutRequest.payload)
}

// UnmarshalJSON validates fresh state and replaces the receiver only on success.
// A failed decode preserves the previous value, including any remembered error.
//
// Example:
//
//	var request payment.CheckoutRequest
//	if err := json.Unmarshal(data, &request); err != nil { return err }
func (checkoutRequest *CheckoutRequest) UnmarshalJSON(data []byte) error {
	var next CheckoutRequest

	if err := json.Unmarshal(data, &next.payload); err != nil {
		return err
	}

	if err := next.Err(); err != nil {
		return err
	}

	*checkoutRequest = next

	return nil
}

// firstError preserves construction precedence without joining diagnostics.
// Callers supply pure validation results which never disclose input values.
func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

// checkOptionalHostedURL handles omission without weakening the required hosted
// URL validator. This policy deliberately excludes native callback schemes.
func checkOptionalHostedURL(value string) error {
	if value == "" {
		return nil
	}

	return checkURL(value)
}

// SubscriptionCheckoutRequest is an immutable approval request for an existing
// provider plan. It creates no catalog, invoice, or entitlement; zero is invalid.
//
// Example:
//
//	request := payment.NewSubscriptionCheckoutRequest("price_monthly", "https://shop.example/account", "signup-1")
//	if err := request.Err(); err != nil { return err }
type SubscriptionCheckoutRequest struct {
	// payload owns the scalar request values used by drivers and JSON.
	payload subscriptionCheckoutPayload
	// err preserves the first invalid construction input.
	err error
}

// subscriptionCheckoutPayload is the private, stable hosted subscription shape.
// It contains no price or financial result inferred from the plan reference.
type subscriptionCheckoutPayload struct {
	// PlanReference identifies an existing provider plan, at most 255 bytes.
	PlanReference string `json:"plan_reference"`
	// ReturnURL is the required hosted HTTPS or loopback HTTP return location.
	ReturnURL string `json:"return_url"`
	// CancelURL optionally follows the hosted return policy.
	CancelURL string `json:"cancel_url,omitzero"`
	// Reference is optional application correlation text, at most 127 runes.
	Reference string `json:"reference,omitzero"`
	// IdempotencyKey scopes provider-bounded duplicate prevention.
	IdempotencyKey string `json:"idempotency_key"`
}

// SubscriptionCheckoutRequestBuilder edits a fresh subscription approval draft.
// Publication copies its values, and invalid settings cannot be repaired.
//
// Example:
//
//	request := payment.NewSubscriptionCheckoutRequest("plan-1", "https://shop.example/account", "signup-1",
//		func(details *payment.SubscriptionCheckoutRequestBuilder) {
//			details.Reference("customer-order")
//		})
//	_ = request.Reference()
type SubscriptionCheckoutRequestBuilder struct {
	// request contains draft-owned scalar values and sticky validation state.
	request SubscriptionCheckoutRequest
}

// SubscriptionCheckoutRequestBuilderFunc supplies one ordered synchronous edit.
// Nil entries panic; expanded nil or empty callback slices supply no edits.
//
// Example:
//
//	var configure payment.SubscriptionCheckoutRequestBuilderFunc = func(details *payment.SubscriptionCheckoutRequestBuilder) {
//		details.CancelURL("https://shop.example/plans")
//	}
//	_ = configure
type SubscriptionCheckoutRequestBuilderFunc = func(*SubscriptionCheckoutRequestBuilder)

// NewSubscriptionCheckoutRequest snapshots a request without I/O. Required
// validation runs first; all callbacks run once in order even on invalid input.
// Supplied nil callbacks panic. Retained drafts cannot change the result.
//
// Example:
//
//	request := payment.NewSubscriptionCheckoutRequest("price_monthly", "https://shop.example/account", "signup-1")
//	if err := request.Err(); err != nil { return err }
func NewSubscriptionCheckoutRequest(plan, returnURL, key string,
	configure ...SubscriptionCheckoutRequestBuilderFunc,
) SubscriptionCheckoutRequest {
	details := &SubscriptionCheckoutRequestBuilder{request: SubscriptionCheckoutRequest{
		payload: subscriptionCheckoutPayload{PlanReference: plan, ReturnURL: returnURL, IdempotencyKey: key},
	}}

	details.request.err = details.request.Err()

	for _, configure := range configure {
		configure(details)
	}

	return details.request
}

// CancelURL sets an optional hosted cancellation location. Empty omits it;
// invalid HTTPS/loopback policy input remains sticky after later replacements.
//
// Example:
//
//	var configure payment.SubscriptionCheckoutRequestBuilderFunc = func(details *payment.SubscriptionCheckoutRequestBuilder) {
//		details.CancelURL("https://shop.example/plans")
//	}
//	_ = configure
func (details *SubscriptionCheckoutRequestBuilder) CancelURL(value string) *SubscriptionCheckoutRequestBuilder {
	details.request.err = firstError(details.request.err, checkOptionalHostedURL(value))
	details.request.payload.CancelURL = value

	return details
}

// Reference sets optional application correlation text, at most 127 runes with
// no controls. Do not include credentials or sensitive customer data.
//
// Example:
//
//	var configure payment.SubscriptionCheckoutRequestBuilderFunc = func(details *payment.SubscriptionCheckoutRequestBuilder) {
//		details.Reference("signup-123")
//	}
//	_ = configure
func (details *SubscriptionCheckoutRequestBuilder) Reference(value string) *SubscriptionCheckoutRequestBuilder {
	details.request.err = firstError(details.request.err, checkText(value, "reference", 127))
	details.request.payload.Reference = value

	return details
}

// PlanReference returns the existing provider plan identity verbatim. It is not
// an application plan name and does not establish that the plan exists.
//
// Example:
//
//	_ = request.PlanReference()
func (subscriptionCheckoutRequest SubscriptionCheckoutRequest) PlanReference() string {
	return subscriptionCheckoutRequest.payload.PlanReference
}

// ReturnURL returns the required hosted return location without normalization.
// Literal checkout-session placeholders remain unchanged.
//
// Example:
//
//	_ = request.ReturnURL()
func (subscriptionCheckoutRequest SubscriptionCheckoutRequest) ReturnURL() string {
	return subscriptionCheckoutRequest.payload.ReturnURL
}

// CancelURL returns the optional hosted cancellation location. Empty means
// omitted, not a default derived from ReturnURL.
//
// Example:
//
//	_ = request.CancelURL()
func (subscriptionCheckoutRequest SubscriptionCheckoutRequest) CancelURL() string {
	return subscriptionCheckoutRequest.payload.CancelURL
}

// Reference returns application correlation text without lookup or authorization.
// The value may be empty when the caller omitted it.
//
// Example:
//
//	_ = request.Reference()
func (subscriptionCheckoutRequest SubscriptionCheckoutRequest) Reference() string {
	return subscriptionCheckoutRequest.payload.Reference
}

// IdempotencyKey returns the key for this approval mutation, not cancellation.
// Its deduplication scope and retention remain provider-bounded.
//
// Example:
//
//	_ = request.IdempotencyKey()
func (subscriptionCheckoutRequest SubscriptionCheckoutRequest) IdempotencyKey() string {
	return subscriptionCheckoutRequest.payload.IdempotencyKey
}

// Err returns the first construction error or invalid-zero error. It performs
// pure validation and never checks provider plan existence.
//
// Example:
//
//	if err := request.Err(); err != nil { return err }
func (subscriptionCheckoutRequest SubscriptionCheckoutRequest) Err() error {
	return firstError(subscriptionCheckoutRequest.err,
		checkIdentity(subscriptionCheckoutRequest.payload.PlanReference, "plan_reference"),
		checkCheckoutText("", subscriptionCheckoutRequest.payload.Reference, subscriptionCheckoutRequest.payload.IdempotencyKey),
		checkRedirects(subscriptionCheckoutRequest.payload.ReturnURL, subscriptionCheckoutRequest.payload.CancelURL))
}

// Validate checks context before construction validity. Cancellation wins over
// request errors without provider I/O or side effects.
//
// Example:
//
//	if err := request.Validate(ctx); err != nil { return err }
func (subscriptionCheckoutRequest SubscriptionCheckoutRequest) Validate(ctx context.Context) error {
	if err := CheckContext(ctx); err != nil {
		return err
	}

	return subscriptionCheckoutRequest.Err()
}

// MarshalJSON preserves the hosted subscription wire shape. Invalid requests
// fail encoding rather than silently dropping private validation state.
//
// Example:
//
//	data, err := json.Marshal(request)
//	if err != nil { return err }
//	_ = data
func (subscriptionCheckoutRequest SubscriptionCheckoutRequest) MarshalJSON() ([]byte, error) {
	if err := subscriptionCheckoutRequest.Err(); err != nil {
		return nil, err
	}

	return json.Marshal(subscriptionCheckoutRequest.payload)
}

// UnmarshalJSON decodes and validates temporary state before publication. Errors
// leave the receiver unchanged, including on partial or malformed input.
//
// Example:
//
//	if err := json.Unmarshal(data, &request); err != nil { return err }
func (subscriptionCheckoutRequest *SubscriptionCheckoutRequest) UnmarshalJSON(data []byte) error {
	var next SubscriptionCheckoutRequest

	if err := json.Unmarshal(data, &next.payload); err != nil {
		return err
	}

	if err := next.Err(); err != nil {
		return err
	}

	*subscriptionCheckoutRequest = next

	return nil
}
