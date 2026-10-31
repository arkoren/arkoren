package payment

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ClientChannel describes the intended client platform, not a payment method.
// Empty is valid and means unspecified; other values must be known constants.
//
// Example:
//
//	var channel payment.ClientChannel = payment.ClientChannelIOS
//	_ = channel
type ClientChannel string

const (
	// ClientChannelWeb identifies a browser handoff, without promising a redirect.
	// Example: details.Channel(payment.ClientChannelWeb)
	ClientChannelWeb ClientChannel = "web"
	// ClientChannelIOS identifies an iOS SDK handoff, not Apple Pay acceptance.
	// Example: details.Channel(payment.ClientChannelIOS)
	ClientChannelIOS ClientChannel = "ios"
	// ClientChannelAndroid identifies an Android SDK handoff, not Google Pay acceptance.
	// Example: details.Channel(payment.ClientChannelAndroid)
	ClientChannelAndroid ClientChannel = "android"
)

// PreparePaymentRequest immutably requests client-side initialization for a
// positive explicit amount. It is separate from hosted checkout and completion.
// Its zero value is invalid; no result or financial state is inferred from it.
//
// Example:
//
//	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "order-1")
//	if err := request.Err(); err != nil { return err }
type PreparePaymentRequest struct {
	// payload owns the scalar handoff request and its private JSON shape.
	payload preparePaymentPayload
	// err retains the first invalid setting even after subsequent replacement.
	err error
}

// preparePaymentPayload is the single private tagged preparation representation.
// Optional risk correlation is a constrained opaque string, never an arbitrary map.
type preparePaymentPayload struct {
	// Amount is positive integer money, never floating point.
	Amount Money `json:"amount"`
	// IdempotencyKey deduplicates this preparation within provider limits.
	IdempotencyKey string `json:"idempotency_key"`
	// Description is optional provider-visible text, at most 127 runes.
	Description string `json:"description,omitzero"`
	// Reference is optional application correlation text, at most 127 runes.
	Reference string `json:"reference,omitzero"`
	// Channel optionally names the intended client platform.
	Channel ClientChannel `json:"channel,omitzero"`
	// ReturnURL optionally supplies an application-approved client callback URI.
	ReturnURL string `json:"return_url,omitzero"`
	// DeviceReference is opaque provider risk correlation, at most 255 bytes.
	DeviceReference string `json:"device_reference,omitzero"`
}

// PreparePaymentRequestBuilder edits one fresh preparation draft. Invalid input
// is sticky; publication detaches the resulting scalar values from this draft.
//
// Example:
//
//	request := payment.NewPreparePaymentRequest(amount, "order-1", func(details *payment.PreparePaymentRequestBuilder) {
//		details.Channel(payment.ClientChannelIOS).ReturnURL("com.example.shop:/payment")
//	})
//	_ = request.Channel()
type PreparePaymentRequestBuilder struct {
	// request holds the draft's private values and construction error.
	request PreparePaymentRequest
}

// PreparePaymentRequestBuilderFunc supplies a synchronous preparation edit.
// Callbacks run once in order even on invalid input; nil entries panic.
//
// Example:
//
//	var configure payment.PreparePaymentRequestBuilderFunc = func(details *payment.PreparePaymentRequestBuilder) {
//		details.Reference("order-1")
//	}
//	_ = configure
type PreparePaymentRequestBuilderFunc = func(*PreparePaymentRequestBuilder)

// NewPreparePaymentRequest copies a positive amount and required key, then runs
// callbacks directly once in order, including invalid declarations. Nil entries
// panic; expanded nil/empty slices omit edits. No I/O or confirmation occurs.
//
// Example:
//
//	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 1200, Currency: "USD"}, "order-1")
//	if err := request.Err(); err != nil { return err }
func NewPreparePaymentRequest(amount Money, key string, configure ...PreparePaymentRequestBuilderFunc) PreparePaymentRequest {
	details := &PreparePaymentRequestBuilder{request: PreparePaymentRequest{payload: preparePaymentPayload{
		Amount: amount, IdempotencyKey: key,
	}}}

	details.request.err = details.request.Err()

	for _, configure := range configure {
		configure(details)
	}

	return details.request
}

// Description replaces optional provider-visible text, limited to 127 runes.
// Controls and malformed UTF-8 produce a sticky error; do not put secrets here.
//
// Example:
//
//	var configure payment.PreparePaymentRequestBuilderFunc = func(details *payment.PreparePaymentRequestBuilder) {
//		details.Description("Book order")
//	}
//	_ = configure
func (details *PreparePaymentRequestBuilder) Description(value string) *PreparePaymentRequestBuilder {
	details.request.err = firstError(details.request.err, checkText(value, "description", 127))
	details.request.payload.Description = value

	return details
}

