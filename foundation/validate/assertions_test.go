package validate_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestCollectionBuilderMapDuplicateProjectionUsesSortedFirstKey verifies stable
// conflict selection independently of Go's randomized map iteration.
func TestCollectionBuilderMapDuplicateProjectionUsesSortedFirstKey(t *testing.T) {
	t.Parallel()

	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.UniqueBy(func(key string, value int) int {
			return value
		})
	})

	var failures validate.Errors

	err := schema.Validate(context.Background(), map[string]int{"z": 7, "a": 7, "m": 7})

	if !errors.As(err, &failures) {
		t.Fatalf("error = %v", err)
	}

	if len(failures) != 2 || failures["/m"][0].Params["conflict"] != "a" || failures["/z"][0].Params["conflict"] != "a" {
		t.Fatalf("failures = %#v", failures)
	}
}

// TestCollectionBuilderCountErrorHasNumericParameters preserves the actual
// count and the numeric limit rather than substituting a description override.
func TestCollectionBuilderCountErrorHasNumericParameters(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.CountWhere(func(index, value int) bool {
			return value > 0
		}, func(count *validate.IntBuilder[int]) {
			count.Describe("Not an error override").Min(2)
		})
	})

	var failures validate.Errors

	err := schema.Validate(context.Background(), []int{-1, 3})

	if !errors.As(err, &failures) {
		t.Fatalf("error = %v", err)
	}

	violation := failures[""][0]

	if violation.Params["count"] != 1 || violation.Params["min"] != int64(2) {
		t.Fatalf("violation = %#v", violation)
	}

	if violation.Message == "Not an error override" {
		t.Fatal("Describe replaced an error")
	}
}

// TestCollectionBuilderCountRejectsCheck prevents operational failure code
// from being converted into a false derived-count observation.
func TestCollectionBuilderCountRejectsCheck(t *testing.T) {
	t.Parallel()

	defer func() {
		err, ok := recover().(error)

		if !ok || !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("panic = %v", err)
		}
	}()

	validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.CountWhere(func(index, value int) bool {
			return true
		}, func(count *validate.IntBuilder[int]) {
			count.Check(func(context.Context, int) error {
				return nil
			})
		})
	})
}

// TestCollectionBuilderCountRejectsNormalization preserves the source count
// instead of letting configuration transform it before numeric constraints.
func TestCollectionBuilderCountRejectsNormalization(t *testing.T) {
	t.Parallel()

	defer func() {
		err, ok := recover().(error)

		if !ok || !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("panic = %v", err)
		}
	}()

	validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.CountWhere(func(key string, value int) bool {
			return true
		}, func(count *validate.IntBuilder[int]) {
			count.Abs()
		})
	})
}

// TestCollectionBuilderMembershipEmptyLists distinguishes existential and
// universal key membership on present empty maps without dummy child schemas.
func TestCollectionBuilderMembershipEmptyLists(t *testing.T) {
	t.Parallel()

	anyKey := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.HasAny()
	})
	everyKey := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.HasAll()
	})

	if err := anyKey.Validate(context.Background(), map[string]int{}); err == nil {
		t.Fatal("empty HasAny passed")
	}

	if err := everyKey.Validate(context.Background(), map[string]int{}); err != nil {
		t.Fatal(err)
	}

	if err := anyKey.Validate(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

// TestCollectionBuilderObservationCountZeroForcesPresence exercises the pure
// condition path rather than the diagnostic assertion path.
func TestCollectionBuilderObservationCountZeroForcesPresence(t *testing.T) {
	t.Parallel()

	type form struct {
		// Scores provides collection values for predicates and derived-count
		// constraints.
		Scores []int
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.CountWhere("Scores", func(index, value int) bool {
					return value > 0
				}, func(count *validate.IntBuilder[int]) {
					count.Max(0)
				})
			})
		})
	})

	if err := schema.Validate(context.Background(), form{
		Scores: []int{-1},
	}); err == nil {
		t.Fatal("zero count was absent")
	}

	if err := schema.Validate(context.Background(), form{}); err == nil {
		t.Fatal("resolved nil collection must still produce a real zero count")
	}
}

