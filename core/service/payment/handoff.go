package payment

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"regexp"
)

// ClientProtocol names an actual provider/client handoff protocol, not a generic
// payment method. Lowercase identifiers with optional dot-separated components
// up to 255 bytes are accepted; underscores and hyphens are permitted in each
// component after its initial letter. Empty components are invalid.
// Third-party adapters can define their own identifiers without registration.
//
// Example:
//
//	action := payment.NewClientAuthorization(payment.ClientProtocol("stripe.payment_intent"), "client-secret")
//	if err := action.Err(); err != nil { return err }
type ClientProtocol string

const (
	// ClientProtocolStripePaymentIntent identifies a Stripe PaymentIntent secret.
	// Example: action := payment.NewClientAuthorization(payment.ClientProtocolStripePaymentIntent, "secret"); _ = action
	ClientProtocolStripePaymentIntent ClientProtocol = "stripe.payment_intent"
	// ClientProtocolPayPalOrder identifies a PayPal client order handoff.
	// Example: action := payment.NewClientOrder(payment.ClientProtocolPayPalOrder, "ORDER-1"); _ = action
	ClientProtocolPayPalOrder ClientProtocol = "paypal.order"
	// ClientProtocolMemoryAuthorization identifies an inert simulator token.
	// Example: action := payment.NewClientAuthorization(payment.ClientProtocolMemoryAuthorization, "simulated-1"); _ = action
	ClientProtocolMemoryAuthorization ClientProtocol = "memory.authorization"
)

// ClientActionKind is the discriminant for the concrete immutable ClientAction.
// Unknown or empty kinds are invalid rather than an unspecified success.
//
// Example:
//
//	if action.Kind() == payment.ClientActionKindAuthorization { _, _ = action.Authorization() }
type ClientActionKind string

const (
	// ClientActionKindAuthorization selects the authorization-token variant.
	// Example: _ = action.Kind() == payment.ClientActionKindAuthorization
	ClientActionKindAuthorization ClientActionKind = "authorization"
	// ClientActionKindOrder selects the client-order variant, not a paid payment.
	// Example: _ = action.Kind() == payment.ClientActionKindOrder
	ClientActionKindOrder ClientActionKind = "order"
	// ClientActionKindSession selects the opaque client-session variant.
	// Example: _ = action.Kind() == payment.ClientActionKindSession
	ClientActionKindSession ClientActionKind = "session"
)

// ClientAuthorization is an authorized client handoff record, never evidence of
// settlement. Diagnostics redact all fields; JSON intentionally exposes the token
// for delivery only to the authorized client. Treat copies as credentials.
//
// Example:
//
//	if authorization, ok := action.Authorization(); ok { _ = authorization.Token }
type ClientAuthorization struct {
	// Protocol identifies the SDK protocol that must consume Token.
	// Example: if authorization, ok := action.Authorization(); ok { _ = authorization.Protocol }
	Protocol ClientProtocol `json:"protocol"`
	// Token is secret handoff material, not a payment status or authentication key.
	// Example: if authorization, ok := action.Authorization(); ok { _ = authorization.Token }
	Token string `json:"token"`
}

// ClientOrder is an SDK order handoff, not a financial payment observation.
// Diagnostics are redacted; JSON is intentionally client-visible and must be
// delivered only after application authorization.
//
// Example:
//
//	if order, ok := action.Order(); ok { _ = order.OrderID }
type ClientOrder struct {
	// Protocol identifies the SDK protocol that understands OrderID.
	// Example: if order, ok := action.Order(); ok { _ = order.Protocol }
	Protocol ClientProtocol `json:"protocol"`
	// OrderID is the provider's client order, not payment.ID or proof of capture.
	// Example: if order, ok := action.Order(); ok { _ = order.OrderID }
	OrderID string `json:"order_id"`
}

// ClientSession contains an opaque SDK session, not an arbitrary JSON metadata
// bag. Session data may be secret; diagnostics redact it while JSON intentionally
// exposes it to an authorized client. No financial resource need exist yet.
//
// Example:
//
//	if session, ok := action.Session(); ok { _ = session.Data }
type ClientSession struct {
	// Protocol identifies the SDK protocol that must consume the session.
	// Example: if session, ok := action.Session(); ok { _ = session.Protocol }
	Protocol ClientProtocol `json:"protocol"`
	// SessionID is client-session identity, never financial payment identity.
	// Example: if session, ok := action.Session(); ok { _ = session.SessionID }
	SessionID SessionID `json:"session_id"`
	// Data is an opaque SDK string, not a toolkit-interpreted JSON object.
	// Example: if session, ok := action.Session(); ok { _ = session.Data }
	Data string `json:"data"`
}

