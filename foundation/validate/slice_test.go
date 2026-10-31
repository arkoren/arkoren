package validate_test

import (
	"cmp"
	"context"
	"slices"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestBuilderSharedSliceKeepsLocalRestrictionsDetached checks Use reuses
// published element definitions without sharing mutable draft containers.
func TestBuilderSharedSliceKeepsLocalRestrictionsDetached(t *testing.T) {
	t.Parallel()

	shared := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(name *validate.StringBuilder[string]) {
			name.Trim()
		})
	})
	local := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Use(shared).Min(2)
	})

	if err := local.Validate(context.Background(), []string{" Ada "}); err == nil {
		t.Fatal("local minimum missing")
	}

	value, err := shared.Parse(context.Background(), []string{" Ada "})

	if err != nil {
		t.Fatal(err)
	}

	if len(value) != 1 || value[0] != "Ada" {
		t.Fatalf("value = %#v", value)
	}
}

// TestSliceRulesMinPass verifies the MinPass slice rule case.
func TestSliceRulesMinPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Min(1)
	}).Validate(context.Background(), []string{"a"}), "")
}

// TestSliceRulesMinFail verifies the MinFail slice rule case.
func TestSliceRulesMinFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Min(2)
	}).Validate(context.Background(), []string{"a"}), "slice.min")
}

// TestSliceRulesMinAbsentNil verifies the MinAbsentNil slice rule case.
func TestSliceRulesMinAbsentNil(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Min(2)
	}).Validate(context.Background(), nil), "")
}

// TestSliceRulesMinEmptyIsPresent verifies the MinEmptyIsPresent slice rule case.
func TestSliceRulesMinEmptyIsPresent(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Min(1)
	}).Validate(context.Background(), []string{}), "slice.min")
}

// TestSliceRulesMaxFail verifies the MaxFail slice rule case.
func TestSliceRulesMaxFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Max(1)
	}).Validate(context.Background(), []string{"a", "b"}), "slice.max")
}

// TestSliceRulesLenPass verifies the LenPass slice rule case.
func TestSliceRulesLenPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Len(2)
	}).Validate(context.Background(), []string{"a", "b"}), "")
}

// TestSliceRulesLenFail verifies the LenFail slice rule case.
func TestSliceRulesLenFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Len(2)
	}).Validate(context.Background(), []string{"a"}), "slice.len")
}

// TestSliceRulesUniquePass verifies the UniquePass slice rule case.
func TestSliceRulesUniquePass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Unique()
	}).Validate(context.Background(), []string{"a", "b"}), "")
}

// TestSliceRulesUniqueFail verifies the UniqueFail slice rule case.
func TestSliceRulesUniqueFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Unique()
	}).Validate(context.Background(), []string{"a", "b", "a"}), "slice.unique")
}

// TestSliceRulesRequiredNilFail verifies the RequiredNilFail slice rule case.
func TestSliceRulesRequiredNilFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Required()
	}).Validate(context.Background(), nil), "required")
}

// TestSliceElementPaths verifies element violations carry the index and
// that zero elements are still checked.
func TestSliceElementPaths(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
			value.Min(2)
		})
	})

	err := schema.Validate(context.Background(), []string{"ok", "", "x"})

	requireCodes(t, err, map[string][]string{
		"/1": {"string.min"},
		"/2": {"string.min"},
	})
}

// TestSliceUniqueNeedsComparable verifies Unique rejects slice elements.
func TestSliceUniqueNeedsComparable(t *testing.T) {
	t.Parallel()

	requirePanic(t, func() {
		validate.Slice(func(items *validate.SliceBuilder[[]string]) {
			items.Values(func(items *validate.SliceBuilder[string]) {
				items.Values(func(value *validate.StringBuilder[string]) {
				})
			})
		}, func(items *validate.SliceBuilder[[]string]) {
			items.Unique()
		})
	})
}

// TestSliceNilElementSchema verifies a missing element schema panics.
func TestSliceNilElementSchema(t *testing.T) {
	t.Parallel()

	requirePanic(t, func() {
		validate.Slice(func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
				value.Use(nil)
			})
		}).Validate(context.Background(), []string{"a"})
	})
}

// TestSliceDefaultIsCopied verifies each Parse gets its own default slice.
func TestSliceDefaultIsCopied(t *testing.T) {
	t.Parallel()

	source := []string{"a"}

	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Default(source)
	})

	source[0] = "changed"

	first, err := schema.Parse(context.Background(), nil)

	if err != nil {
		t.Fatal(err)
	}

	first[0] = "mutated"

	second, _ := schema.Parse(context.Background(), nil)

	if second[0] != "a" {
		t.Fatalf("default leaked: %v", second)
	}
}

