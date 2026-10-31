package validate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestConditionBuilderNearestStructType checks generic If binds to the child
// struct rather than leaking the root type through pointer/composite plumbing.
func TestConditionBuilderNearestStructType(t *testing.T) {
	t.Parallel()

	type profile struct {
		// Enabled controls the conditional rule branch without modifying the checked
		// value.
		Enabled bool
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}
	type form struct {
		// Profile provides the nested object traversed by dotted field selectors.
		Profile *profile
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Profile", func(pointer *validate.PtrBuilder[profile]) {
			pointer.Value(func(details *validate.StructBuilder[profile]) {
				details.Field("Name", func(name *validate.StringBuilder[string]) {
					name.RequiredIf(func(predicate *validate.ConditionBuilder) {
						predicate.If(func(value profile) bool {
							return value.Enabled
						})
					})
				})
			})
		})
	})

	if err := schema.Validate(context.Background(), form{
		Profile: &profile{
			Enabled: true,
		},
	}); err == nil {
		t.Fatal("inner If did not require Name")
	}

	if err := schema.Validate(context.Background(), form{}); err != nil {
		t.Fatal(err)
	}
}

// TestConditionBuilderRejectsNamedSliceIndex enforces exact int for indexes;
// matching an integer's underlying kind is insufficient for a typed callback.
func TestConditionBuilderRejectsNamedSliceIndex(t *testing.T) {
	t.Parallel()

	type index int
	type form struct {
		// Scores provides collection values for predicates and derived-count
		// constraints.
		Scores []int
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	defer func() {
		err, ok := recover().(error)

		if !ok || !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("panic = %v", err)
		}
	}()

	validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Contains("Scores", func(key index, value int) bool {
					return true
				})
			})
		})
	})
}

// TestConditionBuilderEmptyGroupInvalid rejects missing observations rather
// than introducing a vacuously true condition at schema construction.
func TestConditionBuilderEmptyGroupInvalid(t *testing.T) {
	t.Parallel()

	defer func() {
		err, ok := recover().(error)

		if !ok || !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("panic = %v", err)
		}
	}()

	validate.String(func(name *validate.StringBuilder[string]) {
		name.RequiredIf(func(predicate *validate.ConditionBuilder) {
			predicate.Or()
		})
	})
}

// TestConditionBuilderWhenDoesNotReplayUnconditionalElementNormalization
// separates preparation of shared children from additional conditional rules.
func TestConditionBuilderWhenDoesNotReplayUnconditionalElementNormalization(t *testing.T) {
	t.Parallel()

	type form struct {
		// Enabled controls the conditional rule branch without modifying the checked
		// value.
		Enabled bool
		// Scores provides collection values for predicates and derived-count
		// constraints.
		Scores []int
	}

	calls := 0

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Scores", func(items *validate.SliceBuilder[int]) {
			items.Values(func(number *validate.IntBuilder[int]) {
				number.Normalize(func(value int) int {
					calls++

					return value + 1
				})
			})
			items.When(func(predicate *validate.ConditionBuilder) {
				predicate.Eq("Enabled", true)
			}, func(selected *validate.SliceBuilder[int]) {
				selected.Min(1)
			})
		})
	})

	value, err := schema.Parse(context.Background(), form{
		Enabled: true,
		Scores:  []int{1},
	})

	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 || value.Scores[0] != 2 {
		t.Fatalf("calls = %d, scores = %v", calls, value.Scores)
	}
}

// TestConditionsAreImmutable verifies siblings derived with conditions stay
// independent.
func TestConditionsAreImmutable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	on := func(condition *validate.ConditionBuilder) {
		condition.Eq("Gate", "on")
	}

	base := validate.String(func(value *validate.StringBuilder[string]) {
		value.Bail()
	})
	required := validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(base)
		value.RequiredIf(on)
	})
	prohibited := validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(base)
		value.ProhibitedIf(on)
	})

	schema := func(field validate.StringSchema[string]) validate.StructSchema[derivedObject] {
		return validate.Struct[derivedObject](func(object *validate.StructBuilder[derivedObject]) {
			object.Field("Gate", func(value *validate.StringBuilder[string]) {
			})
			object.Field("Val", func(value *validate.StringBuilder[string]) {
				value.Use(field)
			})
		})
	}

	requireCodes(t, schema(base).Validate(ctx, derivedObject{
		Gate: "on",
	}), nil)
	requireCodes(t, schema(required).Validate(ctx, derivedObject{
		Gate: "on",
	}),
		map[string][]string{"/Val": {"required_if"}})
	requireCodes(t, schema(prohibited).Validate(ctx, derivedObject{
		Gate: "on",
		Val:  "x",
	}),
		map[string][]string{"/Val": {"prohibited_if"}})
	requireCodes(t, schema(prohibited).Validate(ctx, derivedObject{
		Gate: "on",
	}), nil)
	requireCodes(t, schema(required).Validate(ctx, derivedObject{
		Gate: "on",
		Val:  "x",
	}), nil)
}

// TestConditionCombinatorsAndTrue verifies the AndTrue condition combinator case.
func TestConditionCombinatorsAndTrue(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.And(func(condition *validate.ConditionBuilder) {
			condition.Eq("A", "a")
		}, func(condition *validate.ConditionBuilder) {
			condition.Present(
				"B")
		})
	}, condForm{
		A: "a",
		B: "b",
	}, true)
}

