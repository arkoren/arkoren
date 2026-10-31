package validate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestBuilderNestedMissingComparisonFails distinguishes a missing target from
// a real zero-valued field when enforcing cross-field equality.
func TestBuilderNestedMissingComparisonFails(t *testing.T) {
	t.Parallel()

	type profile struct {
		// Number provides the numeric value observed by nested conditions.
		Number int
	}
	type form struct {
		// Number provides the numeric value observed by nested conditions.
		Number int
		// Profile provides the nested object traversed by dotted field selectors.
		Profile *profile
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Number", func(number *validate.IntBuilder[int]) {
			number.EqField("Profile.Number")
		})
	})

	var failures validate.Errors

	if err := schema.Validate(context.Background(), form{
		Number: 1,
	}); !errors.As(err, &failures) {
		t.Fatalf("missing target = %v, want validation failure", err)
	}

	if failures["/Number"][0].Code != "eq_field" {
		t.Fatalf("failures = %#v", failures)
	}
}

// TestConditionValueCoercion verifies untyped constants match named and
// sized types, and mismatched kinds are rejected.
func TestConditionValueCoercion(t *testing.T) {
	t.Parallel()

	type form struct {
		// Role selects the identity or condition branch under test.
		Role role
		// Count provides a numeric value for conditional or inferred-field checks.
		Count int64
		// Ratio provides a floating-point sibling for typed observations.
		Ratio float32
		// On provides the boolean sibling used by conditional observations.
		On flag
		// When provides the instant used by time-schema builder checks.
		When time.Time
		// Ptr distinguishes an absent pointer from a present zero pointee.
		Ptr *string
		// Tgt provides the target value governed by the sibling observations.
		Tgt string
	}

	moment := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()

	schema := validate.Struct[form](func(object *validate.StructBuilder[form]) {
		object.Field("Tgt", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.And(func(condition *validate.ConditionBuilder) {
					condition.Eq("Role", "admin")
				}, func(condition *validate.ConditionBuilder) {
					condition.Eq("Count", 3)
				}, func(condition *validate.ConditionBuilder) {
					condition.Eq("Ratio",
						1.5)
				}, func(condition *validate.ConditionBuilder) {
					condition.Eq("On", true)
				}, func(condition *validate.ConditionBuilder) {
					condition.Eq("When", moment)
				}, func(condition *validate.ConditionBuilder) {
					condition.Eq("Ptr", nil)
				})
			})
		})
	})

	requireCodes(t, schema.Validate(ctx, form{
		Role:  "admin",
		Count: 3,
		Ratio: 1.5,
		On:    true,
		When:  moment,
	}),
		map[string][]string{"/Tgt": {"required_if"}})
	requireCodes(t, schema.Validate(ctx, form{
		Role:  "user",
		Count: 3,
		Ratio: 1.5,
		On:    true,
		When:  moment,
	}), nil)

	requirePanic(t, func() {
		validate.Struct[form](func(object *validate.StructBuilder[form]) {
			object.Field("Tgt", func(value *validate.StringBuilder[string]) {
				value.RequiredIf(func(condition *validate.ConditionBuilder) {
					condition.Eq("Ratio", 1e300)
				})
			})
		})
	})
	requirePanic(t, func() {
		validate.Struct[form](func(object *validate.StructBuilder[form]) {
			object.Field("Tgt", func(value *validate.StringBuilder[string]) {
				value.RequiredIf(func(condition *validate.ConditionBuilder) {
					condition.Eq("Count", "3")
				})
			})
		})
	})
	requirePanic(t, func() {
		validate.Struct[form](func(object *validate.StructBuilder[form]) {
			object.Field("Tgt", func(value *validate.StringBuilder[string]) {
				value.RequiredIf(func(condition *validate.ConditionBuilder) {
					condition.Eq("Count", nil)
				})
			})
		})
	})
	requirePanic(t, func() {
		validate.Struct[form](func(object *validate.StructBuilder[form]) {
			object.Field("Tgt", func(value *validate.StringBuilder[string]) {
				value.RequiredIf(func(condition *validate.ConditionBuilder) {

				})
			})
		})
	})
}

// TestEmbeddedBuilderAmbiguousCondition rejects an ambiguous promoted selector
// during construction rather than silently observing the first embedded field.
func TestEmbeddedBuilderAmbiguousCondition(t *testing.T) {
	t.Parallel()

	defer func() {
		err, ok := recover().(error)

		if !ok || !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("panic = %v, want ErrInvalidSchema", err)
		}
	}()

	validate.Struct(func(object *validate.StructBuilder[embeddedAmbiguous]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Eq("Role", "admin")
			})
		})
	})
}

// TestEmbeddedBuilderAmbiguousComparison checks field comparisons use the same
// Go selector rules as condition observations, independent of JSON name conflicts.
func TestEmbeddedBuilderAmbiguousComparison(t *testing.T) {
	t.Parallel()

	defer func() {
		err, ok := recover().(error)

		if !ok || !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("panic = %v, want ErrInvalidSchema", err)
		}
	}()

	validate.Struct(func(object *validate.StructBuilder[embeddedAmbiguous]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.EqField("Role")
		})
	})
}