// ClientAction is a concrete immutable, privately tagged handoff union. Only one
// variant can be constructed; Err rejects zero, unknown tags, or mixed JSON
// variants. Typed accessors return scalar copies. Diagnostics redact handoff
// values, while JSON deliberately includes them for the authorized client.
//
// Example:
//
//	action := payment.NewClientOrder("paypal.order", "ORDER-1")
//	if err := action.Err(); err != nil { return err }
//	order, ok := action.Order()
//	_ = order; _ = ok
type ClientAction struct {
	// payload is the sole private discriminated JSON representation.
	payload clientActionPayload
}

// clientActionPayload keeps the wire discriminant explicit. Validation prohibits
// fields from inactive variants; no interface, arbitrary JSON, or map is used.
type clientActionPayload struct {
	// Kind selects exactly one variant.
	Kind ClientActionKind `json:"kind"`
	// Protocol identifies the provider SDK protocol, not a global registry entry.
	Protocol ClientProtocol `json:"protocol"`
	// Token belongs only to authorization.
	Token string `json:"token,omitzero"`
	// OrderID belongs only to order.
	OrderID string `json:"order_id,omitzero"`
	// SessionID belongs only to session.
	SessionID SessionID `json:"session_id,omitzero"`
	// Data belongs only to session and remains an opaque string.
	Data string `json:"data,omitzero"`
}

// NewClientAuthorization copies a provider authorization token into an immutable
// action. Invalid protocol or empty/control-bearing token fails Err and encoding.
// No SDK call, confirmation, or funds movement occurs.
//
// Example:
//
//	action := payment.NewClientAuthorization("stripe.payment_intent", "client-secret")
//	if err := action.Err(); err != nil { return err }
func NewClientAuthorization(protocol ClientProtocol, token string) ClientAction {
	return ClientAction{payload: clientActionPayload{
		Kind: ClientActionKindAuthorization, Protocol: protocol, Token: token,
	}}
}

// NewClientOrder copies an opaque client order identity into an immutable action.
// Err rejects invalid protocol or identity syntax; it never treats the order as
// a captured payment or queries the provider.
//
// Example:
//
//	action := payment.NewClientOrder("paypal.order", "ORDER-1")
//	if err := action.Err(); err != nil { return err }
func NewClientOrder(protocol ClientProtocol, orderID string) ClientAction {
	return ClientAction{payload: clientActionPayload{
		Kind: ClientActionKindOrder, Protocol: protocol, OrderID: orderID,
	}}
}

// NewClientSession copies an opaque session identity and SDK data string.
// Financial references need not exist. Err rejects invalid identity/protocol or
// empty/control-bearing data; no arbitrary JSON interpretation occurs.
//
// Example:
//
//	action := payment.NewClientSession("vendor.session", "session-1", "opaque-sdk-data")
//	if err := action.Err(); err != nil { return err }
func NewClientSession(protocol ClientProtocol, sessionID SessionID, data string) ClientAction {
	return ClientAction{payload: clientActionPayload{
		Kind: ClientActionKindSession, Protocol: protocol, SessionID: sessionID, Data: data,
	}}
}

// Kind returns the discriminant without inferring settlement or initialization
// success. Use Err before consuming values obtained outside trusted code.
//
// Example:
//
//	_ = action.Kind()
func (clientAction ClientAction) Kind() ClientActionKind { return clientAction.payload.Kind }

// Protocol returns the SDK handoff protocol verbatim. Unknown valid identifiers
// are allowed for third-party adapters, not silently normalized or registered.
//
// Example:
//
//	_ = action.Protocol()
func (clientAction ClientAction) Protocol() ClientProtocol { return clientAction.payload.Protocol }

// Authorization returns a detached token record only for the authorization
// variant; a different kind returns zero and false. Handle Token as a credential.
//
// Example:
//
//	if authorization, ok := action.Authorization(); ok { _ = authorization.Token }
func (clientAction ClientAction) Authorization() (ClientAuthorization, bool) {
	if clientAction.Kind() != ClientActionKindAuthorization {
		return ClientAuthorization{}, false
	}

	return ClientAuthorization{Protocol: clientAction.Protocol(), Token: clientAction.payload.Token}, true
}

