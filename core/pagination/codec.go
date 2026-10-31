package pagination

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"arkoren.dev/foundation/key"
)

// MinSecretLength is the required length in bytes of every CursorCodec secret.
// The name is retained for compatibility; key.Shared accepts exactly 32 bytes,
// not arbitrary longer material.
//
// Example:
//
//	raw := []byte("0123456789abcdef0123456789abcdef") // Example material only.
//	if len(raw) != pagination.MinSecretLength {
//		return errors.New("cursor key must contain exactly 32 bytes")
//	}
const MinSecretLength = 32

const (
	// cursorKeyInfo is the HKDF context binding derived keys to signed
	// cursors, so a secret shared with other uses yields unrelated keys.
	cursorKeyInfo = "arkoren.dev/core/pagination/cursor/v1"

	// cursorMACVersion prefixes the MAC input, so a future format cannot be
	// confused with this one.
	cursorMACVersion = "v1."
)

// ErrInvalidSecret reports a zero current/previous key.Shared supplied to
// NewCursorCodec, or Encode on an unconstructed zero codec. The key package
// enforces MinSecretLength before publishing nonzero application material.
//
// Example:
//
//	_, err := pagination.NewCursorCodec(key.Shared{})
//	if errors.Is(err, pagination.ErrInvalidSecret) {
//		fmt.Println("configure a cursor key")
//	}
var ErrInvalidSecret = errors.New("cursor signing secret is invalid")

// CursorCodec encodes and decodes cursors like [CursorEncode] and
// [CursorDecode], with an HMAC-SHA256 signature so a client cannot forge or
// alter a cursor. Without it a client can craft any position; with it only
// tokens signed by one of the configured keys are accepted. Signing does not
// encrypt values or enforce authorization, tenant/filter binding, or expiry;
// applications must check those policies themselves after decoding.
//
// A signed cursor is base64url(payload) + "." + base64url(mac). The MAC covers
// a version prefix and the encoded payload and is checked before the payload
// is parsed. Signed and unsigned cursors are not interchangeable: a codec
// rejects unsigned cursors, and [CursorDecode] rejects signed ones.
//
// A constructed codec is immutable and safe for concurrent use. The zero codec
// is invalid: Encode returns ErrCursorEncode and ErrInvalidSecret, while Decode
// rejects every token with ErrCursorDecode. Obtain one through NewCursorCodec.
//
// Example:
//
//	codec, err := pagination.NewCursorCodec(material)
//	if err != nil {
//		return err
//	}
//	cursor, err := codec.Encode(42)
//	if err != nil {
//		return err
//	}
//	_ = cursor
type CursorCodec struct {
	// keys are the derived MAC keys; keys[0] signs, all of them verify.
	keys [][]byte
}

// NewCursorCodec returns a codec that signs with secret and also accepts
// cursors signed with any previous secret, which allows rotation: deploy the
// new secret first with the old one in previous, and drop the old one once
// the application's retention policy no longer accepts its cursors. The codec
// supplies no built-in expiration time or expiry validation.
//
// Every secret must be a validated key.Shared. Zero current or previous values
// return [ErrInvalidSecret]. Load or parse material through the key package.
// HKDF-SHA256 derives purpose-bound keys without retaining the original secrets.
// Provision separate material for unrelated purposes.
//
// Example:
//
//	material, err := key.LoadShared("cursor.key")
//	if err != nil {
//		return err
//	}
//	codec, err := pagination.NewCursorCodec(material)
//	if err != nil {
//		return err
//	}
//	_ = codec
func NewCursorCodec(secret key.Shared, previous ...key.Shared) (*CursorCodec, error) {
	codec := &CursorCodec{}

	for _, secret := range append([]key.Shared{secret}, previous...) {
		if secret.Err() != nil {
			return nil, ErrInvalidSecret
		}

		key, err := hkdf.Key(sha256.New, secret.Bytes(), nil, cursorKeyInfo, sha256.Size)

		if err != nil {
			return nil, fmt.Errorf("derive cursor signing key: %w", err)
		}

		codec.keys = append(codec.keys, key)
	}

	return codec, nil
}

// cursorMAC returns the signature of the encoded payload under key.
func cursorMAC(key []byte, payload string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(cursorMACVersion + payload))

	return mac.Sum(nil)
}

// Encode serializes value as JSON and returns it as a signed cursor.
// Serialization failures wrap [ErrCursorEncode]. The complete signed token
// must fit Decode's 4096-byte limit; larger values return ErrCursorEncode
// instead of issuing a cursor that Decode cannot accept.
// An unconstructed zero codec returns errors matching both ErrCursorEncode
// and ErrInvalidSecret, without attempting to serialize value.
//
// Example:
//
//	// Fixed example material only; provision a private key for real applications.
//	material, err := key.NewShared([]byte("0123456789abcdef0123456789abcdef"))
//	if err != nil {
//		return err
//	}
//	codec, err := pagination.NewCursorCodec(material)
//	if err != nil {
//		return err
//	}
//	cursor, err := codec.Encode(42)
//	if err != nil {
//		return err
//	}
//	_ = cursor
func (codec *CursorCodec) Encode[T any](value T) (string, error) {
	if len(codec.keys) == 0 {
		return "", errors.Join(ErrCursorEncode, ErrInvalidSecret)
	}

	data, err := json.Marshal(value)

	if err != nil {
		return "", errors.Join(ErrCursorEncode, err)
	}

	// payloadLimit reserves the separator and signature before base64 expansion,
	// avoiding both an oversized allocation and overflow from adding token lengths.
	payloadLimit := maxCursorLength - 1 - base64.RawURLEncoding.EncodedLen(sha256.Size)

	if len(data) > base64.RawURLEncoding.DecodedLen(payloadLimit) {
		return "", ErrCursorEncode
	}

	payload := base64.RawURLEncoding.EncodeToString(data)

	return payload + "." + base64.RawURLEncoding.EncodeToString(cursorMAC(codec.keys[0], payload)), nil
}

// Decode verifies the signature of cursor and, only when it matches one of the
// codec's secrets, unmarshals the payload into a T. An oversized, unsigned,
// malformed, or tampered cursor returns an error matching [ErrCursorDecode]
// without parsing its payload.
//
// Example:
//
//	codec, err := pagination.NewCursorCodec(material)
//	if err != nil {
//		return err
//	}
//	cursor, err := codec.Encode(42)
//	if err != nil {
//		return err
//	}
//	value, err := codec.Decode[int](cursor)
//	if err != nil {
//		return err
//	}
//	_ = value // 42.
func (codec *CursorCodec) Decode[T any](cursor string) (T, error) {
	var value T

	if len(cursor) > maxCursorLength {
		return value, ErrCursorDecode
	}

	payload, signature, ok := strings.Cut(cursor, ".")

	if !ok {
		return value, ErrCursorDecode
	}

	mac, err := base64.RawURLEncoding.Strict().DecodeString(signature)

	if err != nil {
		return value, errors.Join(ErrCursorDecode, err)
	}

	verified := false

	for _, key := range codec.keys {
		if hmac.Equal(mac, cursorMAC(key, payload)) {
			verified = true

			break
		}
	}

	if !verified {
		return value, ErrCursorDecode
	}

	data, err := base64.RawURLEncoding.Strict().DecodeString(payload)

	if err != nil {
		return value, errors.Join(ErrCursorDecode, err)
	}

	if err := json.Unmarshal(data, &value); err != nil {
		return value, errors.Join(ErrCursorDecode, err)
	}

	return value, nil
}
