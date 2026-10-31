package validate_test

import (
	"context"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestBoolRules verifies Accepted and Declined.
func TestBoolRules(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	if err := validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Accepted()
	}).Validate(ctx, true); err != nil {
		t.Fatal(err)
	}

	requireCodes(t, validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Accepted()
	}).Validate(ctx, false), map[string][]string{"": {"bool.accepted"}})

	if err := validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Declined()
	}).Validate(ctx, false); err != nil {
		t.Fatal(err)
	}

	requireCodes(t, validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Declined()
	}).Validate(ctx, true), map[string][]string{"": {"bool.declined"}})
	requireCodes(t, validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Required()
	}).Validate(ctx, false), map[string][]string{"": {"required"}})
}

// TestBoolNamedType verifies Bool preserves a named boolean type.
func TestBoolNamedType(t *testing.T) {
	t.Parallel()

	requireCodes(t, validate.Bool(func(value *validate.BoolBuilder[flag]) {
		value.Declined()
	}).Validate(context.Background(), true),
		map[string][]string{"": {"bool.declined"}})
}