// Order returns the client-order record only for the order variant. A different
// kind returns zero and false; OrderID is never promoted to financial identity.
//
// Example:
//
//	if order, ok := action.Order(); ok { _ = order.OrderID }
func (clientAction ClientAction) Order() (ClientOrder, bool) {
	if clientAction.Kind() != ClientActionKindOrder {
		return ClientOrder{}, false
	}

	return ClientOrder{Protocol: clientAction.Protocol(), OrderID: clientAction.payload.OrderID}, true
}

// Session returns a copied session record only for the session variant. A
// different kind returns zero and false; no financial reference is fabricated.
//
// Example:
//
//	if session, ok := action.Session(); ok { _ = session.SessionID }
func (clientAction ClientAction) Session() (ClientSession, bool) {
	if clientAction.Kind() != ClientActionKindSession {
		return ClientSession{}, false
	}

	return ClientSession{
		Protocol: clientAction.Protocol(), SessionID: clientAction.payload.SessionID, Data: clientAction.payload.Data,
	}, true
}

// protocolPattern accepts lowercase protocol identifiers without a central
// registry, while excluding whitespace, separators, and malformed empty segments.
var protocolPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)*$`)

// Err validates the discriminant, protocol syntax, and required variant fields.
// Constructors and decoding enforce exclusivity; diagnostics wrap
// ErrInvalidRequest without exposing handoff values.
//
// Example:
//
//	if err := action.Err(); err != nil { return err }
func (clientAction ClientAction) Err() error {
	if len(clientAction.Protocol()) > 255 || !protocolPattern.MatchString(string(clientAction.Protocol())) {
		return fmt.Errorf("%w: client protocol", ErrInvalidRequest)
	}

	return clientAction.payload.validateVariant()
}

// validateVariant checks the required data for the selected concrete variant.
// Constructors and the strict decode boundary guarantee inactive fields are zero.
func (clientActionPayload clientActionPayload) validateVariant() error {
	switch clientActionPayload.Kind {
	case ClientActionKindAuthorization:
		return checkHandoffData(clientActionPayload.Token)
	case ClientActionKindOrder:
		return checkIdentity(clientActionPayload.OrderID, "client order")
	case ClientActionKindSession:
		return firstError(checkIdentity(string(clientActionPayload.SessionID), "client session"),
			checkHandoffData(clientActionPayload.Data))
	default:
		return fmt.Errorf("%w: client action kind", ErrInvalidRequest)
	}
}

// checkHandoffData requires a nonempty, control-free opaque SDK string. It uses
// byte length as the rune ceiling so there is no arbitrary provider-token limit.
func checkHandoffData(value string) error {
	if value == "" {
		return fmt.Errorf("%w: empty client handoff", ErrInvalidRequest)
	}

	return checkText(value, "client handoff", len(value))
}

// MarshalJSON intentionally exposes a valid tagged action to an authorized
// client. Do not use JSON serialization for diagnostics or telemetry.
//
// Example:
//
//	data, err := json.Marshal(action)
//	if err != nil { return err }
//	_ = data // authorized client delivery only
func (clientAction ClientAction) MarshalJSON() ([]byte, error) {
	if err := clientAction.Err(); err != nil {
		return nil, err
	}

	return json.Marshal(clientAction.payload)
}

// UnmarshalJSON rejects unknown members, tags, and mixed variants before replacing
// the receiver. A failure leaves any existing handoff value unchanged.
//
// Example:
//
//	if err := json.Unmarshal(data, &action); err != nil { return err }
func (clientAction *ClientAction) UnmarshalJSON(data []byte) error {
	// Decode strings once at the boundary so even empty or null fields belonging
	// to another variant are rejected. No map is retained by the action value.
	var fields map[string]string

	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	next, err := decodeClientAction(fields)

	if err != nil {
		return err
	}

	*clientAction = next

	return nil
}

// decodeClientAction requires exactly the selected variant's named string fields.
// Required-value validation rejects missing/replaced members, while exact member
// count rejects additional members even when their values are empty or null.
// The temporary string map is never part of the published concrete union.
func decodeClientAction(fields map[string]string) (ClientAction, error) {
	protocol := ClientProtocol(fields["protocol"])
	var action ClientAction
	var members int

	switch ClientActionKind(fields["kind"]) {
	case ClientActionKindAuthorization:
		action = NewClientAuthorization(protocol, fields["token"])
		members = 3
	case ClientActionKindOrder:
		action = NewClientOrder(protocol, fields["order_id"])
		members = 3
	case ClientActionKindSession:
		action = NewClientSession(protocol, SessionID(fields["session_id"]), fields["data"])
		members = 4
	default:
		return ClientAction{}, fmt.Errorf("%w: client action kind", ErrInvalidRequest)
	}

	if len(fields) != members {
		return ClientAction{}, fmt.Errorf("%w: client action members", ErrInvalidRequest)
	}

	return action, action.Err()
}

// String redacts every handoff field in ordinary fmt diagnostics. JSON remains
// intentionally credential-bearing and must not be used as a logging substitute.
//
// Example:
//
//	_ = fmt.Sprint(action) // ClientAction{REDACTED}
func (clientAction ClientAction) String() string { return "ClientAction{REDACTED}" }

// GoString redacts handoff fields under fmt's Go-syntax formatting as well.
// It never prints protocol, identifiers, token, or session data.
//
// Example:
//
//	_ = fmt.Sprintf("%#v", action)
func (clientAction ClientAction) GoString() string { return clientAction.String() }

// LogValue supplies a redacted slog value, independent of the handler format.
// It must not be replaced by JSON marshaling in a diagnostic handler.
//
// Example:
//
//	slog.Info("prepared", "action", action)
func (clientAction ClientAction) LogValue() slog.Value {
	return slog.StringValue(clientAction.String())
}

// String redacts the authorization record, including its token and protocol.
// Use typed fields only when intentionally handing data to an authorized client.
//
// Example:
//
//	_ = fmt.Sprint(authorization)
func (clientAuthorization ClientAuthorization) String() string {
	return "ClientAuthorization{REDACTED}"
}

// GoString also redacts Go-syntax authorization formatting. It does not expose
// secrets even when the record is nested in another diagnostic value.
//
// Example:
//
//	_ = fmt.Sprintf("%#v", authorization)
func (clientAuthorization ClientAuthorization) GoString() string { return clientAuthorization.String() }

// LogValue redacts authorization fields for structured logging. JSON is a
// separate, intentional credential-bearing client transport.
//
// Example:
//
//	slog.Info("handoff", "authorization", authorization)
func (clientAuthorization ClientAuthorization) LogValue() slog.Value {
	return slog.StringValue(clientAuthorization.String())
}

// String redacts the order identity and protocol in ordinary diagnostics.
// The order remains available through its fields for authorized SDK handoff.
//
// Example:
//
//	_ = fmt.Sprint(order)
func (clientOrder ClientOrder) String() string { return "ClientOrder{REDACTED}" }

// GoString redacts Go-syntax order formatting as well. It must not reveal an
// order identity merely because diagnostics request expanded formatting.
//
// Example:
//
//	_ = fmt.Sprintf("%#v", order)
func (clientOrder ClientOrder) GoString() string { return clientOrder.String() }

// LogValue returns a redacted structured order value. It intentionally does not
// reuse JSON, which is reserved for authorized client delivery.
//
// Example:
//
//	slog.Info("handoff", "order", order)
func (clientOrder ClientOrder) LogValue() slog.Value { return slog.StringValue(clientOrder.String()) }

// String redacts opaque session data and identity in ordinary fmt diagnostics.
// Access fields explicitly only for authorized client handoff.
//
// Example:
//
//	_ = fmt.Sprint(session)
func (clientSession ClientSession) String() string { return "ClientSession{REDACTED}" }

// GoString redacts Go-syntax session formatting. Session data must never become
// visible through expanded diagnostic formatting.
//
// Example:
//
//	_ = fmt.Sprintf("%#v", session)
func (clientSession ClientSession) GoString() string { return clientSession.String() }

// LogValue supplies only a redacted session string to structured handlers.
// Authorized JSON serialization remains separate and intentionally exposes Data.
//
// Example:
//
//	slog.Info("handoff", "session", session)
func (clientSession ClientSession) LogValue() slog.Value {
	return slog.StringValue(clientSession.String())
}

// SessionID identifies a client SDK session, not a financial payment or hosted
// checkout. Preserve its opaque provider identity; it may exist without either.
//
// Example:
//
//	id := payment.SessionID("session-1")
//	if err := id.Validate(ctx); err != nil { return err }
type SessionID string

// Validate checks context before opaque session identity syntax. Empty, overlong,
// control-bearing, whitespace-bearing, and malformed UTF-8 identities are invalid.
//
// Example:
//
//	if err := payment.SessionID("session-1").Validate(ctx); err != nil { return err }
func (sessionID SessionID) Validate(ctx context.Context) error {
	return checkID(ctx, string(sessionID), "session_id")
}

// Preparation is an observed client initialization response, not a settled
// payment or proof of authorization. Action is required; all resource references
// are optional and remain absent when the provider returns only a session/order.
// No status, amount, or financial identity is fabricated. Diagnostics redact the
// whole record; JSON intentionally carries secrets to an authorized client.
//
// Example:
//
//	prepared := payment.Preparation{Action: payment.NewClientSession("vendor.session", "session-1", "sdk-data")}
//	if err := prepared.Err(); err != nil { return err }
type Preparation struct {
	// PaymentID is optional observed financial identity, never a session alias.
	// Example: if prepared.PaymentID != "" { _ = prepared.PaymentID }
	PaymentID ID `json:"payment_id,omitzero"`
	// CheckoutID is optional observed checkout/order identity; preparation does
	// not imply a hosted page exists or that completion is currently required.
	// Example: if prepared.CheckoutID != "" { _ = prepared.CheckoutID }
	CheckoutID CheckoutID `json:"checkout_id,omitzero"`
	// SessionID is optional observed SDK-session identity, not a payment identity.
	// Example: if prepared.SessionID != "" { _ = prepared.SessionID }
	SessionID SessionID `json:"session_id,omitzero"`
	// Action is the required concrete handoff for an authorized client. It never
	// establishes successful payment and must not be logged as JSON.
	// Example: if authorization, ok := prepared.Action.Authorization(); ok { _ = authorization.Token }
	Action ClientAction `json:"action"`
}

// preparationPayload owns the private codec shape without diagnostic methods.
// It is a distinct private type, not a public DTO or a second source of tags.
type preparationPayload Preparation

// Err requires a valid action and validates only references actually present.
// Session-only handoffs are valid; no financial reference is mandatory.
//
// Example:
//
//	if err := prepared.Err(); err != nil { return err }
func (preparation Preparation) Err() error {
	return firstError(preparation.Action.Err(), checkOptionalIdentity(string(preparation.PaymentID), "payment_id"),
		checkOptionalIdentity(string(preparation.CheckoutID), "checkout_id"),
		checkOptionalIdentity(string(preparation.SessionID), "session_id"))
}

// MarshalJSON intentionally exposes valid action secrets to an authorized
// client. Invalid action or reference syntax fails before encoding.
//
// Example:
//
//	data, err := json.Marshal(prepared)
//	if err != nil { return err }
//	_ = data // authorized delivery, not logging
func (preparation Preparation) MarshalJSON() ([]byte, error) {
	if err := preparation.Err(); err != nil {
		return nil, err
	}

	return json.Marshal(preparationPayload(preparation))
}

// UnmarshalJSON validates temporary state before replacing the observation.
// Missing financial references stay absent, and invalid input leaves it intact.
//
// Example:
//
//	if err := json.Unmarshal(data, &prepared); err != nil { return err }
func (preparation *Preparation) UnmarshalJSON(data []byte) error {
	var payload preparationPayload

	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	next := Preparation(payload)

	if err := next.Err(); err != nil {
		return err
	}

	*preparation = next

	return nil
}

// String redacts the complete observation, including optional references.
// Client transport JSON is intentionally separate from diagnostic formatting.
//
// Example:
//
//	_ = fmt.Sprint(prepared)
func (preparation Preparation) String() string { return "Preparation{REDACTED}" }

// GoString redacts the observation under expanded Go-syntax formatting too.
// It never exposes an action token or SDK session data.
//
// Example:
//
//	_ = fmt.Sprintf("%#v", prepared)
func (preparation Preparation) GoString() string { return preparation.String() }

// LogValue supplies a redacted slog value independently of handler encoding.
// Do not replace it with credential-bearing JSON in diagnostic handlers.
//
// Example:
//
//	slog.Info("prepared", "result", prepared)
func (preparation Preparation) LogValue() slog.Value { return slog.StringValue(preparation.String()) }
