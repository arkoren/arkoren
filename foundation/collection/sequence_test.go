package collection_test

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/foundation/collection"
)

// ExampleLazySlice demonstrates deferred observation and dense output indices.
func ExampleLazySlice() {
	total := 0
	items := collection.NewSlice([]int{1, 2, 3}).Lazy().TapEach(func(_ int, value int) {
		total += value
	}).Filter(func(_ int, value int) bool {
		return value%2 == 0
	})

	fmt.Println(total)

	for index, value := range items.Iter() {
		fmt.Println(index, value)
	}

	fmt.Println(total)
	// Output:
	// 0
	// 0 2
	// 6
}

// ExampleLazySlice_UnmarshalJSON demonstrates a replayable decoded source.
func ExampleLazySlice_UnmarshalJSON() {
	var items collection.LazySlice[int]

	if err := items.UnmarshalJSON([]byte(`[1,2]`)); err != nil {
		panic(err)
	}

	fmt.Println(items.Items(), items.Items())
	// Output: [1 2] [1 2]
}

// TestStrideLazyNthClampsNegativeOffsets requires lazy selection to match
// eager selection, including offsets whose subtraction would overflow.
func TestStrideLazyNthClampsNegativeOffsets(t *testing.T) {
	t.Parallel()

	source := collection.NewSlice([]int{1, 2, 3, 4, 5}).Lazy()

	for _, offset := range []int{-1, math.MinInt} {
		if got := source.Nth(2, offset).Items(); !slices.Equal(got, []int{1, 3, 5}) {
			t.Fatalf("Nth(2, %d) = %v, want [1 3 5]", offset, got)
		}
	}
}

// TestStrideLazyNthMaxIntStepCountsPositions verifies that arbitrary source
// indices do not affect selection and that output indices remain dense.
func TestStrideLazyNthMaxIntStepCountsPositions(t *testing.T) {
	t.Parallel()

	source := collection.NewLazySlice(func(yield func(int, int) bool) {
		for _, index := range []int{math.MaxInt, math.MinInt, 99} {
			if !yield(index, index) {
				return
			}
		}
	})

	var indices, values []int

	for index, value := range source.Nth(math.MaxInt, 1) {
		indices = append(indices, index)
		values = append(values, value)
	}

	if !slices.Equal(indices, []int{0}) || !slices.Equal(values, []int{math.MinInt}) {
		t.Fatalf("indices = %v, values = %v, want [0], [%d]", indices, values, math.MinInt)
	}
}

// TestStrideLazyNthStopsAtSelection checks that construction is lazy and
// downstream stopping prevents consumption of a MaxInt-sized following gap.
func TestStrideLazyNthStopsAtSelection(t *testing.T) {
	t.Parallel()

	consumed := 0
	source := collection.NewLazySlice(func(yield func(int, int) bool) {
		for index := range 5 {
			consumed++

			if !yield(index, index+1) {
				return
			}
		}
	})

	selected := source.Nth(math.MaxInt, 1)

	if consumed != 0 {
		t.Fatal("Nth consumed source before iteration")
	}

	for index, value := range selected {
		if index != 0 || value != 2 {
			t.Fatalf("selection = (%d, %d), want (0, 2)", index, value)
		}

		break
	}

	if consumed != 2 {
		t.Fatalf("consumed = %d, want 2", consumed)
	}
}

// TestStrideLazySlidingMaxIntStep checks finite iteration at the largest
// step while retaining the lazy form's independent window ownership.
func TestStrideLazySlidingMaxIntStep(t *testing.T) {
	t.Parallel()

	source := collection.NewSlice([]int{1, 2, 3})
	var windows [][]int

	for _, window := range source.Lazy().Sliding(1, math.MaxInt) {
		windows = append(windows, window)
	}

	if !reflect.DeepEqual(windows, [][]int{{1}}) {
		t.Fatalf("windows = %v, want [[1]]", windows)
	}

	windows[0][0] = 9

	if source[0] != 1 {
		t.Fatal("lazy window aliases source storage")
	}
}

