// Package numbers supplies exact JSON arithmetic shared by validation engines.
// It does not choose traversal, normalization, or resource policies.
package numbers

import (
	"encoding/json/jsontext"
	"errors"
	"math/big"
	"strconv"
)

// Decimal owns one validated schema number and its exact rational value.
// The original spelling preserves export precision; the rational is immutable
// after construction and is never exposed through public diagnostic values.
type Decimal struct {
	// text is the original JSON numeric token, without whitespace.
	text string
	// value caches the exact value used by constraint and equality checks.
	value *big.Rat
}

// MarshalJSON preserves a compiled schema number's original precision without
// converting it through binary64. It returns a new byte slice on every call.
//
// Example:
//
//	schema, err := validate.FromJSONSchema([]byte(`{"const":9007199254740993}`))
//	if err != nil {
//		panic(err)
//	}
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(string(document))
func (number Decimal) MarshalJSON() ([]byte, error) {
	return []byte(number.text), nil
}

// Unmarshal overrides only numeric values decoded into any. Other
// kinds use JSON-v2's normal recursive decoder. Decoder storage is not retained.
// Exponents rejected by math/big are explicit schema errors, never rounded or
// silently treated as nonnumeric values; callers must bound untrusted documents.
func Unmarshal(decoder *jsontext.Decoder, destination *any) error {
	if decoder.PeekKind() != '0' {
		return errors.ErrUnsupported
	}

	raw, err := decoder.ReadValue()

	if err != nil {
		return err
	}

	text := string(raw)
	value, ok := new(big.Rat).SetString(text)

	if !ok {
		return errors.New("JSON number exponent exceeds math/big support")
	}

	*destination = Decimal{text: text, value: value}

	return nil
}

// Exact views supported numeric values without changing them. Go
// floats mean their shortest decimal JSON encoding, not their binary fraction.
// Raw JSON is accepted only when it is one valid numeric token. Returned schema
// rationals are borrowed immutable values: callers must not mutate operands.
func Exact(value any) (*big.Rat, bool) {
	switch number := value.(type) {
	case Decimal:
		return number.value, true
	case int:
		return new(big.Rat).SetInt64(int64(number)), true
	case int64:
		return new(big.Rat).SetInt64(number), true
	case float64:
		return new(big.Rat).SetString(strconv.FormatFloat(number, 'g', -1, 64))
	case jsontext.Value:
		text, ok := Text(number)

		if !ok {
			return nil, false
		}

		return new(big.Rat).SetString(text)
	default:
		return nil, false
	}
}

// Text extracts one valid raw numeric token for arithmetic and
// diagnostic rendering. Other raw JSON kinds and arbitrary application objects
// are not coerced or inspected; false leaves their existing handling unchanged.
func Text(value any) (string, bool) {
	number, ok := value.(jsontext.Value)

	if !ok {
		return "", false
	}

	if number.Kind() != '0' || !number.IsValid() {
		return "", false
	}

	return string(number), true
}

// Public materializes compiled numbers at output boundaries. Values
// whose shortest binary64 JSON spelling is exact retain the historical float64
// representation. Otherwise an owned jsontext.Value retains all numeric digits.
// Runtime-supplied Go values keep their original concrete type.
func Public(value any) any {
	if raw, ok := value.(jsontext.Value); ok {
		return raw.Clone()
	}

	number, ok := value.(Decimal)

	if !ok {
		return value
	}

	rounded, err := strconv.ParseFloat(number.text, 64)

	if err == nil {
		candidate, _ := Exact(rounded)

		if candidate.Cmp(number.value) == 0 {
			return rounded
		}
	}

	return jsontext.Value(number.text)
}

// NewDecimal captures an already checked numeric token and its exact value.
// The caller transfers ownership of value and must not mutate it afterwards.
func NewDecimal(text string, value *big.Rat) Decimal {
	return Decimal{text: text, value: value}
}
