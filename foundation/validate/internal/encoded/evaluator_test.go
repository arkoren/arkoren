package encoded_test

import (
	"errors"
	"testing"

	"arkoren.dev/foundation/validate"
)

// requireEncodedMismatch distinguishes schema rejection from malformed JSON,
// resource refusal, or cancellation. It exercises the public adapter boundary.
func requireEncodedMismatch(t *testing.T, check validate.EncodedSchema, document string) validate.Errors {
	t.Helper()

	err := check.Validate(t.Context(), []byte(document))

	failures, ok := errors.AsType[validate.Errors](err)

	if !ok || len(failures) == 0 {
		t.Fatalf("Validate = %v; want nonempty validate.Errors", err)
	}

	return failures
}

func TestEncodedNullIsAValue(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"object","required":["n"],"properties":{"n":{"type":"integer"}}}`))

	if err != nil {
		t.Fatal(err)
	}

	failures := requireEncodedMismatch(t, check, `{"n":null}`)

	if failures["/n"][0].Code != "type" {
		t.Fatalf("failures = %#v", failures)
	}

	if err := check.Validate(t.Context(), []byte(`{"n":0}`)); err != nil {
		t.Fatal(err)
	}

	failures = requireEncodedMismatch(t, check, `{}`)

	if failures["/n"][0].Code != "required" {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestEncodedDefaultsAreIgnoredEvenWhenInvalid(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"object","properties":{"n":{"type":"integer","default":"not an integer"}},"default":null}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"n":null}`)
}

func TestEncodedRequiredCannotBeSatisfiedByDefault(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"required":["n"],"properties":{"n":{"default":1}}}`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{}`)
}

func TestEncodedFormatAndContentAreAnnotations(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"string","format":"date-time","contentEncoding":"base64","contentMediaType":"application/json","contentSchema":false}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`"not a date or encoded JSON"`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedMinimumRejectsRoundedAdjacentInteger(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"minimum":9007199254740993}`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `9007199254740992`)

	if err := check.Validate(t.Context(), []byte(`9007199254740993`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedMaximumRejectsRoundedAdjacentInteger(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"maximum":9007199254740992}`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `9007199254740993`)
}

func TestEncodedDecimalMaximumDoesNotRound(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"maximum":0.1}`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `0.100000000000000000000000000001`)

	if err := check.Validate(t.Context(), []byte(`0.100000000000000000000000000000`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedIntegerDoesNotRoundFractionalToken(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"integer"}`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `1.000000000000000000000000000001`)

	if err := check.Validate(t.Context(), []byte(`1.0e3`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedAcceptsNumbersOutsideFloat64(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"integer","minimum":1e999,"maximum":1e1001,"multipleOf":1e998}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`1e1000`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedExactMultipleOf(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"multipleOf":0.000000000000000000000000000003}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`0.000000000000000000000000000009`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `0.000000000000000000000000000010`)
}

func TestEncodedExclusiveBounds(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"exclusiveMinimum":9007199254740992,"exclusiveMaximum":9007199254740994}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`9007199254740993`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `9007199254740992`)
	requireEncodedMismatch(t, check, `9007199254740994`)
}

func TestEncodedTypeUnionIncludesNull(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":["null","integer"]}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`null`)); err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`0`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `false`)
}

func TestEncodedBooleanSchemas(t *testing.T) {
	accept, err := validate.FromJSONSchemaStrict([]byte(`true`))

	if err != nil {
		t.Fatal(err)
	}

	if err := accept.Validate(t.Context(), []byte(`null`)); err != nil {
		t.Fatal(err)
	}

	reject, err := validate.FromJSONSchemaStrict([]byte(`false`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, reject, `null`)
}

func TestEncodedRuneLengthsAndRE2Patterns(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"string","minLength":2,"maxLength":2,"pattern":"^é.$"}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`"é😀"`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `"é"`)
	requireEncodedMismatch(t, check, `"é😀a"`)
	requireEncodedMismatch(t, check, `"ab"`)
}

func TestEncodedAllOfAnyOfOneOfAndNot(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"allOf":[{"type":"integer"},{"minimum":0}],"anyOf":[{"maximum":5},{"minimum":10}],"oneOf":[{"multipleOf":2},{"multipleOf":3}],"not":{"const":12}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`4`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `-2`)
	requireEncodedMismatch(t, check, `8`)
	requireEncodedMismatch(t, check, `0`)
	requireEncodedMismatch(t, check, `12`)
	requireEncodedMismatch(t, check, `null`)
}

func TestEncodedIfThenElse(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"if":{"properties":{"kind":{"const":"number"}},"required":["kind"]},"then":{"properties":{"value":{"type":"integer"}},"required":["value"]},"else":{"properties":{"value":{"type":"string"}}}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"kind":"number","value":1}`)); err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"value":"a"}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"kind":"number"}`)
	requireEncodedMismatch(t, check, `{"value":1}`)
}

func TestEncodedSuccessfulAnyOfBranchesContributeCoverage(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"anyOf":[{"properties":{"a":true}},{"properties":{"b":true}}],"unevaluatedProperties":false}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"a":null,"b":0}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"a":0,"c":0}`)
}

func TestEncodedFailedAnyOfBranchCannotContributeCoverage(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"anyOf":[{"properties":{"a":true}},{"properties":{"b":true},"required":["missing"]}],"unevaluatedProperties":false}`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"a":0,"b":0}`)
}

func TestEncodedAllOfItemCoverage(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"allOf":[{"prefixItems":[{"type":"integer"}]}],"unevaluatedItems":false}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`[1]`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `[1,2]`)
}

func TestEncodedRecursiveSchemaConsumesInput(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"object","properties":{"next":{"anyOf":[{"type":"null"},{"$ref":"#"}]}},"additionalProperties":false}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"next":{"next":{"next":null}}}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"next":{"next":false}}`)
}

func TestEncodedConditionAnnotationsContributeCoverage(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"if":{"properties":{"a":true},"required":["a"]},"then":{"properties":{"b":true}},"else":{"properties":{"c":true}},"unevaluatedProperties":false}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"a":0,"b":0}`)); err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"c":0}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"a":0,"c":0}`)
}

// TestEncodedMismatchUsesEscapedJSONPointer verifies caller-visible paths for
// object names that contain both JSON Pointer escape characters.
func TestEncodedMismatchUsesEscapedJSONPointer(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"properties":{"a/b~c":{"type":"integer"}}}`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(`{"a/b~c":null}`))

	var failures validate.Errors

	if !errors.As(err, &failures) {
		t.Fatalf("Validate = %v", err)
	}

	if failures["/a~1b~0c"][0].Code != "type" {
		t.Fatalf("failures = %#v", failures)
	}
}