// TestNewLazySliceCreatesLazySliceFromFunctionAndYieldsAllItems verifies that
// NewLazySlice wraps the given iterator function and yields all items when consumed.
func TestNewLazySliceCreatesLazySliceFromFunctionAndYieldsAllItems(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, lazy.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceCanBeUsedDirectlyInForRangeLoop verifies that LazySlice[T] is a
// function type that can be ranged over without any adapter method.
func TestLazySliceCanBeUsedDirectlyInForRangeLoop(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		10, 20, 30,
	}))

	var got []int

	for _, v := range lazy {
		got = append(got, v)
	}

	if !reflect.DeepEqual([]int{
		10, 20, 30,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceEagerMaterializesIntoEagerCollection verifies that Eager
// returns an eager Slice containing all yielded items.
func TestLazySliceEagerMaterializesIntoEagerCollection(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		5, 6, 7,
	}))

	c := lazy.Eager()

	if !reflect.DeepEqual([]int{
		5, 6, 7,
	}, c.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceItemsMaterializesAllItems verifies that Items collects every item
// into a plain Go slice.
func TestLazySliceItemsMaterializesAllItems(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]string{
		"a", "b", "c",
	}))

	if !reflect.DeepEqual([]string{
		"a", "b", "c",
	}, lazy.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceItemsAsMaterializesNamedSlice verifies that ItemsAs preserves a
// caller-defined slice type.
func TestLazySliceItemsAsMaterializesNamedSlice(t *testing.T) {
	t.Parallel()

	type namedInts []int
	lazy := collection.NewLazySlice(slices.All([]int{1, 2, 3}))

	got := lazy.ItemsAs[namedInts]()
	want := namedInts{1, 2, 3}

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// TestLazySliceIsEmptyReturnsTrueForEmptyLazy verifies that IsEmpty is true when
// the iterator yields nothing.
func TestLazySliceIsEmptyReturnsTrueForEmptyLazy(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	if !lazy.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestLazySliceIsEmptyReturnsFalseForNonEmptyLazy verifies that IsEmpty is false
// when the iterator yields at least one item.
func TestLazySliceIsEmptyReturnsFalseForNonEmptyLazy(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1,
	}))

	if lazy.IsEmpty() {
		t.Fatal("expected false")
	}
}

// TestLazySliceIsNotEmptyReturnsTrueForNonEmptyLazy verifies that IsNotEmpty is
// true when the iterator yields at least one item.
func TestLazySliceIsNotEmptyReturnsTrueForNonEmptyLazy(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		42,
	}))

	if !lazy.IsNotEmpty() {
		t.Fatal("expected true")
	}
}

// TestLazySliceIsNotEmptyReturnsFalseForEmptyLazy verifies that IsNotEmpty is
// false when the iterator yields nothing.
func TestLazySliceIsNotEmptyReturnsFalseForEmptyLazy(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	if lazy.IsNotEmpty() {
		t.Fatal("expected false")
	}
}

// TestLazySliceLenReturnsTotalCount verifies that Len returns the number of items
// yielded by the sequence.
func TestLazySliceLenReturnsTotalCount(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	if !reflect.DeepEqual(5, lazy.Len()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceLenReturnsZeroForEmpty verifies that Len is 0 for an empty lazy.
func TestLazySliceLenReturnsZeroForEmpty(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	if !reflect.DeepEqual(0, lazy.Len()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceEveryReturnsTrueWhenAllItemsMatch verifies that Every returns true
// when the predicate holds for every item.
func TestLazySliceEveryReturnsTrueWhenAllItemsMatch(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		2, 4, 6,
	}))

	if !lazy.Every(func(i int, v int) bool {
		return v%2 == 0
	}) {
		t.Fatal("expected true")
	}
}

// TestLazySliceEveryReturnsFalseWhenOneItemFails verifies that Every returns false
// as soon as any item fails the predicate.
func TestLazySliceEveryReturnsFalseWhenOneItemFails(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		2, 3, 6,
	}))

	if lazy.Every(func(i int, v int) bool {
		return v%2 == 0
	}) {
		t.Fatal("expected false")
	}
}

// TestLazySliceEveryReturnsTrueOnEmpty verifies the vacuous truth: Every on an
// empty sequence returns true.
func TestLazySliceEveryReturnsTrueOnEmpty(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	if !lazy.Every(func(i int, v int) bool {
		return false
	}) {
		t.Fatal("expected true")
	}
}

// TestLazySliceEachCallsFForEveryItem verifies that Each invokes f once per item.
func TestLazySliceEachCallsFForEveryItem(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	var got []int

	lazy.Each(func(i int, v int) {
		got = append(got, v)
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceEachDoesNotCallFOnEmpty verifies that Each never calls f when the
// sequence is empty.
func TestLazySliceEachDoesNotCallFOnEmpty(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	called := false

	lazy.Each(func(i int, v int) {
		called = true
	})

	if called {
		t.Fatal("expected false")
	}
}

// TestLazySliceTapEachCallsFForEveryItemWhenConsumed verifies that TapEach calls
// f for each item and still yields the same items.
func TestLazySliceTapEachCallsFForEveryItemWhenConsumed(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		7, 8, 9,
	}))

	var tapped []int

	result := lazy.TapEach(func(i int, v int) {
		tapped = append(tapped, v)
	})

	if len(tapped) != 0 {
		t.Fatal("expected empty value")
	} // not yet consumed

	got := result.Items()

	if !reflect.DeepEqual([]int{
		7, 8, 9,
	}, got) {
		t.Fatal("values differ")
	}

	if !reflect.DeepEqual([]int{
		7, 8, 9,
	}, tapped) {
		t.Fatal("values differ")
	}
}

// TestLazySliceTapEachIsLazyAndDoesNotCallFBeforeConsumption verifies that the
// side-effect function is not invoked until the returned LazySlice is consumed.
func TestLazySliceTapEachIsLazyAndDoesNotCallFBeforeConsumption(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	callCount := 0

	tapped := lazy.TapEach(func(i int, v int) {
		callCount++
	})

	if !reflect.DeepEqual(0, callCount) {
		t.Fatal("values differ")
	} // not called yet

	_ = tapped.Items()

	if !reflect.DeepEqual(3, callCount) {
		t.Fatal("values differ")
	}
}

