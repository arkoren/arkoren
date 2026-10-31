package encoded_test

import (
	"errors"
	"strings"
	"testing"

	"arkoren.dev/foundation/validate"
)

func TestEncodedNestedConstNumbersAndEquivalentSpelling(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"const":{"a":[9007199254740993,1e1000,0.100000000000000000000000000001],"b":null}}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"b":null,"a":[90071992547409930e-1,10e999,100000000000000000000000000001e-30]}`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"b":null,"a":[9007199254740992,1e1000,0.100000000000000000000000000001]}`)
}

func TestEncodedNestedEnumEquality(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"enum":[{"n":[9007199254740993,0.1]},null]}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`{"n":[9007199254740993.0,1e-1]}`)); err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`null`)); err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `{"n":[9007199254740992,0.1]}`)
}

func TestEncodedUniqueAdjacentLargeIntegersRemainDistinct(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"uniqueItems":true}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`[9007199254740992,9007199254740993]`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedUniqueNestedEquivalentNumbersAreDuplicates(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"uniqueItems":true}`))

	if err != nil {
		t.Fatal(err)
	}

	requireEncodedMismatch(t, check, `[{"a":[1e1000,-0],"b":0.1},{"b":0.10,"a":[10e999,0.0]}]`)

	if err := check.Validate(t.Context(), []byte(`[{},[],null,false,0,"0"]`)); err != nil {
		t.Fatal(err)
	}
}

// TestEncodedStructuralEqualityAllocationIsBounded uses repeated enum probes
// of a legal large string to exercise the cumulative comparison budget without
// expensive timing assumptions or retaining a huge graph in the test process.
func TestEncodedStructuralEqualityAllocationIsBounded(t *testing.T) {
	text := strings.Repeat("a", 1<<20)
	document := `"` + text + `"`

	check, err := validate.FromJSONSchemaStrict([]byte(`{
		"$ref":"#/$defs/a",
		"$defs":{
			"a":{"anyOf":[{"$ref":"#/$defs/b"},{"$ref":"#/$defs/b"}]},
			"b":{"anyOf":[{"$ref":"#/$defs/c"},{"$ref":"#/$defs/c"}]},
			"c":{"anyOf":[{"$ref":"#/$defs/d"},{"$ref":"#/$defs/d"}]},
			"d":{"anyOf":[{"$ref":"#/$defs/e"},{"$ref":"#/$defs/e"}]},
			"e":{"anyOf":[{"$ref":"#/$defs/check"},{"$ref":"#/$defs/check"}]},
			"check":{"enum":[` + document + `]}
		}
	}`))

	if err != nil {
		t.Fatal(err)
	}

	err = check.Validate(t.Context(), []byte(document))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("Validate = %v", err)
	}

	if _, ok := errors.AsType[validate.Errors](err); ok {
		t.Fatal("key allocation limit classified as schema mismatch")
	}
}