// TestRedesignMapContainsKeepsEntryPairs prevents the key from one entry and
// the value from another entry from satisfying one collection observation.
func TestRedesignMapContainsKeepsEntryPairs(t *testing.T) {
	t.Parallel()

	type form struct {
		// Scores provides collection values for predicates and derived-count
		// constraints.
		Scores map[string]int
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Contains("Scores", func(key string, value int) bool {
					return key == "admin" && value == 7
				})
			})
		})
	})

	if err := schema.Validate(context.Background(), form{
		Scores: map[string]int{"admin": 1, "other": 7},
	}); err != nil {
		t.Fatalf("separate entries combined: %v", err)
	}

	if err := schema.Validate(context.Background(), form{
		Scores: map[string]int{"admin": 7},
	}); err == nil {
		t.Fatal("matching entry did not activate requirement")
	}
}

// TestRedesignEmptySliceEveryIsVacuous tests the universal assertion without
// conflating empty-but-present collections with absent collections.
func TestRedesignEmptySliceEveryIsVacuous(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(values *validate.SliceBuilder[int]) {
		values.Every(func(_ int, value int) bool {
			return value > 0
		})
	})

	if err := schema.Validate(context.Background(), []int{}); err != nil {
		t.Fatalf("Every on present empty slice must pass: %v", err)
	}
}

// TestRedesignEmptySliceContainsFails checks that the existential assertion
// does run for an allocated empty collection and cannot find a witness.
func TestRedesignEmptySliceContainsFails(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(values *validate.SliceBuilder[int]) {
		values.Contains(func(_ int, value int) bool {
			return value > 0
		})
	})

	if err := schema.Validate(context.Background(), []int{}); err == nil {
		t.Fatal("Contains on present empty slice must fail")
	}
}

// TestRedesignEveryReportsEachFailingIndex checks diagnostics independently
// from the assertion's aggregate failure and preserves JSON pointer paths.
func TestRedesignEveryReportsEachFailingIndex(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(values *validate.SliceBuilder[int]) {
		values.Every(func(_ int, value int) bool {
			return value > 0
		})
	})

	err := schema.Validate(context.Background(), []int{-1, 2, -3})

	var violations validate.Errors

	if !errors.As(err, &violations) {
		t.Fatalf("Validate = %v, want validation diagnostics", err)
	}

	if len(violations) != 2 || len(violations["/0"]) != 1 || len(violations["/2"]) != 1 {
		t.Fatalf("diagnostics = %#v, want exactly /0 and /2", violations)
	}
}

// TestRedesignCountWhereZeroRemainsPresent prevents ordinary optional-number
// semantics from bypassing a minimum on the derived zero count.
func TestRedesignCountWhereZeroRemainsPresent(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(values *validate.SliceBuilder[int]) {
		values.CountWhere(func(_ int, value int) bool {
			return value > 0
		}, func(count *validate.IntBuilder[int]) {
			count.Min(1)
		})
	})

	if err := schema.Validate(context.Background(), []int{-1}); err == nil {
		t.Fatal("derived zero count must fail Min(1)")
	}
}

// TestRedesignCountWhereZeroPassesMaximum checks the zero-count boundary of
// the derived constraint without requiring an arbitrary positive witness.
func TestRedesignCountWhereZeroPassesMaximum(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(values *validate.SliceBuilder[int]) {
		values.CountWhere(func(_ int, value int) bool {
			return value > 0
		}, func(count *validate.IntBuilder[int]) {
			count.Max(0)
		})
	})

	if err := schema.Validate(context.Background(), []int{-1}); err != nil {
		t.Fatal(err)
	}
}
