package validate_test

import (
	"context"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestAnySchema checks unconstrained dynamic values and the distinction
// between nil and present input.
//
// Example:
//
//	go test -run "^TestAnySchema$" .
func TestAnySchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	wantSchema(t, validate.Any(), `{}`)
	wantSchema(t, validate.Any(func(value *validate.AnyBuilder) {
		value.Describe("anything")
	}), `{"description":"anything"}`)

	for _, value := range []any{"text", 3, []int{1}, map[string]any{"a": 1}} {
		if err := validate.Any(func(value *validate.AnyBuilder) {
			value.Required()
		}).Validate(ctx, value); err != nil {
			t.Fatalf("%v: %v", value, err)
		}
	}

	requireCode(t, validate.Any(func(value *validate.AnyBuilder) {
		value.Required()
	}).Validate(ctx, nil), "required")
}

// TestAnySchemaDefaultAndStruct checks Any defaults and registration on
// interface-valued struct fields.
//
// Example:
//
//	go test -run "^TestAnySchemaDefaultAndStruct$" .
func TestAnySchemaDefaultAndStruct(t *testing.T) {
	t.Parallel()

	type event struct {
		// Payload provides a dynamic value for Any defaults and child registration.
		Payload any `json:"payload"`
	}

	schema := validate.Struct[event](func(object *validate.StructBuilder[event]) {
		object.Field("Payload", func(value *validate.AnyBuilder) {
			value.Default("none")
			value.Describe("Event data")
		})
	})

	parsed, err := schema.Parse(context.Background(), event{})

	if err != nil || parsed.Payload != "none" {
		t.Fatalf("parsed = %v, %v", parsed, err)
	}

	wantSchema(t, schema, `{"properties":{"payload":{"default":"none","description":"Event data"}},"type":"object"}`)
}
