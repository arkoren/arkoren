package encoded_test

import (
	"testing"

	"arkoren.dev/foundation/validate"
)

func TestEncodedArrayPrefixItemsAndContains(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"array","minItems":2,"maxItems":4,"prefixItems":[{"const":"head"}],"items":{"type":"integer"},"contains":{"minimum":3,"type":"integer"},"minContains":1,"maxContains":2}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`["head",3,4]`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `["head",1,2]`)
	requireEncodedMismatch(t, check, `["head",3,4,5]`)
	requireEncodedMismatch(t, check, `["head",null]`)
	requireEncodedMismatch(t, check, `["head"]`)
	requireEncodedMismatch(t, check, `["head",3,4,1,1]`)
}

func TestEncodedDraft07TupleAndAdditionalItems(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$schema":"http://json-schema.org/draft-07/schema#","items":[{"type":"string"},{"type":"integer"}],"additionalItems":false}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`["head",0]`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `["head",0,null]`)
	requireEncodedMismatch(t, check, `["head",null]`)
}

func TestEncodedAdditionalItemsIgnoredWithoutLegacyTuple(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$schema":"https://json-schema.org/draft-07/schema#","items":{"type":"integer"},"additionalItems":false}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`[1,2,3]`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedContainsZeroMinimumAndUnevaluatedItems(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"contains":{"type":"integer"},"minContains":0,"maxContains":1,"unevaluatedItems":{"type":"string"}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`["a",1,"b"]`)); err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`["a"]`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `[null]`)
	requireEncodedMismatch(t, check, `[1,2]`)
}
