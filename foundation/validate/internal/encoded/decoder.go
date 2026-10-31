package encoded

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"arkoren.dev/foundation/validate/internal/faults"
	"arkoren.dev/foundation/validate/internal/numbers"
)

// encodedDecoder recursively materializes only JSON types and numbers.Decimal.
// JSON-text's strict defaults reject invalid UTF-8 and duplicate object names.
// All strings and numeric tokens are detached from the decoder's scratch space.
type encodedDecoder struct {
	// tokens supplies strict syntax and duplicate-name checking.
	tokens *jsontext.Decoder
	// budget bounds traversal and observes cancellation between tokens.
	budget encodedBudget
	// numericDigits charges coefficient digits and exponent expansion before
	// allocating each exact rational, including numbers in ignored annotations.
	numericDigits int
}

// decodeEncoded checks the byte bound before parsing and requires EOF after
// one root value. Syntax errors are intentionally redacted at this boundary.
func decodeEncoded(ctx context.Context, document []byte) (any, error) {
	if len(document) > encodedMaxBytes {
		return nil, fmt.Errorf("%w: bytes", faults.ErrEncodedLimit)
	}

	decoder := encodedDecoder{
		tokens: jsontext.NewDecoder(bytes.NewReader(document)),
		budget: encodedBudget{ctx: ctx},
	}

	value, err := decoder.value(0)

	if err != nil {
		return nil, err
	}

	if err := decoder.budget.tick(); err != nil {
		return nil, err
	}

	if _, err := decoder.tokens.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, faults.ErrEncodedJSON
	}

	return value, nil
}

// value consumes one value, checking depth before descending into containers.
// Numbers share the application importer's exact representation, but enforce
// wire-specific allocation bounds before invoking math/big.
func (decoder *encodedDecoder) value(depth int) (any, error) {
	if err := decoder.budget.tick(); err != nil {
		return nil, err
	}

	kind := decoder.tokens.PeekKind()
	container := kind == '{' || kind == '['

	if depth >= encodedMaxDepth && container {
		return nil, fmt.Errorf("%w: JSON depth", faults.ErrEncodedLimit)
	}

	token, err := decoder.tokens.ReadToken()

	if err != nil {
		return nil, faults.ErrEncodedJSON
	}

	if token.Kind() == '0' {
		return decoder.number(token.String())
	}

	switch token.Kind() {
	case '{':
		return decoder.object(depth + 1)
	case '[':
		return decoder.array(depth + 1)
	default:
		return encodedTokenValue(token)
	}
}

// encodedTokenValue detaches one scalar token from decoder scratch storage.
// Delimiters belong to the recursive container reader, never this function.
func encodedTokenValue(token jsontext.Token) (any, error) {
	switch token.Kind() {
	case 'n':
		return nil, nil
	case 't', 'f':
		return token.Bool(), nil
	case '"':
		return token.String(), nil
	default:
		return nil, faults.ErrEncodedJSON
	}
}

// object consumes names and children until the matching delimiter. Names are
// checked as tokens, so duplicates are rejected by the JSON-text decoder.
func (decoder *encodedDecoder) object(depth int) (any, error) {
	object := make(map[string]any)

	for decoder.tokens.PeekKind() != '}' {
		if err := decoder.budget.tick(); err != nil {
			return nil, err
		}

		token, err := decoder.tokens.ReadToken()

		if err != nil {
			return nil, faults.ErrEncodedJSON
		}

		name := token.String()
		value, err := decoder.value(depth)

		if err != nil {
			return nil, err
		}

		object[name] = value
	}

	if _, err := decoder.tokens.ReadToken(); err != nil {
		return nil, faults.ErrEncodedJSON
	}

	return object, nil
}

// array consumes elements without a float64 conversion or retained wire slice.
// The byte, depth, and work limits also bound the materialized value graph.
func (decoder *encodedDecoder) array(depth int) (any, error) {
	array := make([]any, 0)

	for decoder.tokens.PeekKind() != ']' {
		value, err := decoder.value(depth)

		if err != nil {
			return nil, err
		}

		array = append(array, value)
	}

	if _, err := decoder.tokens.ReadToken(); err != nil {
		return nil, faults.ErrEncodedJSON
	}

	return array, nil
}

// number rejects oversized coefficients and exponents before rational
// allocation. Valid JSON grammar has already been checked by tokens.
// Exponent leading zeros do not consume magnitude; coefficient zeros do consume
// the documented digit budget. Even zero with an excessive exponent is refused.
// Cumulative expansion accounting also bounds graphs of many short exponents.
func (decoder *encodedDecoder) number(text string) (numbers.Decimal, error) {
	coefficient, exponent, found := strings.Cut(strings.ToLower(text), "e")
	digits := len(strings.TrimPrefix(coefficient, "-")) - strings.Count(coefficient, ".")

	if digits > encodedMaxDigits {
		return numbers.Decimal{}, fmt.Errorf("%w: numeric digits", faults.ErrEncodedLimit)
	}

	if found {
		magnitude, err := encodedExponent(exponent)

		if err != nil {
			return numbers.Decimal{}, err
		}

		digits += magnitude
	}

	decoder.numericDigits += digits

	if decoder.numericDigits > encodedMaxNumericDigits {
		return numbers.Decimal{}, fmt.Errorf("%w: cumulative numeric digits", faults.ErrEncodedLimit)
	}

	value, ok := new(big.Rat).SetString(text)

	if !ok {
		return numbers.Decimal{}, faults.ErrEncodedJSON
	}

	return numbers.NewDecimal(text, value), nil
}

// encodedExponent bounds and returns magnitude without asking math/big to
// expand a power of ten. Trimming sign and leading zeros prevents overflow.
// Returning the magnitude lets the decoder charge cumulative rational growth.
func encodedExponent(text string) (int, error) {
	digits := strings.TrimLeft(strings.TrimLeft(text, "+-"), "0")

	if len(digits) > 4 {
		return 0, fmt.Errorf("%w: numeric exponent", faults.ErrEncodedLimit)
	}

	if digits == "" {
		return 0, nil
	}

	magnitude, err := strconv.Atoi(digits)

	if err != nil {
		return 0, fmt.Errorf("%w: numeric exponent", faults.ErrEncodedLimit)
	}

	if magnitude > encodedMaxExponent {
		return 0, fmt.Errorf("%w: numeric exponent", faults.ErrEncodedLimit)
	}

	return magnitude, nil
}
