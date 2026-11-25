package vaultsecret

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"arkoren.dev/core/service/secret"
)

// errNumericPrecision rejects values Vault's native float64 JSON boundary can
// change. It contains no member names or numeric contents, so validation does not
// introduce secret payloads into returned diagnostic text.
var errNumericPrecision = errors.New("vault JSON number changes under native float64 encoding; encode exact values as JSON strings")

// textField is the single KV member that carries a text payload. A secret
// whose payload is not a JSON object is stored as {"value": "<text>"} and
// unwrapped again on read; see [encodeVaultData] and [decodeVaultData].
const textField = "value"

// errAmbiguousText rejects the one JSON object that would be indistinguishable
// from a wrapped text payload. It contains no payload content.
var errAmbiguousText = fmt.Errorf("%w: an object with the single string member %q is reserved for text payloads", secret.ErrInvalidSecret, textField)

// encodeVaultData maps a driver payload to native KV v2 data.
//
// A JSON object becomes native KV fields, so native clients keep the ordinary
// KV v2 shape: its numbers must survive native JSON serialization without a
// value change, and the driver retains the original member tokens rather than
// float64 approximations. Any other payload that is valid UTF-8 (including JSON
// that is not an object) is stored as the single member {"value": "<text>"}.
// The literal object {"value": "<string>"} is rejected with
// [secret.ErrInvalidSecret] because it would read back as plain text, not as the
// object that was written. Bytes that are not UTF-8 fail with
// [secret.ErrUnsupported], since KV stores JSON strings only.
func encodeVaultData(value []byte) (map[string]any, error) {
	var object map[string]jsontext.Value

	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		if !utf8.Valid(value) {
			return nil, fmt.Errorf("%w: Vault stores UTF-8 text or JSON objects only", secret.ErrUnsupported)
		}

		return map[string]any{textField: string(value)}, nil
	}

	if text, only := object[textField]; only && len(object) == 1 && text.Kind() == '"' {
		return nil, errAmbiguousText
	}

	data := make(map[string]any, len(object))

	for field, raw := range object {
		if err := validateVaultNumbers(raw); err != nil {
			return nil, err
		}

		data[field] = raw
	}

	return data, nil
}

// decodeVaultData returns the payload bytes of a KV v2 response. An object with
// exactly one member, "value", holding a string is a wrapped text payload and
// yields that string's bytes; every other object is returned as compact JSON
// with sorted keys, so object formatting may differ from the write.
func decodeVaultData(data map[string]any) ([]byte, error) {
	if text, ok := data[textField].(string); ok && len(data) == 1 {
		return []byte(text), nil
	}

	return json.Marshal(data)
}

// validateVaultNumbers visits every numeric token, including nested objects and
// arrays, without decoding them into a generic floating-point map. String values
// are left alone. Malformed JSON errors propagate, although encodeVaultData has
// already established member syntax before this numeric policy check.
func validateVaultNumbers(raw jsontext.Value) error {
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))

	for {
		token, err := decoder.ReadToken()

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		if token.Kind() != '0' {
			continue
		}

		if err := validateVaultNumber(token.String()); err != nil {
			return err
		}
	}
}

// validateVaultNumber compares exact decimal values before and after the native
// float64-to-shortest-JSON conversion. Different spellings such as 1e3 and 1000
// are equivalent; rounding, overflow, and underflow are rejected. Zero is handled
// before rational parsing so a huge exponent on zero cannot allocate a huge
// denominator. Input is a syntactically valid JSON number from jsontext.Decoder.
func validateVaultNumber(raw string) error {
	number, err := strconv.ParseFloat(raw, 64)

	if err != nil {
		return errNumericPrecision
	}

	if number == 0 {
		coefficient, _, _ := strings.Cut(raw, "e")
		coefficient, _, _ = strings.Cut(coefficient, "E")

		if strings.ContainsAny(coefficient, "123456789") {
			return errNumericPrecision
		}

		return nil
	}

	original, valid := new(big.Rat).SetString(raw)
	native, converted := new(big.Rat).SetString(strconv.FormatFloat(number, 'g', -1, 64))

	if !valid || !converted {
		return errNumericPrecision
	}

	if original.Cmp(native) != 0 {
		return errNumericPrecision
	}

	return nil
}
