package resendwebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arkoren.dev/core/http/webhook"
)

// Envelope retains an authenticated Resend notification without decoding its
// event-specific data. Custom credential-free definitions can decode Data;
// unbound event types do not need to match an email schema.
//
// Example:
//
//	event := webhook.NewEvent("custom.event", func(envelope resendwebhook.Envelope) (string, error) {
//		return envelope.Type, nil
//	})
//	_ = event
type Envelope struct {
	// Type selects the native event binding after authentication.
	// Example: _ = resendwebhook.Envelope{Type: "email.delivered"}
	Type string `json:"type"`
	// CreatedAt is the provider event time, not the signature attempt time.
	// It remains a string so unknown event schemas remain acknowledgeable.
	// Example: _ = resendwebhook.Envelope{CreatedAt: "2026-02-22T23:41:12.126Z"}
	CreatedAt string `json:"created_at"`
	// Data owns the event-specific JSON. It can contain personal information.
	// Example: _ = string(resendwebhook.Envelope{}.Data)
	Data jsontext.Value `json:"data"`
}

// ErrInvalidSignature identifies missing, stale, future-dated, or incorrect
// Svix authentication. Returned errors also match webhook.ErrRejected and
// never include credentials, payloads, or request header values.
//
// Example:
//
//	if errors.Is(err, resendwebhook.ErrInvalidSignature) {
//		// Do not process the unauthenticated notification.
//	}
var ErrInvalidSignature = errors.New("resend webhook: invalid signature")

// errEnvelope identifies authenticated JSON or schema failures. These are
// processing failures (503), not unauthenticated-request failures (400).
var errEnvelope = errors.New("resend webhook: invalid envelope or email data")

// toleranceSeconds matches the installed Resend SDK's five-minute window.
// Checking against bounds instead of subtracting avoids integer overflow.
const toleranceSeconds int64 = 300

// New validates the endpoint's whsec_ base64 signing secret and returns a
// source that performs no network I/O and needs no API key. Only Svix v1
// HMAC-SHA256 signatures are accepted, over the exact received body. Attempt
// timestamps must be within five minutes of local time in either direction.
// Replays within that window require application-owned deduplication.
// The secret must use unbroken standard base64 after whsec_; empty, malformed,
// or line-broken secrets return webhook.ErrInvalid without exposing their bytes.
//
// Example:
//
//	source, err := resendwebhook.New("whsec_c2lnbmluZy1zZWNyZXQ=")
//	if err != nil {
//		return err
//	}
//	_ = source.Err()
func New(secret string) (webhook.Source[Envelope], error) {
	if strings.ContainsAny(secret, "\r\n") {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: resend signing secret", webhook.ErrInvalid)
	}

	encoded, prefixed := strings.CutPrefix(secret, "whsec_")
	key, err := base64.StdEncoding.DecodeString(encoded)

	if !prefixed || err != nil || len(key) == 0 {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: resend signing secret", webhook.ErrInvalid)
	}

	verify := func(request *http.Request, body []byte) (webhook.Delivery[Envelope], error) {
		if !authenticate(request.Header, body, key) {
			return webhook.Delivery[Envelope]{}, fmt.Errorf("%w: %w", webhook.ErrRejected, ErrInvalidSignature)
		}

		var envelope Envelope

		if err := json.Unmarshal(body, &envelope); err != nil {
			return webhook.Delivery[Envelope]{}, errEnvelope
		}

		if envelope.Type == "" {
			return webhook.Delivery[Envelope]{}, errEnvelope
		}

		return webhook.Delivery[Envelope]{ID: request.Header.Get("svix-id"), Type: envelope.Type, Payload: envelope}, nil
	}

	return webhook.NewSource(verify), nil
}

// authenticate validates unique Svix headers and signed attempt freshness
// before comparing supported signatures in constant time. Multiple signatures
// in one header support endpoint key rotation; duplicate header lines fail closed.
func authenticate(headers http.Header, body, key []byte) bool {
	for _, name := range []string{"svix-id", "svix-timestamp", "svix-signature"} {
		if len(headers.Values(name)) != 1 || headers.Get(name) == "" {
			return false
		}
	}

	timestamp := headers.Get("svix-timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)

	if err != nil {
		return false
	}

	now := time.Now().Unix()

	if seconds < now-toleranceSeconds || seconds > now+toleranceSeconds {
		return false
	}

	digest := hmac.New(sha256.New, key)
	digest.Write([]byte(headers.Get("svix-id") + "." + timestamp + "."))
	digest.Write(body)
	expected := base64.StdEncoding.EncodeToString(digest.Sum(nil))

	return matchesSignature(headers.Get("svix-signature"), expected)
}

// matchesSignature ignores unsupported versions rather than interpreting
// their bytes as v1. Any matching v1 entry permits a rotating-key delivery.
func matchesSignature(header, expected string) bool {
	for signature := range strings.SplitSeq(header, " ") {
		value, supported := strings.CutPrefix(signature, "v1,")

		if supported && hmac.Equal([]byte(value), []byte(expected)) {
			return true
		}
	}

	return false
}