// Reference replaces optional application correlation text, at most 127 runes.
// It is not authorization or idempotency and must contain no controls or secrets.
//
// Example:
//
//	var configure payment.PreparePaymentRequestBuilderFunc = func(details *payment.PreparePaymentRequestBuilder) {
//		details.Reference("order-1")
//	}
//	_ = configure
func (details *PreparePaymentRequestBuilder) Reference(value string) *PreparePaymentRequestBuilder {
	details.request.err = firstError(details.request.err, checkText(value, "reference", 127))
	details.request.payload.Reference = value

	return details
}

// Channel replaces the optional client platform. Empty means unspecified;
// unknown values are sticky errors rather than provider-specific escape hatches.
//
// Example:
//
//	var configure payment.PreparePaymentRequestBuilderFunc = func(details *payment.PreparePaymentRequestBuilder) {
//		details.Channel(payment.ClientChannelAndroid)
//	}
//	_ = configure
func (details *PreparePaymentRequestBuilder) Channel(value ClientChannel) *PreparePaymentRequestBuilder {
	details.request.err = firstError(details.request.err, checkChannel(value))
	details.request.payload.Channel = value

	return details
}

// ReturnURL sets an optional HTTPS, loopback HTTP, or syntactically valid custom
// native callback URI. Dangerous schemes, controls, and credentials are rejected.
// The application MUST authorize its own scheme/host allowlist before construction;
// syntax validation cannot establish scheme registration or application ownership.
// Hosted checkout's stricter redirect policy is unchanged.
//
// Example:
//
//	var configure payment.PreparePaymentRequestBuilderFunc = func(details *payment.PreparePaymentRequestBuilder) {
//		details.ReturnURL("com.example.shop:/payment")
//	}
//	_ = configure
func (details *PreparePaymentRequestBuilder) ReturnURL(value string) *PreparePaymentRequestBuilder {
	details.request.err = firstError(details.request.err, checkCallbackURL(value))
	details.request.payload.ReturnURL = value

	return details
}

// DeviceReference sets optional opaque provider risk correlation, at most 255
// bytes without whitespace, controls, or malformed UTF-8. Empty omits it. This
// value is not a general metadata bag, authorization, or payment instrument.
//
// Example:
//
//	var configure payment.PreparePaymentRequestBuilderFunc = func(details *payment.PreparePaymentRequestBuilder) {
//		details.DeviceReference("device-session-123")
//	}
//	_ = configure
func (details *PreparePaymentRequestBuilder) DeviceReference(value string) *PreparePaymentRequestBuilder {
	details.request.err = firstError(details.request.err, checkOptionalIdentity(value, "device_reference"))
	details.request.payload.DeviceReference = value

	return details
}

// Amount returns a detached Money copy. Mutating it does not change this request
// or imply that initialization has authorized or transferred any funds.
//
// Example:
//
//	_ = request.Amount().Currency
func (preparePaymentRequest PreparePaymentRequest) Amount() Money {
	return preparePaymentRequest.payload.Amount
}

// IdempotencyKey returns this preparation's required mutation key verbatim.
// Deduplication is provider-scoped and bounded by provider retention.
//
// Example:
//
//	_ = request.IdempotencyKey()
func (preparePaymentRequest PreparePaymentRequest) IdempotencyKey() string {
	return preparePaymentRequest.payload.IdempotencyKey
}

// Description returns optional provider-visible text without normalization.
// Empty means omitted rather than an inferred product description.
//
// Example:
//
//	_ = request.Description()
func (preparePaymentRequest PreparePaymentRequest) Description() string {
	return preparePaymentRequest.payload.Description
}

// Reference returns optional application correlation, not a resource identity.
// No provider lookup or authorization is implied by a nonempty value.
//
// Example:
//
//	_ = request.Reference()
func (preparePaymentRequest PreparePaymentRequest) Reference() string {
	return preparePaymentRequest.payload.Reference
}

// Channel returns the optional intended client platform. It does not guarantee
// that a provider enables any particular wallet or payment method.
//
// Example:
//
//	_ = request.Channel()
func (preparePaymentRequest PreparePaymentRequest) Channel() ClientChannel {
	return preparePaymentRequest.payload.Channel
}

// ReturnURL returns the optional client callback URI verbatim. The application
// owns its allowlist and must not treat syntax validation as destination trust.
//
// Example:
//
//	_ = request.ReturnURL()
func (preparePaymentRequest PreparePaymentRequest) ReturnURL() string {
	return preparePaymentRequest.payload.ReturnURL
}

// DeviceReference returns optional opaque provider risk correlation. It is not
// financial identity and should be excluded from ordinary logs and telemetry.
//
// Example:
//
//	_ = request.DeviceReference()
func (preparePaymentRequest PreparePaymentRequest) DeviceReference() string {
	return preparePaymentRequest.payload.DeviceReference
}