// TestLazySliceFilterReturnsMatchingItems verifies that Filter keeps only the items
// for which the predicate returns true.
func TestLazySliceFilterReturnsMatchingItems(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	got := lazy.Filter(func(i int, v int) bool {
		return v%2 == 0
	}).Items()

	if !reflect.DeepEqual([]int{
		2, 4,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceFilterYieldsDenseOutputIndices verifies that Filter reindexes output.
func TestLazySliceFilterYieldsDenseOutputIndices(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))
	var indices []int

	for index := range lazy.Filter(func(_ int, value int) bool {
		return value%2 == 0
	}) {
		indices = append(indices, index)
	}

	if !reflect.DeepEqual([]int{
		0, 1,
	}, indices) {
		t.Fatal("values differ")
	}
}

// TestLazySliceFilterReturnsEmptyWhenNoneMatch verifies that Filter returns an
// empty sequence when no items satisfy the predicate.
func TestLazySliceFilterReturnsEmptyWhenNoneMatch(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 3, 5,
	}))

	got := lazy.Filter(func(i int, v int) bool {
		return v%2 == 0
	}).Items()

	if len(got) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceFilterReturnsEmptyOnEmptyInput verifies that Filter on an empty
// source yields nothing.
func TestLazySliceFilterReturnsEmptyOnEmptyInput(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	got := lazy.Filter(func(i int, v int) bool {
		return true
	}).Items()

	if len(got) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceRejectReturnsItemsNotMatchingPredicate verifies that Reject keeps
// only the items for which the predicate returns false.
func TestLazySliceRejectReturnsItemsNotMatchingPredicate(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	got := lazy.Reject(func(i int, v int) bool {
		return v%2 == 0
	}).Items()

	if !reflect.DeepEqual([]int{
		1, 3, 5,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceMapTransformsItemsToSameType verifies that Map applies the function
// to each item and collects the results.
func TestLazySliceMapTransformsItemsToSameType(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	got := lazy.Map(func(i int, v int) int {
		return v * 10
	}).Items()

	if !reflect.DeepEqual([]int{
		10, 20, 30,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceMapTransformsItemsToDifferentType verifies that Map can produce a
// lazy of a different element type (int → bool).
func TestLazySliceMapTransformsItemsToDifferentType(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		2, 3, 4,
	}))

	got := lazy.Map(func(i int, v int) bool {
		return v%2 == 0
	}).Items()

	if !reflect.DeepEqual([]bool{
		true, false, true,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceFlatMapFlattensMappedResults verifies that FlatMap yields all mapped items in order.
func TestLazySliceFlatMapFlattensMappedResults(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	got := lazy.FlatMap(func(i int, v int) []int {
		return []int{
			v, v * 10,
		}
	}).Items()

	if !reflect.DeepEqual([]int{
		1, 10, 2, 20, 3, 30,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceFlatMapYieldsDenseOutputIndices verifies that FlatMap reindexes output.
func TestLazySliceFlatMapYieldsDenseOutputIndices(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))
	var indices []int

	for index := range lazy.FlatMap(func(_ int, value int) []int {
		return []int{
			value, value * 10,
		}
	}) {
		indices = append(indices, index)
	}

	if !reflect.DeepEqual([]int{
		0, 1, 2, 3, 4, 5,
	}, indices) {
		t.Fatal("values differ")
	}
}

// TestLazySliceFlatMapIsLazy verifies that FlatMap does not consume the source until needed.
func TestLazySliceFlatMapIsLazy(t *testing.T) {
	t.Parallel()

	called := 0
	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	flatMapped := lazy.FlatMap(func(i int, v int) []int {
		called++

		return []int{
			v,
		}
	})

	if !reflect.DeepEqual(0, called) {
		t.Fatal("values differ")
	}

	_ = flatMapped.Items()

	if !reflect.DeepEqual(3, called) {
		t.Fatal("values differ")
	}
}

// TestLazySliceKeyByIndexesItemsByDerivedKey verifies that KeyBy materializes a lookup map.
func TestLazySliceKeyByIndexesItemsByDerivedKey(t *testing.T) {
	t.Parallel()

	type user struct {
		// ID is the stable key used to index each fixture. Distinct names
		// make the associated values easy to distinguish in assertions.
		ID int

		// Name identifies the fixture value retained under its ID. It lets
		// the assertion verify the whole item, not only the derived key.
		Name string
	}

	lazy := collection.NewLazySlice(slices.All([]user{
		{
			ID: 1, Name: "alice",
		}, {
			ID: 2, Name: "bob",
		},
	}))

	result := lazy.KeyBy(func(i int, v user) int {
		return v.ID
	})

	if !reflect.DeepEqual(map[int]user{
		1: {
			ID: 1, Name: "alice",
		},
		2: {
			ID: 2, Name: "bob",
		},
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceCountByCountsItemsPerDerivedKey verifies that CountBy groups counts by key.
func TestLazySliceCountByCountsItemsPerDerivedKey(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]string{
		"ant", "ape", "bat", "bee",
	}))

	result := lazy.CountBy(func(i int, v string) byte {
		return v[0]
	})

	if !reflect.DeepEqual(map[byte]int{
		'a': 2, 'b': 2,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceTakeUntilStopsBeforeMatchingItem verifies that TakeUntil excludes the matching item.
func TestLazySliceTakeUntilStopsBeforeMatchingItem(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	got := lazy.TakeUntil(func(i int, v int) bool {
		return v >= 4
	}).Items()

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSkipUntilStartsAtFirstMatchingItem verifies that SkipUntil yields from the first match onward.
func TestLazySliceSkipUntilStartsAtFirstMatchingItem(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	got := lazy.SkipUntil(func(i int, v int) bool {
		return v >= 4
	}).Items()

	if !reflect.DeepEqual([]int{
		4, 5,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceMapReturnsEmptyOnEmptyInput verifies that Map on an empty source
// yields an empty lazy.
func TestLazySliceMapReturnsEmptyOnEmptyInput(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	got := lazy.Map(func(i int, v int) int {
		return v
	}).Items()

	if len(got) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceFirstWhereReturnsFirstMatch verifies that FirstWhere returns the
// first item satisfying the predicate with ok=true.
func TestLazySliceFirstWhereReturnsFirstMatch(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4,
	}))

	result, ok := lazy.FirstWhere(func(i int, v int) bool {
		return v > 2
	})

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(3, result) {
		t.Fatal("values differ")
	}
}

// TestLazySliceFirstWhereReturnsNotFoundWhenNoMatch verifies that FirstWhere
// returns ok=false when no item satisfies the predicate.
func TestLazySliceFirstWhereReturnsNotFoundWhenNoMatch(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	_, ok := lazy.FirstWhere(func(i int, v int) bool {
		return v > 10
	})

	if ok {
		t.Fatal("expected false")
	}
}

// TestLazySliceFirstWhereReturnsNotFoundOnEmpty verifies that FirstWhere returns
// ok=false when the sequence is empty.
func TestLazySliceFirstWhereReturnsNotFoundOnEmpty(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	_, ok := lazy.FirstWhere(func(i int, v int) bool {
		return true
	})

	if ok {
		t.Fatal("expected false")
	}
}

// TestLazySliceFirstReturnsFirstItemWithOkTrue verifies that First returns the
// first item and ok=true for a non-empty sequence.
func TestLazySliceFirstReturnsFirstItemWithOkTrue(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		42, 99,
	}))

	result, ok := lazy.First()

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(42, result) {
		t.Fatal("values differ")
	}
}

// TestLazySliceFirstReturnsOkFalseOnEmpty verifies that First returns ok=false
// when the sequence is empty.
func TestLazySliceFirstReturnsOkFalseOnEmpty(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	_, ok := lazy.First()

	if ok {
		t.Fatal("expected false")
	}
}

// TestLazySliceLastReturnsLastItemWithOkTrue verifies that Last returns the final
// item and ok=true for a non-empty sequence.
func TestLazySliceLastReturnsLastItemWithOkTrue(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 99,
	}))

	result, ok := lazy.Last()

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(99, result) {
		t.Fatal("values differ")
	}
}

// TestLazySliceLastReturnsOkFalseOnEmpty verifies that Last returns ok=false when
// the sequence is empty.
func TestLazySliceLastReturnsOkFalseOnEmpty(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	_, ok := lazy.Last()

	if ok {
		t.Fatal("expected false")
	}
}

// TestLazySliceContainsTrueWhenFound verifies that Contains returns true when an
// item satisfying the predicate exists.
func TestLazySliceContainsTrueWhenFound(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	if !lazy.Contains(func(i int, v int) bool {
		return v == 2
	}) {
		t.Fatal("expected true")
	}
}

// TestLazySliceContainsFalseWhenNotFound verifies that Contains returns false when
// no item satisfies the predicate.
func TestLazySliceContainsFalseWhenNotFound(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	if lazy.Contains(func(i int, v int) bool {
		return v == 99
	}) {
		t.Fatal("expected false")
	}
}

// TestLazySliceTakeYieldsAtMostNItems verifies that Take limits output to n items.
func TestLazySliceTakeYieldsAtMostNItems(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, lazy.Take(3).Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceTakeZeroLimitYieldsNothing verifies that Take(0) yields an empty
// sequence.
func TestLazySliceTakeZeroLimitYieldsNothing(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	if len(lazy.Take(0).Items()) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceTakeNegativeLimitYieldsNothing verifies that Take with a negative
// limit yields an empty sequence.
func TestLazySliceTakeNegativeLimitYieldsNothing(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	if len(lazy.Take(-1).Items()) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceTakeLimitExceedingLengthYieldsAll verifies that Take yields all
// items when the limit exceeds the sequence length.
func TestLazySliceTakeLimitExceedingLengthYieldsAll(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2,
	}))

	if !reflect.DeepEqual([]int{
		1, 2,
	}, lazy.Take(100).Items()) {
		t.Fatal("values differ")
	}
}

// newInfiniteLazySlice returns an increasing sequence and records yielded items.
func newInfiniteLazySlice(generated *int) collection.LazySlice[int] {
	return collection.NewLazySlice(func(yield func(int, int) bool) {
		yieldIncreasingIntegers(generated, yield)
	})
}

// yieldIncreasingIntegers yields each non-negative integer until yield stops it.
func yieldIncreasingIntegers(generated *int, yield func(int, int) bool) {
	for index := 0; ; index++ {
		*generated++

		if !yield(index, index) {
			return
		}
	}
}

// TestLazySliceTakeWithInfiniteSourceYieldsExactlyNItems verifies that Take stops
// early when combined with an infinite generator, demonstrating lazy evaluation.
func TestLazySliceTakeWithInfiniteSourceYieldsExactlyNItems(t *testing.T) {
	t.Parallel()

	generated := 0
	infinite := newInfiniteLazySlice(&generated)

	got := infinite.Take(3).Items()

	if !reflect.DeepEqual([]int{
		0, 1, 2,
	}, got) {
		t.Fatal("values differ")
	}

	if !reflect.DeepEqual(3, generated) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSkipSkipsFirstNItems verifies that Skip omits the first n items.
func TestLazySliceSkipSkipsFirstNItems(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	if !reflect.DeepEqual([]int{
		4, 5,
	}, lazy.Skip(3).Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSkipZeroSkipsNothing verifies that Skip(0) returns all items.
func TestLazySliceSkipZeroSkipsNothing(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, lazy.Skip(0).Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSkipExceedingLengthYieldsEmpty verifies that skipping more than
// the total count yields an empty sequence.
func TestLazySliceSkipExceedingLengthYieldsEmpty(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2,
	}))

	if len(lazy.Skip(10).Items()) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceTakeWhileYieldsItemsUntilPredicateReturnsFalse verifies that
// TakeWhile stops as soon as the predicate fails.
func TestLazySliceTakeWhileYieldsItemsUntilPredicateReturnsFalse(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	got := lazy.TakeWhile(func(i int, v int) bool {
		return v < 4
	}).Items()

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceTakeWhileYieldsNothingWhenFirstItemFailsPredicate verifies that
// TakeWhile yields nothing when the very first item fails.
func TestLazySliceTakeWhileYieldsNothingWhenFirstItemFailsPredicate(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		5, 1, 2,
	}))

	got := lazy.TakeWhile(func(i int, v int) bool {
		return v < 4
	}).Items()

	if len(got) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceTakeWhileYieldsAllWhenPredicateAlwaysTrue verifies that TakeWhile
// yields all items when the predicate never fails.
func TestLazySliceTakeWhileYieldsAllWhenPredicateAlwaysTrue(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	got := lazy.TakeWhile(func(i int, v int) bool {
		return true
	}).Items()

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSkipWhileSkipsLeadingItemsMatchingPredicateThenYieldsRest verifies
// that SkipWhile skips the leading run and returns the rest.
func TestLazySliceSkipWhileSkipsLeadingItemsMatchingPredicateThenYieldsRest(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	got := lazy.SkipWhile(func(i int, v int) bool {
		return v < 4
	}).Items()

	if !reflect.DeepEqual([]int{
		4, 5,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSkipWhileYieldsAllWhenFirstItemFailsPredicate verifies that
// SkipWhile yields all items when the first one fails the predicate.
func TestLazySliceSkipWhileYieldsAllWhenFirstItemFailsPredicate(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		5, 1, 2,
	}))

	got := lazy.SkipWhile(func(i int, v int) bool {
		return v < 4
	}).Items()

	if !reflect.DeepEqual([]int{
		5, 1, 2,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSkipWhileYieldsNothingWhenPredicateAlwaysTrue verifies that
// SkipWhile yields nothing when the predicate holds for every item.
func TestLazySliceSkipWhileYieldsNothingWhenPredicateAlwaysTrue(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	got := lazy.SkipWhile(func(i int, v int) bool {
		return true
	}).Items()

	if len(got) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceChunkSplitsIntoChunksOfGivenSize verifies that Chunk partitions the
// sequence into consecutive slices, with the last chunk potentially smaller.
func TestLazySliceChunkSplitsIntoChunksOfGivenSize(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	var chunks [][]int

	for _, chunk := range lazy.Chunk(2) {
		chunks = append(chunks, slices.Clone(chunk))
	}

	if !reflect.DeepEqual([][]int{
		{
			1, 2,
		}, {
			3, 4,
		}, {
			5,
		},
	}, chunks) {
		t.Fatal("values differ")
	}
}

// TestLazySliceChunkYieldsDenseOutputIndices verifies that Chunk reindexes output.
func TestLazySliceChunkYieldsDenseOutputIndices(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))
	var indices []int

	for index := range lazy.Chunk(2) {
		indices = append(indices, index)
	}

	if !reflect.DeepEqual([]int{
		0, 1, 2,
	}, indices) {
		t.Fatal("values differ")
	}
}

// TestLazySliceChunkOnEmptyLazyYieldsNoChunks verifies that Chunk over an empty
// sequence produces no chunks.
func TestLazySliceChunkOnEmptyLazyYieldsNoChunks(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	var chunks [][]int

	for _, chunk := range lazy.Chunk(3) {
		chunks = append(chunks, slices.Clone(chunk))
	}

	if len(chunks) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceChunkPanicsOnZeroSize verifies that Chunk panics when size is zero.
func TestLazySliceChunkPanicsOnZeroSize(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		lazy.Chunk(0)
	}()
}

// TestLazySliceChunkPanicsOnNegativeSize verifies that Chunk panics when size is
// negative.
func TestLazySliceChunkPanicsOnNegativeSize(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		lazy.Chunk(-1)
	}()
}

// TestLazySliceSlidingReturnsWindowsWithDefaultStepOfOne verifies that Sliding
// with no step argument advances by one between windows.
func TestLazySliceSlidingReturnsWindowsWithDefaultStepOfOne(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4,
	}))

	var windows [][]int

	for _, w := range lazy.Sliding(2) {
		windows = append(windows, slices.Clone(w))
	}

	if !reflect.DeepEqual([][]int{
		{
			1, 2,
		}, {
			2, 3,
		}, {
			3, 4,
		},
	}, windows) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSlidingYieldsDenseOutputIndices verifies that Sliding reindexes output.
func TestLazySliceSlidingYieldsDenseOutputIndices(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4,
	}))
	var indices []int

	for index := range lazy.Sliding(2) {
		indices = append(indices, index)
	}

	if !reflect.DeepEqual([]int{
		0, 1, 2,
	}, indices) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSlidingReturnsWindowsWithExplicitStep verifies that Sliding with an
// explicit step advances by that many positions between windows.
func TestLazySliceSlidingReturnsWindowsWithExplicitStep(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	var windows [][]int

	for _, w := range lazy.Sliding(3, 2) {
		windows = append(windows, slices.Clone(w))
	}

	if !reflect.DeepEqual([][]int{
		{
			1, 2, 3,
		}, {
			3, 4, 5,
		},
	}, windows) {
		t.Fatal("values differ")
	}
}

// TestLazySliceSlidingReturnsNothingWhenCollectionSmallerThanWindowSize verifies
// that Sliding yields nothing when the source has fewer items than the window.
func TestLazySliceSlidingReturnsNothingWhenCollectionSmallerThanWindowSize(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2,
	}))

	var windows [][]int

	for _, w := range lazy.Sliding(5) {
		windows = append(windows, slices.Clone(w))
	}

	if len(windows) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceSlidingPanicsOnZeroSize verifies that Sliding panics when size is
// zero.
func TestLazySliceSlidingPanicsOnZeroSize(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		lazy.Sliding(0)
	}()
}

// TestLazySliceSlidingPanicsOnNegativeSize verifies that Sliding panics when size
// is negative.
func TestLazySliceSlidingPanicsOnNegativeSize(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		lazy.Sliding(-1)
	}()
}

// TestLazySliceSlidingPanicsOnZeroStep verifies that Sliding panics when step is
// zero.
func TestLazySliceSlidingPanicsOnZeroStep(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		lazy.Sliding(2, 0)
	}()
}

// TestLazySliceSlidingPanicsOnNegativeStep verifies that Sliding panics when step
// is negative.
func TestLazySliceSlidingPanicsOnNegativeStep(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		lazy.Sliding(2, -1)
	}()
}

