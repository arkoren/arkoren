package push

import (
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Recipient is a sealed push destination: use a Subscription or Token value.
// Pointers, including typed nil pointers, and embedded foreign variants are
// rejected by NewMessage. Value forms keep published messages independent of
// caller mutation. Recipients contain secrets and must not enter telemetry.
//
// Example:
//
//	var recipient push.Recipient = push.Token("device-registration")
//	message := push.NewMessage(recipient, "Ready")
//	if err := message.Err(); err != nil { return err }
type Recipient interface {
	// recipient marks the canonical destination variants. Construction also
	// checks concrete value types so embedded interfaces cannot bypass policy.
	recipient()
}

// Subscription is a browser's Web Push registration, passed by value.
// Validation requires HTTPS and well-formed encryption keys, not a reachable
// endpoint. Drivers own request security and supported endpoint policy.
//
// Example:
//
//	registration := push.Subscription{Endpoint: endpoint, Keys: keys}
//	message := push.NewMessage(registration, "Ready")
//	if err := message.Err(); err != nil { return err }
type Subscription struct {
	// Endpoint is the sensitive absolute HTTPS push-service URL. Credentials
	// and fragments, including an empty trailing #, are rejected. Encoded %23
	// in a path or query is data; paths and query strings are retained exactly.
	//
	// Example:
	//
	//	registration := push.Subscription{Endpoint: "https://push.example/send/123", Keys: keys}
	//	_ = registration
	Endpoint string `json:"endpoint"`

	// Keys holds the browser-generated encryption material, not VAPID keys.
	// These strings must not be logged or exposed through error messages.
	//
	// Example:
	//
	//	registration := push.Subscription{Endpoint: endpoint, Keys: keys}
	//	_ = registration
	Keys SubscriptionKeys `json:"keys"`
}

// SubscriptionKeys contains unpadded, URL-safe base64 encryption material.
// P256DH is the browser's public key; Auth is a secret authentication value.
// Neither field is a sender's VAPID signing key.
//
// Example:
//
//	keys := push.SubscriptionKeys{P256DH: browserPublicKey, Auth: browserAuthSecret}
//	message := push.NewMessage(push.Subscription{Endpoint: endpoint, Keys: keys}, "Ready")
//	if err := message.Err(); err != nil { return err }
type SubscriptionKeys struct {
	// P256DH encodes a valid 65-byte uncompressed P-256 public point.
	// Compressed points, padding, and noncanonical base64 are invalid.
	//
	// Example:
	//
	//	keys := push.SubscriptionKeys{P256DH: browserPublicKey, Auth: browserAuthSecret}
	//	_ = keys
	P256DH string `json:"p256dh"`

	// Auth encodes exactly 16 bytes of authentication secret without padding.
	// Treat it as a credential even though it accompanies a public key.
	//
	// Example:
	//
	//	keys := push.SubscriptionKeys{P256DH: browserPublicKey, Auth: browserAuthSecret}
	//	_ = keys
	Auth string `json:"auth"`
}

// Token is a nonempty, valid UTF-8 native device registration token.
// Its opaque value is interpreted by the configured Driver, not by a global
// provider registry. Do not log it or use it as a telemetry label.
//
// Example:
//
//	message := push.NewMessage(push.Token("device-registration"), "Ready")
//	if err := message.Err(); err != nil { return err }
type Token string

// recipient seals the value variant; actual validation occurs at construction.
func (Subscription) recipient() {
}

// recipient seals the opaque native variant without adding provider routing.
func (Token) recipient() {
}

// checkRecipient accepts only canonical value forms. It never calls methods on
// unknown variants, so nil pointers and embedded implementations fail safely.
func checkRecipient(to Recipient) error {
	switch to := to.(type) {
	case Subscription:
		return checkSubscription(to)
	case Token:
		if to == "" || !utf8.ValidString(string(to)) {
			return fmt.Errorf("%w: invalid native token", ErrInvalidMessage)
		}

		return nil
	default:
		return fmt.Errorf("%w: recipient must be a subscription or token value", ErrInvalidMessage)
	}
}

// checkSubscription validates browser wire structure only. Parse errors are
// intentionally not wrapped, because they can include the secret endpoint.
func checkSubscription(registration Subscription) error {
	endpoint, err := url.Parse(registration.Endpoint)

	if err != nil {
		return fmt.Errorf("%w: invalid subscription endpoint", ErrInvalidMessage)
	}

	if endpoint.Scheme != "https" || endpoint.Hostname() == "" {
		return fmt.Errorf("%w: subscription requires an absolute HTTPS endpoint", ErrInvalidMessage)
	}

	// Parsing discards an empty fragment, so inspect the original delimiter.
	if endpoint.User != nil || strings.Contains(registration.Endpoint, "#") {
		return fmt.Errorf("%w: subscription endpoint contains credentials or fragment", ErrInvalidMessage)
	}

	return checkKeys(registration.Keys)
}

// checkKeys verifies the P-256 point and auth length without network access.
// All errors name only the field; decoder or curve errors never expose keys.
func checkKeys(keys SubscriptionKeys) error {
	point, err := decodeKey(keys.P256DH)

	if err != nil {
		return fmt.Errorf("%w: invalid subscription public key", ErrInvalidMessage)
	}

	if _, err := ecdh.P256().NewPublicKey(point); err != nil {
		return fmt.Errorf("%w: invalid subscription public key", ErrInvalidMessage)
	}

	auth, err := decodeKey(keys.Auth)

	if err != nil || len(auth) != 16 {
		return fmt.Errorf("%w: invalid subscription auth secret", ErrInvalidMessage)
	}

	return nil
}

// decodeKey requires the raw URL-safe alphabet and canonical trailing bits.
// The standard decoder ignores line breaks, so those are rejected separately.
func decodeKey(value string) ([]byte, error) {
	if strings.ContainsAny(value, "\r\n") {
		return nil, ErrInvalidMessage
	}

	return base64.RawURLEncoding.Strict().DecodeString(value)
}
