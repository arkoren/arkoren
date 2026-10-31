package validate_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestAtOutsideStructReportsInvalidSchema checks that a sibling-field failure
// cannot silently become a root violation when no object resolver is available.
// The declared At contract requires an invalid-schema error for an unknown field.
func TestAtOutsideStructReportsInvalidSchema(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return validate.At("Name", "app.name", nil)
		})
	})

	_, err := schema.Parse(context.Background(), "Ada")

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("Parse = %v, want ErrInvalidSchema for an unresolved At field", err)
	}
}