// TestNormalizeSlice checks slice transformations and invalid callbacks while
// preserving caller-owned input.
//
// Example:
//
//	go test -run "^TestNormalizeSlice$" .
func TestNormalizeSlice(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	input := []int{3, 1, 2, 1}

	got, err := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
		})

		items.Sort(cmp.Compare[int])
	}).Parse(ctx, input)

	if err != nil || !slices.Equal(got, []int{1, 1, 2, 3}) {
		t.Fatalf("Sort = %v, %v", got, err)
	}

	if !slices.Equal(input, []int{3, 1, 2, 1}) {
		t.Fatalf("input mutated: %v", input)
	}

	got, _ = validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
		})

		items.Filter(func(i, e int) bool {
			return i > 0 && e > 1
		})
	}).Parse(ctx, input)

	if !slices.Equal(got, []int{2}) {
		t.Fatalf("Filter = %v", got)
	}

	got, _ = validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
		})

		items.Reject(func(i, e int) bool {
			return i == 0 || e == 2
		})
	}).Parse(ctx, input)

	if !slices.Equal(got, []int{1, 1}) {
		t.Fatalf("Reject = %v", got)
	}

	got, _ = validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
		})

		items.RejectZero()
	}).Parse(ctx, []int{0, 4, 0, 5})

	if !slices.Equal(got, []int{4, 5}) {
		t.Fatalf("RejectZero = %v", got)
	}

	got, _ = validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
		})

		items.Dedupe()
	}).Parse(ctx, []int{3, 1, 3, 2, 1})

	if !slices.Equal(got, []int{3, 1, 2}) {
		t.Fatalf("Dedupe = %v", got)
	}

	got, _ = validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
		})

		items.Sort(cmp.Compare[int])
	}).Parse(ctx, nil)

	if got != nil {
		t.Fatalf("nil should stay nil: %v", got)
	}

	structs, _ := validate.Slice(func(items *validate.SliceBuilder[normForm]) {
		items.Values(func(object *validate.StructBuilder[normForm]) {
		})

		items.RejectZero()
	}).
		Parse(ctx, []normForm{{}, {
			Name: "a",
		}})

	if len(structs) != 1 {
		t.Fatalf("RejectZero structs = %v", structs)
	}

	mustPanicInvalid(t, func() {
		validate.Slice(func(items *validate.SliceBuilder[[]int]) {
			items.Values(func(items *validate.SliceBuilder[int]) {
				items.Values(func(number *validate.IntBuilder[int]) {
				})
			})
		}, func(items *validate.SliceBuilder[[]int]) {
			items.Dedupe()
		})
	})
	mustPanicInvalid(t, func() {
		validate.Slice(func(items *validate.SliceBuilder[int]) {
			items.Values(func(number *validate.IntBuilder[int]) {
			})

			items.Sort(nil)
		})
	})
	mustPanicInvalid(t, func() {
		validate.Slice(func(items *validate.SliceBuilder[int]) {
			items.Values(func(number *validate.IntBuilder[int]) {
			})

			items.Filter(nil)
		})
	})
	mustPanicInvalid(t, func() {
		validate.Slice(func(items *validate.SliceBuilder[int]) {
			items.Values(func(number *validate.IntBuilder[int]) {
			})

			items.Reject(nil)
		})
	})
}

// TestNormalizeSliceOrderAndRules checks the ordering of element preparation,
// slice normalization and collection constraints.
//
// Example:
//
//	go test -run "^TestNormalizeSliceOrderAndRules$" .
func TestNormalizeSliceOrderAndRules(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	err := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.RejectZero()
		items.Min(1)
	}).Validate(ctx, []string{"", ""})

	requireCodes(t, err, map[string][]string{"": {"slice.min"}})

	err = validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Min(1)
		items.RejectZero()
	}).Validate(ctx, []string{"", ""})

	requireCodes(t, err, map[string][]string{"": {"slice.min"}})

	if err := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Unique()
		items.Dedupe()
	}).Validate(ctx, []string{"a", "a"}); err != nil {
		t.Fatalf("Dedupe before Unique: %v", err)
	}

	err = validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
			value.Min(2)
		})

		items.Dedupe()
	}).Validate(ctx, []string{"a", "a", "bb"})

	requireCodes(t, err, map[string][]string{"/0": {"string.min"}})

	// Normalizers run in written order.
	got, _ := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
		})

		items.Filter(func(_ int, e int) bool {
			return e != 1
		})
		items.Normalize(func(v []int) []int {
			return append([]int{len(v)}, v...)
		})
	}).
		Parse(ctx, []int{1, 2, 3})

	if !slices.Equal(got, []int{2, 2, 3}) {
		t.Fatalf("order = %v", got)
	}

	err = validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
		})

		items.Required()
		items.RejectZero()
	}).Validate(ctx, []int{})

	if err != nil {
		t.Fatalf("empty non-nil is present: %v", err)
	}

	// Elements are normalized before the slice normalizers run.
	strs, err := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
			value.Trim()
		})

		items.RejectZero()
	}).Parse(ctx, []string{" a ", "", " "})

	if err != nil || !slices.Equal(strs, []string{"a"}) {
		t.Fatalf("elements = %v, %v", strs, err)
	}
}