// TestConditionCombinatorsAndFalse verifies the AndFalse condition combinator case.
func TestConditionCombinatorsAndFalse(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.And(func(condition *validate.ConditionBuilder) {
			condition.Eq("A", "a")
		}, func(condition *validate.ConditionBuilder) {
			condition.Present(
				"B")
		})
	}, condForm{
		A: "a",
	}, false)
}

// TestConditionCombinatorsOrTrue verifies the OrTrue condition combinator case.
func TestConditionCombinatorsOrTrue(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.Or(func(condition *validate.ConditionBuilder) {
			condition.Eq("A", "z")
		}, func(condition *validate.ConditionBuilder) {
			condition.Present(
				"B",
			)
		})
	}, condForm{
		B: "b",
	}, true)
}

// TestConditionCombinatorsOrFalse verifies the OrFalse condition combinator case.
func TestConditionCombinatorsOrFalse(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.Or(func(condition *validate.ConditionBuilder) {
			condition.Eq("A", "z")
		}, func(condition *validate.ConditionBuilder) {
			condition.Present(
				"B",
			)
		})
	}, condForm{}, false)
}

// TestConditionCombinatorsNotTrue verifies the NotTrue condition combinator case.
func TestConditionCombinatorsNotTrue(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.Not(func(condition *validate.ConditionBuilder) {
			condition.Present("A")
		})
	}, condForm{}, true)
}

// TestConditionCombinatorsAbsentTrue verifies the AbsentTrue condition combinator case.
func TestConditionCombinatorsAbsentTrue(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.Absent("A")
	}, condForm{}, true)
}

// TestConditionCombinatorsAbsentFalse verifies the AbsentFalse condition combinator case.
func TestConditionCombinatorsAbsentFalse(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.Absent("A")
	}, condForm{
		A: "x",
	}, false)
}

// TestConditionCombinatorsNotEqTrue verifies the NotEqTrue condition combinator case.
func TestConditionCombinatorsNotEqTrue(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.NotEq("A", "a")
	}, condForm{
		A: "b",
	}, true)
}

// TestConditionCombinatorsInFalse verifies the InFalse condition combinator case.
func TestConditionCombinatorsInFalse(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.In("A", "a", "b")
	}, condForm{
		A: "c",
	}, false)
}

// TestConditionCombinatorsNotInTrue verifies the NotInTrue condition combinator case.
func TestConditionCombinatorsNotInTrue(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.NotIn("A", "a", "b")
	}, condForm{
		A: "c",
	}, true)
}

// TestConditionCombinatorsIntConstant verifies the IntConstant condition combinator case.
func TestConditionCombinatorsIntConstant(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.Eq("N", 3)
	}, condForm{
		N: 3,
	}, true)
}

// TestConditionCombinatorsIfTrue verifies the IfTrue condition combinator case.
func TestConditionCombinatorsIfTrue(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.If(func(value condForm) bool {
			return value.N > 1
		})
	}, condForm{
		N: 2,
	}, true)
}

// TestConditionCombinatorsIfFalse verifies the IfFalse condition combinator case.
func TestConditionCombinatorsIfFalse(t *testing.T) {
	t.Parallel()

	requireCondition(t, func(condition *validate.ConditionBuilder) {
		condition.If(func(value condForm) bool {
			return value.N > 1
		})
	}, condForm{
		N: 1,
	}, false)
}

// TestConditionConstructionProblems verifies bad conditions are reported
// by Struct, all at once.
func TestConditionConstructionProblems(t *testing.T) {
	t.Parallel()

	type form struct {
		// Role selects the identity or condition branch under test.
		Role string
		// Count provides a numeric value for conditional or inferred-field checks.
		Count int
		// Tags provides slice storage for child rules and copy-isolation checks.
		Tags []string
		// Other provides an independent sibling for comparison or registration
		// checks.
		Other string
		// Date provides a time field for exact condition-type checks.
		Date time.Time
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	message := requirePanic(t, func() {
		validate.Struct[form](func(object *validate.StructBuilder[form]) {
			object.Field("Name", func(value *validate.StringBuilder[string]) {
				value.RequiredIf(func(condition *validate.ConditionBuilder) {
					condition.Eq("Nope", "x")
				})
				value.RequiredIf(func(condition *validate.ConditionBuilder) {
					condition.Eq("Count", "text")
				})
				value.RequiredIf(func(condition *validate.ConditionBuilder) {
					condition.Eq("Tags", []string{"a"})
				})
				value.RequiredIf(func(condition *validate.ConditionBuilder) {
					condition.If(func(int) bool {
						return true
					})
				})
				value.RequiredWith("Missing")
				value.EqField("Count")
			})
		})
	})

	wants := []string{
		`unknown field "Nope"`,
		`Eq("Count"): value "text" is not assignable to int`,
		`type []string is not comparable`,
		`If[int] does not match the enclosing object type validate_test.form`,
		`unknown field "Missing"`,
		`eq_field("Count"): field has type int, want string`,
	}

	for _, want := range wants {
		if !strings.Contains(message, want) {
			t.Errorf("report misses %q:\n%s", want, message)
		}
	}
}
