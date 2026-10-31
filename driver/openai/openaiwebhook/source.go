package openaiwebhook

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/webhooks"

	"arkoren.dev/core/http/webhook"
)

// Envelope retains an authenticated event and its raw data for binding-specific
// decoding. Unknown data shapes do not prevent acknowledging unbound events.
// Treat retained data as sensitive; it is not a generated response snapshot.
//
// Example:
//
//	definition := webhook.NewEvent("custom.event", func(envelope openaiwebhook.Envelope) (string, error) {
//		return envelope.ID, nil
//	})
//	_ = definition
type Envelope struct {
	// ID is the JSON event identity, distinct from Delivery.ID's webhook-id.
	// Example: _ = openaiwebhook.Envelope{ID: "evt_example"}
	ID string `json:"id"`
	// Type selects the native event binding without rewriting the provider key.
	// Example: _ = openaiwebhook.Envelope{Type: "response.completed"}
	Type string `json:"type"`
	// CreatedAt is the event time in Unix seconds, not the signature timestamp.
	// Example: _ = openaiwebhook.Envelope{CreatedAt: 1719168000}
	CreatedAt int64 `json:"created_at"`
	// Object is "event" when supplied; OpenAI also permits its omission.
	// Example: _ = openaiwebhook.Envelope{Object: "event"}
	Object string `json:"object,omitzero"`
	// Data owns the raw event-specific JSON, decoded only by a bound definition.
	// Example: _ = string(openaiwebhook.Envelope{}.Data)
	Data jsontext.Value `json:"data"`
}

// ErrInvalidSignature marks missing, stale, future or incorrect signatures.
// Source errors also wrap webhook.ErrRejected and never include credentials,
// header values or payload bytes. The SDK has no signature-error sentinel.
//
// Example:
//
//	if errors.Is(err, openaiwebhook.ErrInvalidSignature) {
//		// Do not process this unauthenticated notification.
//	}
var ErrInvalidSignature = errors.New("openai webhook: invalid signature")

// errEnvelope rejects malformed authenticated events without exposing their data.
var errEnvelope = errors.New("openai webhook: invalid event envelope")

// minimumSigningKeyBytes matches the SDK's Standard Webhooks symmetric-key
// minimum. Short prefixed keys fail at construction, not on the first delivery.
const minimumSigningKeyBytes = 24

// New validates a dashboard endpoint secret and returns an independent source.
// Secrets must use canonical whsec_-prefixed base64 with at least 24 decoded
// bytes, matching the official SDK's prefixed-secret policy. Invalid secrets
// wrap webhook.ErrInvalid. There is no API key, network I/O or lifecycle hook.
// The SDK authenticates exact body bytes before strict json/v2 envelope parsing.
//
// Example:
//
//	source, err := openaiwebhook.New(secret)
//	if err != nil {
//		return err
//	}
//	_ = source.Err()
func New(secret string) (webhook.Source[Envelope], error) {
	encoded, prefixed := strings.CutPrefix(secret, "whsec_")
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)

	if !prefixed || err != nil || len(key) < minimumSigningKeyBytes {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: openai webhook signing secret", webhook.ErrInvalid)
	}

	if base64.StdEncoding.EncodeToString(key) != encoded {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: openai webhook signing secret", webhook.ErrInvalid)
	}

	verifier := webhooks.NewWebhookService(option.WithWebhookSecret(secret))
	verify := func(request *http.Request, body []byte) (webhook.Delivery[Envelope], error) {
		if err := verifier.VerifySignature(body, request.Header); err != nil {
			return webhook.Delivery[Envelope]{}, fmt.Errorf("%w: %w", webhook.ErrRejected, ErrInvalidSignature)
		}

		envelope, err := parseEnvelope(body)

		if err != nil {
			return webhook.Delivery[Envelope]{}, err
		}

		return webhook.Delivery[Envelope]{ID: request.Header.Get("webhook-id"), Type: envelope.Type, Payload: envelope}, nil
	}

	return webhook.NewSource(verify), nil
}

// parseEnvelope validates common metadata after authentication. Event-specific
// data stays raw so unsupported event types remain acknowledgeable by core.
func parseEnvelope(body []byte) (Envelope, error) {
	var envelope Envelope

	if err := json.Unmarshal(body, &envelope); err != nil {
		return Envelope{}, errEnvelope
	}

	if envelope.ID == "" || envelope.Type == "" {
		return Envelope{}, errEnvelope
	}

	if envelope.Object != "" && envelope.Object != "event" {
		return Envelope{}, errEnvelope
	}

	return envelope, nil
}