// TestLazySliceReduceAccumulatesValuesWithSameType verifies that Reduce computes
// the correct sum over a sequence of integers.
func TestLazySliceReduceAccumulatesValuesWithSameType(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4,
	}))

	sum := lazy.Reduce(func(acc int, i int, v int) int {
		return acc + v
	}, 0)

	if !reflect.DeepEqual(10, sum) {
		t.Fatal("values differ")
	}
}

// TestLazySliceReduceAccumulatesWithDifferentKType verifies that Reduce can use a
// different accumulator type than the element type (int32 → int64).
func TestLazySliceReduceAccumulatesWithDifferentKType(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int32{
		1, 2, 3,
	}))

	sum := lazy.Reduce(func(acc int64, i int, v int32) int64 {
		return acc + int64(v)
	}, int64(0))

	if !reflect.DeepEqual(int64(6), sum) {
		t.Fatal("values differ")
	}
}

// TestLazySliceReduceReturnsInitialOnEmpty verifies that Reduce returns the initial
// value unchanged when the sequence is empty.
func TestLazySliceReduceReturnsInitialOnEmpty(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	result := lazy.Reduce(func(acc int, i int, v int) int {
		return acc + v
	}, 42)

	if !reflect.DeepEqual(42, result) {
		t.Fatal("values differ")
	}
}

