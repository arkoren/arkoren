package validate_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestTextSchemaJSON checks that text marshalers render as JSON strings with
// their format annotation.
//
// Example:
//
//	go test -run "^TestTextSchemaJSON$" .
func TestTextSchemaJSON(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Text[validate.Duration](), `{"format":"go-duration","type":"string"}`)
	wantSchema(t, validate.Text[validate.Duration](func(value *validate.TextBuilder[validate.Duration]) {
		value.Describe("Per attempt")
	}), `{"description":"Per attempt","format":"go-duration","type":"string"}`)
	wantSchema(t, validate.Text[netip.Addr](), `{"type":"string"}`)
}

// TestTextSchemaPresence checks zero-value absence and Required for text-
// backed values.
//
// Example:
//
//	go test -run "^TestTextSchemaPresence$" .
func TestTextSchemaPresence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	required := validate.Text[validate.Duration](func(value *validate.TextBuilder[validate.Duration]) {
		value.Required()
	})

	requireCode(t, required.Validate(ctx, 0), "required")

	if err := required.Validate(ctx, validate.Duration(time.Second)); err != nil {
		t.Fatal(err)
	}
}

// TestTextSchemaDefaultAndCheck checks default application and custom rules on
// text-backed values.
//
// Example:
//
//	go test -run "^TestTextSchemaDefaultAndCheck$" .
func TestTextSchemaDefaultAndCheck(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.Text[validate.Duration](func(value *validate.TextBuilder[validate.Duration]) {
		value.Default(validate.Duration(time.Minute))
		value.Check(func(_ context.Context, v validate.Duration) error {
			if v.Std() > time.Hour {
				return validate.Fail("too_long", nil)
			}

			return nil
		})
	})

	parsed, err := schema.Parse(ctx, 0)

	if err != nil || parsed.Std() != time.Minute {
		t.Fatalf("parsed = %v, %v", parsed, err)
	}

	requireCode(t, schema.Validate(ctx, validate.Duration(2*time.Hour)), "too_long")
}

// TestTextSchemaInStruct checks typed text-child registration inside a struct
// schema.
//
// Example:
//
//	go test -run "^TestTextSchemaInStruct$" .
func TestTextSchemaInStruct(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[timedJob](func(object *validate.StructBuilder[timedJob]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Elapsed", func(value *validate.TextBuilder[validate.Duration]) {
			value.Required()
		})
	})

	requireCodes(t, schema.Validate(context.Background(), timedJob{
		Name: "x",
	}), map[string][]string{"/elapsed": {"required"}})
	wantSchema(t, schema, `{"properties":{"elapsed":{"format":"go-duration","type":"string"},"name":{"type":"string"}},"required":["name","elapsed"],"type":"object"}`)
}

// reuseTextValue deliberately supports both struct and text schema roles so
// tests can exercise exact-value-type reuse that must not erase child rules.
type reuseTextValue struct {
	// Name holds the name observed by string rules or sibling conditions.
	Name string
}

// MarshalText provides the text-role contract without changing field contents.
func (value *reuseTextValue) MarshalText() ([]byte, error) {
	return []byte(value.Name), nil
}

// UnmarshalText supplies Text's pointer constraint for the dual-role fixture.
func (value *reuseTextValue) UnmarshalText(text []byte) error {
	value.Name = string(text)

	return nil
}
