package key

import (
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
)

// ringRedaction is the constant text every formatting path prints for a Ring.
const ringRedaction = "[REDACTED shared key ring]"

// Ring is an immutable, ordered set of keys that supports rotation. The first
// key is current: it encrypts or signs new data. Every later key is previous:
// it is accepted only to decrypt or verify data produced before a rotation.
// Consumers such as link signers, pagination cursors and the AES and ChaCha20
// cipher drivers take a Ring instead of a separate list of previous keys.
//
// The constraint is currently only [Shared]. It is declared as an interface so
// the set can widen to include asymmetric identities later without changing the
// spelling of existing Ring[key.Shared] types. The zero value is invalid: build
// a Ring with [NewRing] or [ParseRing] and check [Ring.Err] at consumer
// boundaries.
//
// Ring redacts like [Shared]: every formatting verb and structured logging
// print a constant marker. [Ring.Encode] is the only way to export the text
// form. A Ring has no MarshalText or MarshalJSON on purpose, so it cannot be
// serialized by accident.
//
// Example:
//
//	current, err := key.GenerateShared()
//	if err != nil {
//		return err
//	}
//	ring, err := key.NewRing(current)
//	if err != nil {
//		return err
//	}
//	_ = ring.Current()
type Ring[K interface{ Shared }] struct {
	// keys holds the current key first, then the previous keys. It is never
	// mutated after construction and never shared with callers.
	keys []K
}

// NewRing builds a Ring whose current key is current and whose previous keys
// are previous, newest first. The slice is copied; later changes to it do not
// affect the ring.
//
// It returns ErrInvalid when any key is the zero value or invalid, or when two
// keys have the same material, because a duplicate makes the rotation order
// ambiguous.
//
// Example:
//
//	ring, err := key.NewRing(newKey, oldKey)
//	if err != nil {
//		return err
//	}
//	_ = ring.Previous()
func NewRing[K interface{ Shared }](current K, previous ...K) (Ring[K], error) {
	keys := append([]K{current}, previous...)

	for index, candidate := range keys {
		if Shared(candidate).Err() != nil {
			return Ring[K]{}, ErrInvalid
		}

		for _, earlier := range keys[:index] {
			if *Shared(candidate).material == *Shared(earlier).material {
				return Ring[K]{}, ErrInvalid
			}
		}
	}

	return Ring[K]{keys: keys}, nil
}

// ParseRing parses the text form of a Ring: one or more padded standard base64
// keys, the encoding [ParseShared] accepts, separated by commas on a single
// line. The first key is current and the rest are previous. A single key, such
// as the output of "arkoren key generate shared" or "openssl rand -base64 32",
// is a valid one-key ring.
//
// Whitespace around the whole text and around each entry is ignored. ParseRing
// returns ErrInvalid for empty entries, duplicate keys, embedded line breaks,
// keys that are not exactly 32 bytes, and input over 64 KiB. Errors never
// contain key material. The input is not retained.
//
// Example:
//
//	ring, err := key.ParseRing[key.Shared](os.Getenv("APP_KEYS"))
//	if err != nil {
//		return err
//	}
//	_ = ring.Current()
func ParseRing[K interface{ Shared }](text string) (Ring[K], error) {
	if len(text) > maxFileSize {
		return Ring[K]{}, ErrInvalid
	}

	text = strings.TrimSpace(text)

	if strings.ContainsAny(text, "\r\n") {
		return Ring[K]{}, ErrInvalid
	}

	var keys []K

	for entry := range strings.SplitSeq(text, ",") {
		parsed, err := ParseShared(entry)

		if err != nil {
			return Ring[K]{}, err
		}

		keys = append(keys, K(parsed))
	}

	return NewRing(keys[0], keys[1:]...)
}

// UnmarshalText parses the text form accepted by [ParseRing] and replaces the
// ring on success. On failure the ring is left unchanged and the error is
// ErrInvalid. It lets text decoders, which look for [encoding.TextUnmarshaler],
// fill a Ring directly.
//
// Example:
//
//	var ring key.Ring[key.Shared]
//	if err := ring.UnmarshalText([]byte(encoded)); err != nil {
//		return err
//	}
func (ring *Ring[K]) UnmarshalText(text []byte) error {
	parsed, err := ParseRing[K](string(text))

	if err != nil {
		return err
	}

	*ring = parsed

	return nil
}

// Err reports whether the ring holds at least one valid key. It returns
// ErrInvalid for the zero value. Consumers call it to reject missing
// configuration instead of generating replacement material.
//
// Example:
//
//	if err := (key.Ring[key.Shared]{}).Err(); errors.Is(err, key.ErrInvalid) {
//		fmt.Println("configure an application key")
//	}
func (ring Ring[K]) Err() error {
	if len(ring.keys) == 0 {
		return ErrInvalid
	}

	return nil
}

// Len returns the number of keys in the ring, current included. The zero value
// has length 0.
//
// Example:
//
//	fmt.Println(ring.Len())
func (ring Ring[K]) Len() int {
	return len(ring.keys)
}

// Current returns the key used for new encryption or signing. The zero value
// returns the zero key, which is invalid.
//
// Example:
//
//	current := ring.Current()
//	_ = current.Bytes()
func (ring Ring[K]) Current() K {
	if len(ring.keys) == 0 {
		var zero K

		return zero
	}

	return ring.keys[0]
}

// Previous returns the keys accepted only for decryption or verification,
// newest first. The result is a detached copy that is empty for a one-key or
// zero ring.
//
// Example:
//
//	for _, old := range ring.Previous() {
//		_ = old.Fingerprint()
//	}
func (ring Ring[K]) Previous() []K {
	if len(ring.keys) == 0 {
		return []K{}
	}

	return slices.Clone(ring.keys[1:])
}

// Encode exports the canonical text form: the padded base64 of each key joined
// by commas, current first. [ParseRing] accepts it back. The zero value exports
// an empty string. The result is secret material: protect it like
// [Shared.Base64] and never log it.
//
// Example:
//
//	return os.WriteFile("app.keys", []byte(ring.Encode()), 0o600)
func (ring Ring[K]) Encode() string {
	encoded := make([]string, len(ring.keys))

	for index, member := range ring.keys {
		encoded[index] = Shared(member).Base64()
	}

	return strings.Join(encoded, ",")
}

// String redacts the ring. Zero and valid rings print the same constant marker.
//
// Example:
//
//	fmt.Println(key.Ring[key.Shared]{}) // [REDACTED shared key ring]
func (ring Ring[K]) String() string {
	return ringRedaction
}

// Format writes the constant redaction marker for every verb, flag, width and
// precision, including %x and %q.
//
// Example:
//
//	fmt.Printf("%x", key.Ring[key.Shared]{}) // [REDACTED shared key ring]
func (ring Ring[K]) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, ringRedaction)
}

// GoString redacts the ring in Go-syntax formatting.
//
// Example:
//
//	fmt.Printf("%#v", key.Ring[key.Shared]{}) // [REDACTED shared key ring]
func (ring Ring[K]) GoString() string {
	return ringRedaction
}

// LogValue redacts the ring in structured logs.
//
// Example:
//
//	slog.Info("configuration", "keys", key.Ring[key.Shared]{})
func (ring Ring[K]) LogValue() slog.Value {
	return slog.StringValue(ringRedaction)
}
