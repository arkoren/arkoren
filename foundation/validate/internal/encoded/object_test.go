package encoded_test

import (
	"testing"

	"arkoren.dev/foundation/validate"
)

func TestEncodedObjectPatternsAndAdditionalProperties(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"object","minProperties":1,"maxProperties":3,"propertyNames":{"pattern":"^[a-z_]+$"},"properties":{"fixed":{"type":"integer"}},"patternProperties":{"^x":{"minimum":2},"_n$":{"maximum":3}},"additionalProperties":false}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"fixed":0,"x_n":3}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"x_n":4}`)
	requireEncodedMismatch(t, check, `{"x_n":1}`)
	requireEncodedMismatch(t, check, `{"extra":1}`)
	requireEncodedMismatch(t, check, `{"x9":2}`)
	requireEncodedMismatch(t, check, `{}`)
	requireEncodedMismatch(t, check, `{"x":2,"xx":2,"xxx":2,"xxxx":2}`)
}

func TestEncodedDependentKeywords(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"dependentRequired":{"a":["b"]},"dependentSchemas":{"b":{"properties":{"c":{"type":"integer"}},"required":["c"]}}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"a":null,"b":null,"c":0}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"a":null}`)
	requireEncodedMismatch(t, check, `{"b":0}`)
	requireEncodedMismatch(t, check, `{"b":0,"c":null}`)
}

func TestEncodedDraft07Dependencies(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"$schema":"http://json-schema.org/draft-07/schema#","dependencies":{"a":["b"],"b":{"required":["c"]}}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"a":null,"b":null,"c":null}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"a":null}`)
	requireEncodedMismatch(t, check, `{"b":null}`)
}