// TestLazySliceReverseReturnsItemsInReversedOrder verifies that Reverse returns a
// lazy that yields items in reverse order.
func TestLazySliceReverseReturnsItemsInReversedOrder(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	if !reflect.DeepEqual([]int{
		3, 2, 1,
	}, lazy.Reverse().Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceReverseReturnsEmptyOnEmptyInput verifies that Reverse on an empty
// source yields an empty sequence.
func TestLazySliceReverseReturnsEmptyOnEmptyInput(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	if len(lazy.Reverse().Items()) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceSortReturnsItemsInSortedOrder verifies that Sort orders items using
// the provided comparison function.
func TestLazySliceSortReturnsItemsInSortedOrder(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		3, 1, 4, 1, 5, 9,
	}))

	got := lazy.Sort(cmp.Compare[int]).Items()

	if !reflect.DeepEqual([]int{
		1, 1, 3, 4, 5, 9,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceUniqueRemovesDuplicatesAndPreservesFirstOccurrence verifies that
// Unique keeps only the first occurrence of each distinct key.
func TestLazySliceUniqueRemovesDuplicatesAndPreservesFirstOccurrence(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 2, 3, 1, 4,
	}))

	got := lazy.Unique(func(i int, v int) int {
		return v
	}).Items()

	if !reflect.DeepEqual([]int{
		1, 2, 3, 4,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySlicePartitionSplitsIntoMatchingAndNonMatching verifies that Partition
// correctly separates items into two groups.
func TestLazySlicePartitionSplitsIntoMatchingAndNonMatching(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5,
	}))

	even, odd := lazy.Partition(func(i int, v int) bool {
		return v%2 == 0
	})

	if !reflect.DeepEqual([]int{
		2, 4,
	}, even.Items()) {
		t.Fatal("values differ")
	}

	if !reflect.DeepEqual([]int{
		1, 3, 5,
	}, odd.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySlicePartitionAllMatchSecondResultYieldsNothing verifies that when every
// item matches, the second (non-matching) lazy is empty.
func TestLazySlicePartitionAllMatchSecondResultYieldsNothing(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		2, 4, 6,
	}))

	_, odd := lazy.Partition(func(i int, v int) bool {
		return v%2 == 0
	})

	if len(odd.Items()) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySlicePartitionNoneMatchFirstResultYieldsNothing verifies that when no