// Err reports the first construction error or invalid zero value without I/O.
// Required amount/key validation precedes optional settings; values are not echoed.
//
// Example:
//
//	if err := request.Err(); err != nil { return err }
func (preparePaymentRequest PreparePaymentRequest) Err() error {
	return firstError(preparePaymentRequest.err, checkPositiveMoney(preparePaymentRequest.payload.Amount),
		checkKey(preparePaymentRequest.payload.IdempotencyKey),
		checkText(preparePaymentRequest.payload.Description, "description", 127),
		checkText(preparePaymentRequest.payload.Reference, "reference", 127),
		checkChannel(preparePaymentRequest.payload.Channel), checkCallbackURL(preparePaymentRequest.payload.ReturnURL),
		checkOptionalIdentity(preparePaymentRequest.payload.DeviceReference, "device_reference"))
}

// Validate checks context before Err. Cancellation never becomes an uncertain
// mutation outcome before dispatch, even when the request is also invalid.
//
// Example:
//
//	if err := request.Validate(ctx); err != nil { return err }
func (preparePaymentRequest PreparePaymentRequest) Validate(ctx context.Context) error {
	if err := CheckContext(ctx); err != nil {
		return err
	}

	return preparePaymentRequest.Err()
}

// MarshalJSON emits the private snake_case payload only when valid. Remembered
// invalid settings cannot be erased by serializing their later replacements.
//
// Example:
//
//	data, err := json.Marshal(request)
//	if err != nil { return err }
//	_ = data
func (preparePaymentRequest PreparePaymentRequest) MarshalJSON() ([]byte, error) {
	if err := preparePaymentRequest.Err(); err != nil {
		return nil, err
	}

	return json.Marshal(preparePaymentRequest.payload)
}

// UnmarshalJSON validates fresh temporary state and commits only on success.
// Errors preserve the receiver's prior payload and validation state.
//
// Example:
//
//	if err := json.Unmarshal(data, &request); err != nil { return err }
func (preparePaymentRequest *PreparePaymentRequest) UnmarshalJSON(data []byte) error {
	var next PreparePaymentRequest

	if err := json.Unmarshal(data, &next.payload); err != nil {
		return err
	}

	if err := next.Err(); err != nil {
		return err
	}

	*preparePaymentRequest = next

	return nil
}

// callbackSchemePattern follows RFC 3986 scheme syntax, not a registration
// registry. Applications must separately authorize the exact callback destination.
var callbackSchemePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*$`)

// checkChannel enforces the portable platform vocabulary, including omission.
// Providers must not silently interpret unknown platforms differently.
func checkChannel(value ClientChannel) error {
	switch value {
	case "", ClientChannelWeb, ClientChannelIOS, ClientChannelAndroid:
		return nil
	default:
		return fmt.Errorf("%w: channel", ErrInvalidRequest)
	}
}

// checkOptionalIdentity permits omission while retaining the same opaque byte
// length, whitespace, control, and UTF-8 policy as provider references.
func checkOptionalIdentity(value, field string) error {
	if value == "" {
		return nil
	}

	return checkIdentity(value, field)
}

// checkCallbackURL adds native callback syntax only to preparation requests.
// Hosted HTTPS/loopback validation remains separate and is never relaxed.
func checkCallbackURL(value string) error {
	if value == "" {
		return nil
	}

	location, err := parseCallbackURL(value)

	if err != nil {
		return err
	}

	if location.Scheme == "https" || location.Scheme == "http" {
		return checkHostedURL(location)
	}

	return checkNativeCallback(location)
}

// parseCallbackURL rejects malformed UTF-8/escaping, controls, and fragments
// before credential and transport/native scheme policy is applied. It preserves
// the input string; decoded text is used only for detecting encoded controls.
func parseCallbackURL(value string) (*url.URL, error) {
	decoded, err := url.PathUnescape(value)

	if err != nil || !utf8.ValidString(decoded) {
		return nil, fmt.Errorf("%w: callback encoding", ErrInvalidRequest)
	}

	unsafe := strings.ContainsFunc(decoded, unicode.IsControl) || strings.ContainsFunc(value, unicode.IsSpace)

	if unsafe || strings.Contains(value, "#") {
		return nil, fmt.Errorf("%w: callback controls, whitespace, or fragment", ErrInvalidRequest)
	}

	location, err := url.Parse(value)

	if err != nil {
		return nil, fmt.Errorf("%w: callback syntax", ErrInvalidRequest)
	}

	return location, nil
}

// checkNativeCallback checks custom-scheme syntax and excludes executable,
// content, transport, and messaging schemes. It cannot verify app registration.
func checkNativeCallback(location *url.URL) error {
	if !callbackSchemePattern.MatchString(location.Scheme) || location.User != nil {
		return fmt.Errorf("%w: callback scheme or credentials", ErrInvalidRequest)
	}

	if location.Fragment != "" || location.String() == location.Scheme+":" {
		return fmt.Errorf("%w: callback destination", ErrInvalidRequest)
	}

	blocked := ",javascript,vbscript,data,file,content,about,blob,ftp,ftps,ws,wss,mailto,tel,sms,"

	if strings.Contains(blocked, ","+strings.ToLower(location.Scheme)+",") {
		return fmt.Errorf("%w: callback scheme", ErrInvalidRequest)
	}

	return nil
}
