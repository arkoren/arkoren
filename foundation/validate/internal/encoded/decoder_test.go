package encoded_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/foundation/validate"
)

func TestEncodedMalformedInstanceIsNotAMismatch(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`{"secret":"private",`))

	if !errors.Is(err, validate.ErrEncodedJSON) || strings.Contains(err.Error(), "private") {
		t.Fatalf("Validate = %v", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("malformed JSON classified as schema mismatch")
	}
}

func TestEncodedDuplicateNamesAreMalformed(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`{"a":1,"\u0061":2}`))

	if !errors.Is(err, validate.ErrEncodedJSON) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedTrailingDocumentIsMalformed(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`null true`))

	if !errors.Is(err, validate.ErrEncodedJSON) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedDuplicateSchemaNamesFailCompilation(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"type":"integer","type":"null"}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedCancellationDuringTokenTraversal(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	ctx := &encodedCancelContext{Context: t.Context(), after: 8}

	err = check.Validate(ctx, []byte(`[1,2,3,4,5,6,7,8,9]`))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedExponentBoundIsNotAMismatch(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`1e4097`))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("numeric limit classified as schema mismatch")
	}
}

func TestEncodedZeroHugeExponentIsBoundedBeforeAllocation(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`0e99999999999999999999999999999999999999`))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedSchemaExponentBoundRetainsBothIdentities(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"maximum":1e999999999999999999999999}`))

	if !errors.Is(err, validate.ErrEncodedLimit) || !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedCoefficientDigitsAreBounded(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(strings.Repeat("1", 4097)))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedDocumentBytesAreBounded(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), bytes.Repeat([]byte(" "), (8<<20)+1))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedDocumentDepthIsBounded(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	document := strings.Repeat("[", 257) + "null" + strings.Repeat("]", 257)

	err = check.Validate(t.Context(), []byte(document))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedEmptyContainersCannotExceedDepthBound(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	document := strings.Repeat("[", 257) + strings.Repeat("]", 257)

	err = check.Validate(t.Context(), []byte(document))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEncodedExactDepthBoundaryAcceptsScalarLeaf(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	document := strings.Repeat("[", 256) + "null" + strings.Repeat("]", 256)

	if err := check.Validate(t.Context(), []byte(document)); err != nil {
		t.Fatal(err)
	}
}

// TestEncodedCumulativeNumericExpansionIsBounded checks allocation policy with
// cheap zero tokens: individual exponents are legal, but their combined decimal
// expansion cannot multiply a small wire document into unlimited rational work.
func TestEncodedCumulativeNumericExpansionIsBounded(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	document := "[" + strings.Repeat("0e4096,", 16384) + "0]"

	err = check.Validate(t.Context(), []byte(document))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("numeric expansion limit classified as schema mismatch")
	}
}

// TestEncodedMaximumExponentBoundaryRemainsExact verifies that the documented
// maximum is accepted, including exponent leading zeros and equivalent decimal
// spellings, instead of accidentally becoming a binary64 or machine-int bound.
func TestEncodedMaximumExponentBoundaryRemainsExact(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"integer","const":1e4096}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`1E+00000000000000004096`)); err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`10e4095`)); err != nil {
		t.Fatal(err)
	}
}

// TestEncodedMinimumExponentBoundaryRemainsFractional ensures very small
// accepted values do not underflow to zero or become mathematical integers.
func TestEncodedMinimumExponentBoundaryRemainsFractional(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"number","minimum":1e-4096,"maximum":1e-4096,"not":{"type":"integer"}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`10e-4097`)); !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v; explicit exponent must remain within policy", err)
	}

	if err := check.Validate(t.Context(), []byte(`1e-4096`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `0`)
}

// TestEncodedMalformedUTF8IsNotAMismatch exercises the strict JSON-text
// boundary. Invalid encoding cannot reach string length or pattern assertions.
func TestEncodedMalformedUTF8IsNotAMismatch(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"string"}`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte{'"', 0xff, '"'})

	if !errors.Is(err, validate.ErrEncodedJSON) {
		t.Fatalf("Validate = %v", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("malformed UTF-8 classified as schema mismatch")
	}
}
