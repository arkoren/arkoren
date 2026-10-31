package vaultsecret

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"
)

// errNumericPrecision rejects values Vault's native float64 JSON boundary can
// change. It contains no member names or numeric contents, so validation does not
// introduce secret payloads into returned diagnostic text.
var errNumericPrecision = errors.New("vault JSON number changes under native float64 encoding; encode exact values as JSON strings")

// decodeVaultData accepts a non-null object whose numbers survive native JSON
// serialization without a value change. It retains the original member tokens,
// not their float64 approximations, and never changes strings into numbers or
// numbers into strings. Native clients therefore keep the ordinary KV v2 shape.
func decodeVaultData(value []byte) (map[string]any, error) {
	var object map[string]jsontext.Value

	if err := json.Unmarshal(value, &object); err != nil {
		return nil, err
	}

	if object == nil {
		return nil, errors.New("vault secret payload must be a JSON object")
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

// validateVaultNumbers visits every numeric token, including nested objects and
// arrays, without decoding them into a generic floating-point map. String values
// are left alone. Malformed JSON errors propagate, although decodeVaultData has
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
