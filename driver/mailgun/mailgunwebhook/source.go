package mailgunwebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"arkoren.dev/core/http/webhook"
)

// Envelope retains event-data after verifying the signature token. It excludes
// signing material, but contains personal data; the signature does not cover it.
// Additional native events can use Data with webhook.NewEvent.
//
// Example:
//
//	custom := webhook.NewEvent("custom", func(envelope mailgunwebhook.Envelope) (string, error) {
//		return string(envelope.Data), nil
//	})
//	_ = custom
type Envelope struct {
	// Data is the owned JSON event-data object, without the signature wrapper.
	// It is not authenticated by Mailgun's HMAC. Do not log it indiscriminately.
	// Example: _ = string(mailgunwebhook.Envelope{}.Data)
	Data jsontext.Value `json:"event-data"`
}

// Builder sets local signature freshness policy. New snapshots its duration;
// retaining this draft cannot change an already published source.
//
// Example:
//
//	_, err := mailgunwebhook.New(signingKey, func(signature *mailgunwebhook.Builder) {
//		signature.Tolerance(time.Hour)
//	})
//	if err != nil {
//		return err
//	}
type Builder struct {
	// tolerance bounds both old and future signature timestamps, not event age.
	tolerance time.Duration
}

// BuilderFunc configures one draft synchronously in order. Nil callbacks panic;
// omitting callbacks retains the thirty-minute local freshness policy.
//
// Example:
//
//	var policy mailgunwebhook.BuilderFunc = func(signature *mailgunwebhook.Builder) {
//		signature.Tolerance(time.Hour)
//	}
//	_, err := mailgunwebhook.New(signingKey, policy)
//	if err != nil {
//		return err
//	}
type BuilderFunc = func(*Builder)

// Tolerance bounds timestamp distance from receipt time in both directions.
// Zero restores thirty minutes; negative values make New fail. Delayed provider
// deliveries outside the window are rejected; increase it if needed for retries.
//
// Example:
//
//	policy := func(signature *mailgunwebhook.Builder) {
//		signature.Tolerance(time.Hour)
//	}
//	_ = policy
func (signature *Builder) Tolerance(tolerance time.Duration) *Builder {
	signature.tolerance = tolerance

	return signature
}

// ErrInvalidSignature marks malformed signature material, mismatched HMACs and
// timestamps outside local policy. Source errors also wrap webhook.ErrRejected
// and never expose signing material or body content.
//
// Example:
//
//	if errors.Is(err, mailgunwebhook.ErrInvalidSignature) {
//		// Reject the signature; do not process the event.
//	}
var ErrInvalidSignature = errors.New("mailgun webhook: invalid signature")

// errEnvelope marks invalid event structure after token verification. Core
// returns 503 rather than misclassifying a schema failure as a bad signature.
var errEnvelope = errors.New("mailgun webhook: invalid event envelope")

// defaultTolerance allows processing delays while bounding captured-token age.
// This is a local policy, not a Mailgun-specified expiration period.
const defaultTolerance = 30 * time.Minute

// signatureFields is parsed before authentication because Mailgun places its
// HMAC inputs inside JSON. Parent signatures are intentionally not substituted.
type signatureFields struct {
	// Timestamp is signed as its exact original decimal string.
	Timestamp string `json:"timestamp"`
	// Token is Mailgun's random fifty-character per-request nonce.
	Token string `json:"token"`
	// Signature is the hexadecimal SHA256 HMAC of Timestamp + Token.
	Signature string `json:"signature"`
}

