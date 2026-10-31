// Package envelope owns workflow payload framing and failure wire records.
// Error identities and the public codec API remain in workflow.
package envelope

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

// Codec supplies the encoding policy chosen by the public adapter.
type Codec interface {
	// Marshal encodes the inner value as JSON.
	Marshal(any) ([]byte, error)
	// Unmarshal decodes the inner value into a destination pointer.
	Unmarshal([]byte, any) error
}

// valueEnvelope distinguishes an absent member from a present JSON null.
type valueEnvelope struct {
	// V retains the original encoded inner value.
	V jsontext.Value `json:"v"`
}

const (
	// prefix opens the single-member fast-path value envelope.
	prefix = `{"v":`
	// suffix closes the encoded value envelope.
	suffix = `}`
)

// Encode frames codec output without validating it a second time.
// Empty output becomes null; returned bytes do not retain the codec buffer.
func Encode(codec Codec, value any) ([]byte, error) {
	inner, err := codec.Marshal(value)

	if err != nil {
		return nil, fmt.Errorf("encode value: %w", err)
	}

	if len(inner) == 0 {
		inner = []byte("null")
	}

	out := make([]byte, 0, len(prefix)+len(inner)+len(suffix))
	out = append(out, prefix...)
	out = append(out, inner...)

	return append(out, suffix...), nil
}

// Decode opens a payload envelope before invoking the selected codec.
// Empty data leaves the destination unchanged.
func Decode(codec Codec, data []byte, value any) error {
	if len(data) == 0 {
		return nil
	}

	inner, err := open(data)

	if err != nil {
		return err
	}

	if err := codec.Unmarshal(inner, value); err != nil {
		return fmt.Errorf("decode value: %w", err)
	}

	return nil
}

// open preserves the fast path and lets the codec report malformed inner JSON.
// Other objects use JSON-v2 to preserve the distinction between null and absence.
func open(data []byte) ([]byte, error) {
	if len(data) > len(prefix)+len(suffix) &&
		string(data[:len(prefix)]) == prefix && data[len(data)-1] == '}' {
		return data[len(prefix) : len(data)-1], nil
	}

	var envelope valueEnvelope

	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode value envelope: %w", err)
	}

	if len(envelope.V) == 0 {
		return nil, fmt.Errorf("decode value envelope: missing \"v\" member")
	}

	return []byte(envelope.V), nil
}