// TestNormalizeElementsBeforeSlice checks that parent filtering, deduplication
// and sorting receive already-normalized elements.
//
// Example:
//
//	go test -run "^TestNormalizeElementsBeforeSlice$" .
func TestNormalizeElementsBeforeSlice(t *testing.T) {
	ctx := context.Background()

	got, err := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
			value.Trim()
		})

		items.RejectZero()
	}).Parse(ctx, []string{"a", "  ", "b"})

	if err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Trim+RejectZero = %v, %v", got, err)
	}

	got, err = validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
			value.ToLower()
		})

		items.Dedupe()
	}).Parse(ctx, []string{"A", "a", "b"})

	if err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("ToLower+Dedupe = %v, %v", got, err)
	}

	nums, err := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Abs()
		})

		items.Sort(cmp.Compare[int])
	}).Parse(ctx, []int{-3, 1, -2})

	if err != nil || !slices.Equal(nums, []int{1, 2, 3}) {
		t.Fatalf("Abs+Sort = %v, %v", nums, err)
	}
}

// TestNormalizeElementOnce checks that element and map-value normalizers run
// exactly once per parse traversal.
//
// Example:
//
//	go test -run "^TestNormalizeElementOnce$" .
func TestNormalizeElementOnce(t *testing.T) {
	ctx := context.Background()
	inc := func(n int) int {
		return n + 1
	}

	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Normalize(inc)
		})

		items.Max(5)
	})

	input := []int{1, 2}

	got, err := schema.Parse(ctx, input)

	if err != nil || !slices.Equal(got, []int{2, 3}) {
		t.Fatalf("Parse = %v, %v", got, err)
	}

	if !slices.Equal(input, []int{1, 2}) {
		t.Fatalf("input mutated: %v", input)
	}

	if err := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Normalize(inc)
			number.Max(2)
		})
	}).Validate(ctx, []int{1, 2}); err == nil {
		t.Fatal("Validate: 2 normalizes to 3 once, want Max violation")
	}

	if err := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Normalize(inc)
			number.Max(3)
		})
	}).Validate(ctx, []int{1, 2}); err != nil {
		t.Fatalf("Validate = %v, want nil (applied once)", err)
	}

	type holder struct {
		// Nums provides numeric elements for nested normalization checks.
		Nums []int
	}

	obj := validate.Struct[holder](func(object *validate.StructBuilder[holder]) {
		object.Field("Nums", func(items *validate.SliceBuilder[int]) {
			items.Values(func(number *validate.IntBuilder[int]) {
				number.Normalize(inc)
			})
		})
	})

	res, err := obj.Parse(ctx, holder{
		Nums: []int{1},
	})

	if err != nil || !slices.Equal(res.Nums, []int{2}) {
		t.Fatalf("object field = %v, %v", res, err)
	}

	m, err := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Keys(func(value *validate.StringBuilder[string]) {
		})
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Normalize(inc)
		})
	}).Parse(ctx, map[string]int{"a": 1})

	if err != nil || m["a"] != 2 {
		t.Fatalf("map = %v, %v", m, err)
	}
}

// TestReuseBuilderPointerSlicePreservesElements covers pointer/value equivalence
// at the public Schema boundary, including its nested validation definitions.
func TestReuseBuilderPointerSlicePreservesElements(t *testing.T) {
	t.Parallel()

	shared := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Min(3)
		})
	})
	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Use(&shared)
	})

	if err := schema.Validate(context.Background(), []int{1}); err == nil {
		t.Fatal("pointer reuse dropped element validation")
	}
}

// TestReuseBuilderRejectsMultipleSliceChildren prevents later Use calls from
// replacing the source's constraints with a different element definition.
func TestReuseBuilderRejectsMultipleSliceChildren(t *testing.T) {
	t.Parallel()

	shared := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Min(3)
		})
	})

	requireInvalidReuse(t, func() {
		validate.Slice(func(items *validate.SliceBuilder[int]) {
			items.Use(shared)
			items.Use(shared)
		})
	})
}

// TestReuseBuilderRejectsReplacingImportedSliceChild protects shared element
// constraints while leaving ordinary local child replacement explicit.
func TestReuseBuilderRejectsReplacingImportedSliceChild(t *testing.T) {
	t.Parallel()

	shared := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Min(3)
		})
	})

	requireInvalidReuse(t, func() {
		validate.Slice(func(items *validate.SliceBuilder[int]) {
			items.Use(shared)
			items.Values(func(number *validate.IntBuilder[int]) {

			})
		})
	})
}