// item matches, the first (matching) lazy is empty.
func TestLazySlicePartitionNoneMatchFirstResultYieldsNothing(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 3, 5,
	}))

	even, _ := lazy.Partition(func(i int, v int) bool {
		return v%2 == 0
	})

	if len(even.Items()) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceConcatYieldsAllItemsFromBothSources verifies that Concat appends the
// second lazy's items after the first's.
func TestLazySliceConcatYieldsAllItemsFromBothSources(t *testing.T) {
	t.Parallel()

	first := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))
	second := collection.NewLazySlice(slices.All([]int{
		4, 5, 6,
	}))

	got := first.Concat(second).Items()

	if !reflect.DeepEqual([]int{
		1, 2, 3, 4, 5, 6,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceConcatYieldsDenseOutputIndices verifies that Concat reindexes output.
func TestLazySliceConcatYieldsDenseOutputIndices(t *testing.T) {
	t.Parallel()

	first := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))
	second := collection.NewLazySlice(slices.All([]int{
		4, 5, 6,
	}))
	var indices []int

	for index := range first.Concat(second) {
		indices = append(indices, index)
	}

	if !reflect.DeepEqual([]int{
		0, 1, 2, 3, 4, 5,
	}, indices) {
		t.Fatal("values differ")
	}
}

