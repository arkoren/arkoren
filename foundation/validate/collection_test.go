package validate_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestTypedCollectionCancellationStopsPredicates checks the callbacks inside a
// collection rule, not just the boundary between rules. Each predicate observes
// an entry once, and cancellation stops before invoking it for another entry.
func TestTypedCollectionCancellationStopsPredicates(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	calls := 0

	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Every(func(_ int, _ int) bool {
			calls++
			cancel()

			return true
		})
	})

	_, err := schema.Parse(ctx, []int{1, 2, 3})

	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("Parse error = %v; predicate calls = %d", err, calls)
	}
}

// TestMapNaNPredicateKeepsAssociatedValue covers the collection-observation
// path. Its predicate must receive the ranged value, not the zero value from
// a failed lookup of a NaN key.
func TestMapNaNPredicateKeepsAssociatedValue(t *testing.T) {
	t.Parallel()

	schema := validate.Map(func(entries *validate.MapBuilder[float64, int]) {
		entries.Every(func(_ float64, value int) bool {
			return value == 3
		})
	})

	if _, err := schema.Parse(context.Background(), map[float64]int{math.NaN(): 3}); err != nil {
		t.Fatalf("predicate did not receive the stored value: %v", err)
	}
}

// TestRedesignMapHasRecognizesZeroValue checks key existence rather than the
// presence or truthiness of the value stored at that key.
func TestRedesignMapHasRecognizesZeroValue(t *testing.T) {
	t.Parallel()

	type form struct {
		// Scores provides collection values for predicates and derived-count
		// constraints.
		Scores map[int]int
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Has("Scores", 0)
			})
		})
	})

	if err := schema.Validate(context.Background(), form{
		Scores: map[int]int{1: 0},
	}); err != nil {
		t.Fatal(err)
	}

	if err := schema.Validate(context.Background(), form{
		Scores: map[int]int{0: 0},
	}); err == nil {
		t.Fatal("zero key with zero value must exist")
	}
}