// TestEmbeddedBuilderExplicitPaths resolves each embedded struct by its actual
// Go name, even when the promoted terminal selector would be ambiguous.
func TestEmbeddedBuilderExplicitPaths(t *testing.T) {
	t.Parallel()

	schema := validate.Struct(func(object *validate.StructBuilder[embeddedAmbiguous]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Or(func(group *validate.ConditionBuilder) {
					group.Eq("EmbeddedFirst.Role", "admin").Eq("EmbeddedSecond.Role", "admin")
				})
			})
		})
	})

	if err := schema.Validate(context.Background(), embeddedAmbiguous{
		EmbeddedFirst: EmbeddedFirst{
			Role: "admin",
		},
	}); err == nil {
		t.Fatal("first explicit path was not observed")
	}

	if err := schema.Validate(context.Background(), embeddedAmbiguous{
		EmbeddedSecond: EmbeddedSecond{
			Role: "admin",
		},
	}); err == nil {
		t.Fatal("second explicit path was not observed")
	}
}

// TestEmbeddedBuilderShallowSelectorWins preserves ordinary Go shadowing when
// the root and embedded fields have distinct JSON names and are both encoded.
func TestEmbeddedBuilderShallowSelectorWins(t *testing.T) {
	t.Parallel()

	type form struct {
		// EmbeddedFirst promotes the first candidate field in an embedding conflict.
		EmbeddedFirst
		// Role selects the identity or condition branch under test.
		Role string `json:"root_role"`
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Eq("Role", "admin")
			})
		})
	})

	if err := schema.Validate(context.Background(), form{
		EmbeddedFirst: EmbeddedFirst{
			Role: "admin",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := schema.Validate(context.Background(), form{
		Role: "admin",
	}); err == nil {
		t.Fatal("shallower selector was not observed")
	}
}

// TestRedesignNestedMissingNotEqIsFalse distinguishes an unresolved nested
// observation from an ordinary comparison with a zero-valued field.
func TestRedesignNestedMissingNotEqIsFalse(t *testing.T) {
	t.Parallel()

	type profile struct {
		// Role selects the identity or condition branch under test.
		Role string
	}
	type form struct {
		// Profile provides the nested object traversed by dotted field selectors.
		Profile *profile
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.NotEq("Profile.Role", "admin")
			})
		})
	})

	if err := schema.Validate(context.Background(), form{}); err != nil {
		t.Fatalf("nil parent must make NotEq false: %v", err)
	}

	if err := schema.Validate(context.Background(), form{
		Profile: &profile{
			Role: "user",
		},
	}); err == nil {
		t.Fatal("resolved unequal value must make Name required")
	}
}

// TestRedesignNestedZeroValueCanMatch distinguishes an existing zero-valued
// terminal field from a path that cannot be reached through a nil pointer.
func TestRedesignNestedZeroValueCanMatch(t *testing.T) {
	t.Parallel()

	type profile struct {
		// Role selects the identity or condition branch under test.
		Role string
	}
	type form struct {
		// Profile provides the nested object traversed by dotted field selectors.
		Profile *profile
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("Profile.Role", "")
			})
		})
	})

	if err := schema.Validate(context.Background(), form{}); err != nil {
		t.Fatalf("unreachable nested field matched an empty string: %v", err)
	}

	if err := schema.Validate(context.Background(), form{
		Profile: &profile{},
	}); err == nil {
		t.Fatal("reachable empty string must match an empty-string comparison")
	}
}

// TestRedesignExplicitNotComplementsMissingComparison verifies that grouping
// negation, unlike NotEq, deliberately includes an unresolved nested path.
func TestRedesignExplicitNotComplementsMissingComparison(t *testing.T) {
	t.Parallel()

	type profile struct {
		// Role selects the identity or condition branch under test.
		Role string
	}
	type form struct {
		// Profile provides the nested object traversed by dotted field selectors.
		Profile *profile
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Not(func(group *validate.ConditionBuilder) {
					group.Eq("Profile.Role", "admin")
				})
			})
		})
	})

	if err := schema.Validate(context.Background(), form{}); err == nil {
		t.Fatal("Not(false) must require Name when the nested parent is nil")
	}
}

// TestRedesignPresentUsesPointerAndCollectionPresence checks that an allocated
// zero pointee and allocated empty slice are present, while nil values are not.
func TestRedesignPresentUsesPointerAndCollectionPresence(t *testing.T) {
	t.Parallel()

	type form struct {
		// Number provides the numeric value observed by nested conditions.
		Number *int
		// Tags provides slice storage for child rules and copy-isolation checks.
		Tags []string
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Present("Number")
				predicate.Present("Tags")
			})
		})
	})

	if err := schema.Validate(context.Background(), form{}); err != nil {
		t.Fatal(err)
	}

	zero := 0

	if err := schema.Validate(context.Background(), form{
		Number: &zero,
	}); err != nil {
		t.Fatalf("root conditions must combine with AND: %v", err)
	}

	if err := schema.Validate(context.Background(), form{
		Number: &zero,
		Tags:   []string{},
	}); err == nil {
		t.Fatal("non-nil pointer and empty non-nil slice must both be present")
	}
}

// TestConditionOutsideStructPanics verifies conditions need a Struct.
func TestConditionOutsideStructPanics(t *testing.T) {
	t.Parallel()

	requirePanic(t, func() {
		validate.String(func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Present("A")
			})
		}).Validate(context.Background(), "")
	})

	requirePanic(t, func() {
		type holder struct {
			// Items provides child values for collection traversal and default checks.
			Items []string
		}

		validate.Struct[holder](func(object *validate.StructBuilder[holder]) {
			object.Field("Items", func(items *validate.SliceBuilder[string]) {
				items.Values(func(value *validate.StringBuilder[string]) {
					value.RequiredIf(func(condition *validate.ConditionBuilder) {
						condition.Present("Items")
					})
				})
			})
		})
	})
}