// TestLazySliceGroupByGroupsAllItemsByKeyIntoEagerSubCollections verifies that
// GroupBy partitions items into map entries of eager Slices.
func TestLazySliceGroupByGroupsAllItemsByKeyIntoEagerSubCollections(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5, 6,
	}))

	groups := lazy.GroupBy(evenOddKey)

	if !reflect.DeepEqual(2, groups.Len()) {
		t.Fatal("values differ")
	}

	requireMapSliceValue(t, groups, "odd", []int{
		1, 3, 5,
	})

	requireMapSliceValue(t, groups, "even", []int{
		2, 4, 6,
	})
}

// TestLazySliceNthPicksEveryNthItemFromIndexZero verifies that Nth selects every
// nth item starting from index 0.
func TestLazySliceNthPicksEveryNthItemFromIndexZero(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		0, 1, 2, 3, 4, 5, 6,
	}))

	got := lazy.Nth(3).Items()

	if !reflect.DeepEqual([]int{
		0, 3, 6,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceNthYieldsDenseOutputIndices verifies that Nth reindexes output.
func TestLazySliceNthYieldsDenseOutputIndices(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		0, 1, 2, 3, 4, 5, 6,
	}))
	var indices []int

	for index := range lazy.Nth(3) {
		indices = append(indices, index)
	}

	if !reflect.DeepEqual([]int{
		0, 1, 2,
	}, indices) {
		t.Fatal("values differ")
	}
}