// New validates the account HTTP webhook signing key and creates a source with
// no I/O or SDK client. It verifies only timestamp + token, not event-data. Use
// webhook.Handler for routing, body limits, application bindings and deduplication.
// There is no token cache; repeated valid tokens inside the window can verify.
// Empty keys, whitespace, and control characters return webhook.ErrInvalid;
// keys are never trimmed or repaired, and errors do not contain their bytes.
//
// Example:
//
//	source, err := mailgunwebhook.New(signingKey)
//	if err != nil {
//		return err
//	}
//	_ = source.Err()
func New(signingKey string, configure ...BuilderFunc) (webhook.Source[Envelope], error) {
	if signingKey == "" || strings.ContainsFunc(signingKey, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: mailgun signing key", webhook.ErrInvalid)
	}

	signature := &Builder{}

	for _, apply := range configure {
		apply(signature)
	}

	if signature.tolerance < 0 {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: mailgun signature tolerance", webhook.ErrInvalid)
	}

	if signature.tolerance == 0 {
		signature.tolerance = defaultTolerance
	}

	tolerance := signature.tolerance

	return webhook.NewSource(func(_ *http.Request, body []byte) (webhook.Delivery[Envelope], error) {
		return verify(body, signingKey, tolerance)
	}), nil
}

// verify parses only the signature wrapper before checking the token, then
// selects the native event identity. Invalid JSON cannot supply verifiable
// signature material and is rejected; validly wrapped invalid event-data is 503.
func verify(body []byte, signingKey string, tolerance time.Duration) (webhook.Delivery[Envelope], error) {
	var payload struct {
		// Signature carries the untrusted timestamp, nonce, and HMAC wrapper.
		Signature signatureFields `json:"signature"`
		// Data retains event data for routing only after token verification.
		Data jsontext.Value `json:"event-data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return webhook.Delivery[Envelope]{}, fmt.Errorf("%w: %w", webhook.ErrRejected, ErrInvalidSignature)
	}

	if !validSignature(payload.Signature, signingKey, tolerance) {
		return webhook.Delivery[Envelope]{}, fmt.Errorf("%w: %w", webhook.ErrRejected, ErrInvalidSignature)
	}

	return delivery(payload.Data)
}

// validSignature uses constant-time HMAC comparison and bounds old and future
// timestamps without overflowing duration arithmetic for extreme Unix values.
func validSignature(signature signatureFields, signingKey string, tolerance time.Duration) bool {
	if len(signature.Token) != 50 {
		return false
	}

	digest, err := hex.DecodeString(signature.Signature)

	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(signature.Timestamp + signature.Token))

	if !hmac.Equal(digest, mac.Sum(nil)) {
		return false
	}

	seconds, err := strconv.ParseInt(signature.Timestamp, 10, 64)

	if err != nil {
		return false
	}

	issuedAt := time.Unix(seconds, 0)
	now := time.Now()

	return !issuedAt.Before(now.Add(-tolerance)) && !issuedAt.After(now.Add(tolerance))
}

// delivery validates the three mandatory Mailgun event fields, leaving optional
// native fields for the bound decoder. IDs are only unique within a UTC day,
// so the date of the event (not signature or receipt) qualifies the identity.
func delivery(data jsontext.Value) (webhook.Delivery[Envelope], error) {
	var metadata struct {
		// ID is provider-assigned and only unique within one UTC day.
		ID string `json:"id"`
		// Event is the native event binding key, including unknown future keys.
		Event string `json:"event"`
		// Timestamp is the event time in Unix seconds, including a fraction.
		Timestamp float64 `json:"timestamp"`
	}

	if err := json.Unmarshal(data, &metadata); err != nil {
		return webhook.Delivery[Envelope]{}, errEnvelope
	}

	if metadata.ID == "" || metadata.Event == "" {
		return webhook.Delivery[Envelope]{}, errEnvelope
	}

	// Range-check before converting seconds to int64; realistic event times lie
	// within Go's supported four-digit calendar years, keeping identity stable.
	if metadata.Timestamp <= 0 || metadata.Timestamp >= 253402300800 {
		return webhook.Delivery[Envelope]{}, errEnvelope
	}

	day := time.Unix(int64(metadata.Timestamp), 0).UTC().Format("2006-01-02")

	return webhook.Delivery[Envelope]{
		ID:      day + ":" + metadata.ID,
		Type:    metadata.Event,
		Payload: Envelope{Data: data},
	}, nil
}
