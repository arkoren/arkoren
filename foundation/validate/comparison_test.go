package validate_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestFloatFieldOrderingRejectsNaN checks that every ordered field predicate
// agrees with Go's floating comparisons: NaN is unordered on either side, not
// less than finite values as a total-order sorting comparator would report.
func TestFloatFieldOrderingRejectsNaN(t *testing.T) {
	t.Parallel()

	// bounds exercises the same predicates with NaN on both operand sides.
	type bounds struct {
		// Left is the unordered value.
		Left float64
		// Right is an ordinary finite comparison target.
		Right float64
	}

	schema := validate.Struct(func(object *validate.StructBuilder[bounds]) {
		object.Field("Left", func(number *validate.FloatBuilder[float64]) {
			number.GtField("Right").GteField("Right").LtField("Right").LteField("Right")
		})
		object.Field("Right", func(number *validate.FloatBuilder[float64]) {
			number.GtField("Left").GteField("Left").LtField("Left").LteField("Left")
		})
	})

	_, err := schema.Parse(context.Background(), bounds{Left: math.NaN(), Right: 1})

	requireCodes(t, err, map[string][]string{
		"/Left":  {"gt_field", "gte_field", "lt_field", "lte_field"},
		"/Right": {"gt_field", "gte_field", "lt_field", "lte_field"},
	})
}

// TestFieldComparisons verifies the cross-field comparison rules.
func TestFieldComparisons(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	schema := orderSchema()
	base := order{
		Kind:  "person",
		Phone: "1",
	}

	mismatch := base
	mismatch.Password, mismatch.Confirm = "secret", "other"

	requireCodes(t, schema.Validate(ctx, mismatch), map[string][]string{"/Confirm": {"eq_field"}})

	same := base
	same.Password, same.Confirm = "secret", "secret"

	requireCodes(t, schema.Validate(ctx, same), nil)

	lower := base
	lower.Min, lower.Max = 5, 3

	requireCodes(t, schema.Validate(ctx, lower), map[string][]string{"/Max": {"gte_field"}})

	equal := base
	equal.Min, equal.Max = 3, 3

	requireCodes(t, schema.Validate(ctx, equal), nil)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reversed := base
	reversed.From, reversed.To = start, start.Add(-time.Hour)

	requireCodes(t, schema.Validate(ctx, reversed), map[string][]string{"/To": {"after_field"}})
}

// TestFieldComparisonParams verifies the comparison names the other field
// by its JSON name.
func TestFieldComparisonParams(t *testing.T) {
	t.Parallel()

	type pair struct {
		// A provides one independently registered value for sibling-rule checks.
		A string `json:"first"`
		// B provides the second sibling value for comparison and presence checks.
		B string `json:"second"`
	}

	schema := validate.Struct[pair](func(object *validate.StructBuilder[pair]) {
		object.Field("B", func(value *validate.StringBuilder[string]) {
			value.NeField("A")
		})
	})

	err := schema.Validate(context.Background(), pair{
		A: "x",
		B: "x",
	})

	errs, _ := errors.AsType[validate.Errors](err)

	violation := errs["/second"][0]

	if violation.Code != "ne_field" || violation.Params["field"] != "first" ||
		violation.Message != "must not equal first" {
		t.Fatalf("violation = %#v", violation)
	}
}

// TestAllFieldComparisonRelations verifies the numeric relations.
func TestAllFieldComparisonRelations(t *testing.T) {
	t.Parallel()

	type span struct {
		// Low provides a lower bound for cross-field numeric comparisons.
		Low int
		// High provides an upper bound for cross-field numeric comparisons.
		High int
		// Fl provides the lower floating-point sibling bound.
		Fl float64
		// Fh provides the upper floating-point sibling bound.
		Fh float64
		// Ul provides the lower unsigned sibling bound.
		Ul uint
		// Uh provides the upper unsigned sibling bound.
		Uh uint
	}

	schema := validate.Struct[span](func(object *validate.StructBuilder[span]) {
		object.Field("High", func(number *validate.IntBuilder[int]) {
			number.GtField("Low")
			number.NeField("Low")
		})
		object.Field("Fh", func(number *validate.FloatBuilder[float64]) {
			number.LtField("Fl")
		})
		object.Field("Uh", func(number *validate.UintBuilder[uint]) {
			number.LteField("Ul")
		})
	})

	err := schema.Validate(context.Background(), span{
		Low:  2,
		High: 2,
		Fl:   1,
		Fh:   2,
		Ul:   1,
		Uh:   2,
	})

	requireCodes(t, err, map[string][]string{
		"/High": {"gt_field", "ne_field"},
		"/Fh":   {"lt_field"},
		"/Uh":   {"lte_field"},
	})
}