// TestLazySliceNthPicksEveryNthItemStartingAtGivenOffset verifies that Nth with an
// offset begins selection from that position.
func TestLazySliceNthPicksEveryNthItemStartingAtGivenOffset(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		0, 1, 2, 3, 4, 5, 6,
	}))

	got := lazy.Nth(3, 1).Items()

	if !reflect.DeepEqual([]int{
		1, 4,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceNthOffsetBeyondLengthReturnsEmptyLazySlice verifies that an offset
// larger than the sequence length yields nothing.
func TestLazySliceNthOffsetBeyondLengthReturnsEmptyLazySlice(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	got := lazy.Nth(2, 100).Items()

	if len(got) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceNthPanicsOnZeroN verifies that Nth panics when n is zero.
func TestLazySliceNthPanicsOnZeroN(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		lazy.Nth(0)
	}()
}

// TestLazySliceNthPanicsOnNegativeN verifies that Nth panics when n is negative.
func TestLazySliceNthPanicsOnNegativeN(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		lazy.Nth(-1)
	}()
}

// TestLazySliceForPageReturnsFirstPage verifies that ForPage returns the first
// page of items.
func TestLazySliceForPageReturnsFirstPage(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5, 6,
	}))

	got := lazy.ForPage(1, 2).Items()

	if !reflect.DeepEqual([]int{
		1, 2,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceForPageReturnsSecondPage verifies that ForPage returns the second
// page of items.
func TestLazySliceForPageReturnsSecondPage(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3, 4, 5, 6,
	}))

	got := lazy.ForPage(2, 2).Items()

	if !reflect.DeepEqual([]int{
		3, 4,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazySliceForPageBeyondDataReturnsEmptyLazySlice verifies that ForPage returns
// nothing when the requested page exceeds the available data.
func TestLazySliceForPageBeyondDataReturnsEmptyLazySlice(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	got := lazy.ForPage(10, 2).Items()

	if len(got) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazySliceMarshalJSONMarshalsNonEmptyLazyToJSONArray verifies that MarshalJSON
// encodes a non-empty lazy as a JSON array.
func TestLazySliceMarshalJSONMarshalsNonEmptyLazyToJSONArray(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		1, 2, 3,
	}))

	data, err := lazy.MarshalJSON()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requireSameJSON(t, `[1,2,3]`, string(data))
}

// TestLazySliceMarshalJSONMarshalsEmptyLazyToNull verifies that MarshalJSON on an
// empty lazy produces a null or empty-array JSON value with no error.
func TestLazySliceMarshalJSONMarshalsEmptyLazyToNull(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{}))

	data, err := lazy.MarshalJSON()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Round-trip: the encoded output must be valid JSON.
	var decoded []int

	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestLazySliceUnmarshalJSONDecodesJSONArrayAndYieldsItems verifies that
// UnmarshalJSON populates the lazy so that it yields the decoded items.
func TestLazySliceUnmarshalJSONDecodesJSONArrayAndYieldsItems(t *testing.T) {
	t.Parallel()

	var lazy collection.LazySlice[int]

	if err := lazy.UnmarshalJSON([]byte(`[10,20,30]`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		10, 20, 30,
	}, lazy.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceUnmarshalJSONReturnsErrorOnInvalidJSON verifies that UnmarshalJSON
// returns an error when given malformed JSON.
func TestLazySliceUnmarshalJSONReturnsErrorOnInvalidJSON(t *testing.T) {
	t.Parallel()

	var lazy collection.LazySlice[int]

	if lazy.UnmarshalJSON([]byte(`not-json`)) == nil {
		t.Fatal("expected an error")
	}
}

// TestLazySliceMarshalJSONToEncodesNonEmptyLazyToJSONArray verifies that
// MarshalJSONTo writes a valid JSON array to the encoder.
func TestLazySliceMarshalJSONToEncodesNonEmptyLazyToJSONArray(t *testing.T) {
	t.Parallel()

	lazy := collection.NewLazySlice(slices.All([]int{
		4, 5, 6,
	}))

	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)

	if err := lazy.MarshalJSONTo(enc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requireSameJSON(t, `[4,5,6]`, buf.String())
}

// TestLazySliceUnmarshalJSONFromDecodesJSONArrayAndYieldsItems verifies that
// UnmarshalJSONFrom populates the lazy from a JSON array.
func TestLazySliceUnmarshalJSONFromDecodesJSONArrayAndYieldsItems(t *testing.T) {
	t.Parallel()

	dec := jsontext.NewDecoder(strings.NewReader(`[7,8,9]`))

	var lazy collection.LazySlice[int]

	if err := lazy.UnmarshalJSONFrom(dec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		7, 8, 9,
	}, lazy.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazySliceUnmarshalJSONFromReturnsErrorOnInvalidJSON verifies that
// UnmarshalJSONFrom returns an error when the decoder contains invalid JSON.
func TestLazySliceUnmarshalJSONFromReturnsErrorOnInvalidJSON(t *testing.T) {
	t.Parallel()

	dec := jsontext.NewDecoder(strings.NewReader(`not-json`))

	var lazy collection.LazySlice[int]

	if lazy.UnmarshalJSONFrom(dec) == nil {
		t.Fatal("expected an error")
	}
}

// TestLazySliceSlidingStepLargerThanSize verifies that a step wider than the
// window skips intervening items without yielding an incomplete final window.
func TestLazySliceSlidingStepLargerThanSize(t *testing.T) {
	t.Parallel()

	lazy := collection.NewSlice([]int{1, 2, 3, 4, 5, 6, 7}).Lazy()
	var windows [][]int

	for _, window := range lazy.Sliding(2, 3) {
		windows = append(windows, window)
	}

	if !reflect.DeepEqual(windows, [][]int{{1, 2}, {4, 5}}) {
		t.Fatalf("windows = %v", windows)
	}
}
