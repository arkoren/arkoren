package validate_test

import (
	"context"
	"reflect"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestDiagnosticOwnershipTypedOneOfKeepsDefinition checks both capture and
// publication for a named numeric enum. Reusing the original input slice or
// mutating diagnostic parameters must not change validation or schema export.
func TestDiagnosticOwnershipTypedOneOfKeepsDefinition(t *testing.T) {
	t.Parallel()

	// priority preserves the enum's scalar Go type in diagnostic parameters.
	type priority int

	allowed := []priority{3, 5}

	schema := validate.Int(func(number *validate.IntBuilder[priority]) {
		number.OneOf(allowed...)
	})

	allowed[0] = 7

	_, err := schema.Parse(context.Background(), priority(7))

	first := diagnosticViolation(t, err, "")

	first.Params["values"].([]any)[0] = priority(7)
	first.Params["added"] = true

	_, err = schema.Parse(context.Background(), priority(7))

	second := diagnosticViolation(t, err, "")

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{"values": []any{priority(3), priority(5)}}

	if !reflect.DeepEqual(second.Params, want) || string(encoded) != `{"enum":[3,5],"type":"integer"}` {
		t.Fatalf("OneOf definition changed: params=%#v, schema=%s", second.Params, encoded)
	}

	if _, err := schema.Parse(context.Background(), priority(3)); err != nil {
		t.Fatalf("OneOf lost an originally allowed value: %v", err)
	}
}
