package postmarkwebhook

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"arkoren.dev/core/http/webhook"
)

// Envelope retains an authenticated Postmark notification for a bound decoder.
// Body contains personal data and must not be logged. Unknown fields are retained.
//
// Example: _ = postmarkwebhook.Envelope{RecordType: "Delivery"}
type Envelope struct {
	// RecordType selects the native event binding; inbound email uses "Inbound".
	// Example: _ = postmarkwebhook.Envelope{RecordType: "Open"}
	RecordType string
	// Body owns the original JSON, decoded only when the event is bound.
	// Example: _ = json.Unmarshal(envelope.Body, &notification)
	Body jsontext.Value
}

// errEnvelope avoids exposing JSON contents in processing failures.
var errEnvelope = errors.New("postmark webhook: invalid envelope")

// New authenticates outbound notifications using endpoint-specific Basic Auth.
// It performs no I/O. Configure these credentials on the Postmark webhook URL,
// not with a Postmark API token. HTTPS is mandatory at the public endpoint;
// TLS may terminate at a trusted proxy. Basic Auth neither signs the body nor
// prevents credential-holder replay. Apply IP allowlisting at the trusted edge.
// Missing or incorrect credentials wrap webhook.ErrRejected; malformed
// authenticated JSON produces a retryable processing failure.
// Empty credentials, control characters, or a colon in username return
// webhook.ErrInvalid at construction. Credentials are not trimmed or logged.
//
// Delivery IDs use X-PM-Webhook-Trace-Id, documented as stable across retries.
// Without that header the ID is empty, disabling deduplication rather than
// incorrectly collapsing separate opens/clicks sharing a MessageID.
//
// Example:
//
//	source, err := postmarkwebhook.New("webhooks", "endpoint-secret")
//	if err != nil {
//		return err
//	}
//	_ = source.Err()
func New(username, password string) (webhook.Source[Envelope], error) {
	return newSource(username, password, parseOutbound)
}

// NewInbound authenticates parsed inbound email, whose JSON has no RecordType.
// Use a separate route from New; every accepted envelope binds to Inbound.
// The same HTTPS and Basic Auth limitations as New apply. Delivery IDs use the
// inbound MessageID, which identifies the received email across retries.
// Construction applies the same credential format checks as New.
//
// Example:
//
//	source, err := postmarkwebhook.NewInbound("inbound", "endpoint-secret")
//	if err != nil {
//		return err
//	}
//	_ = source.Err()
func NewInbound(username, password string) (webhook.Source[Envelope], error) {
	return newSource(username, password, parseInbound)
}

// newSource validates endpoint credentials and captures fixed-size hashes so
// comparisons do not reveal matching prefixes or credential lengths.
func newSource(username, password string, parse func([]byte) (webhook.Delivery[Envelope], error)) (webhook.Source[Envelope], error) {
	if username == "" || strings.ContainsAny(username, ":\r\n") || password == "" {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: postmark Basic Auth credentials", webhook.ErrInvalid)
	}

	if strings.ContainsFunc(username+password, unicode.IsControl) {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: postmark Basic Auth credentials", webhook.ErrInvalid)
	}

	wantUsername, wantPassword := sha256.Sum256([]byte(username)), sha256.Sum256([]byte(password))

	return webhook.NewSource(func(request *http.Request, body []byte) (webhook.Delivery[Envelope], error) {
		providedUsername, providedPassword, ok := request.BasicAuth()
		gotUsername, gotPassword := sha256.Sum256([]byte(providedUsername)), sha256.Sum256([]byte(providedPassword))
		matched := subtle.ConstantTimeCompare(gotUsername[:], wantUsername[:]) & subtle.ConstantTimeCompare(gotPassword[:], wantPassword[:])

		if !ok || matched != 1 || len(request.Header.Values("Authorization")) != 1 {
			return webhook.Delivery[Envelope]{}, fmt.Errorf("%w: postmark Basic Auth", webhook.ErrRejected)
		}

		delivery, err := parse(body)

		if delivery.ID == "" {
			delivery.ID = request.Header.Get("X-PM-Webhook-Trace-Id")
		}

		return delivery, err
	}), nil
}

// parseOutbound checks only routing structure, leaving unknown native events
// acknowledgeable and deferring payload schema checks until a binding is used.
func parseOutbound(body []byte) (webhook.Delivery[Envelope], error) {
	var header struct {
		// RecordType is the native binding key, including unknown future types.
		RecordType string `json:"RecordType"`
	}

	if err := json.Unmarshal(body, &header); err != nil {
		return webhook.Delivery[Envelope]{}, errEnvelope
	}

	if header.RecordType == "" {
		return webhook.Delivery[Envelope]{}, errEnvelope
	}

	return webhook.Delivery[Envelope]{Type: header.RecordType, Payload: Envelope{RecordType: header.RecordType, Body: body}}, nil
}

// parseInbound requires a received-message identity without parsing email dates,
// whose syntax is chosen by the sending mail server rather than Postmark.
func parseInbound(body []byte) (webhook.Delivery[Envelope], error) {
	var header struct {
		// MessageID identifies a received inbound message across retries.
		MessageID string `json:"MessageID"`
	}

	if err := json.Unmarshal(body, &header); err != nil {
		return webhook.Delivery[Envelope]{}, errEnvelope
	}

	if header.MessageID == "" {
		return webhook.Delivery[Envelope]{}, errEnvelope
	}

	return webhook.Delivery[Envelope]{ID: header.MessageID, Type: "Inbound", Payload: Envelope{RecordType: "Inbound", Body: body}}, nil
}

// decode parses only bound payloads and returns a sanitized retryable error on
// invalid types. Optional provider fields remain zero when omitted.
func decode[T any](envelope Envelope) (T, error) {
	var payload T

	if err := json.Unmarshal(envelope.Body, &payload); err != nil {
		return payload, errEnvelope
	}

	return payload, nil
}
