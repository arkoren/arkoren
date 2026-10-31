package collection_test

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/foundation/collection"
)

// requireSameElements fails when expected and actual have different elements.
func requireSameElements[T comparable](t *testing.T, expected, actual []T) {
	t.Helper()

	if len(expected) != len(actual) {
		t.Fatal("values differ")
	}

	if !haveSameElements(expected, actual) {
		t.Fatal("values differ")
	}
}

// haveSameElements reports whether expected and actual have equal element counts.
func haveSameElements[T comparable](expected, actual []T) bool {
	counts := elementCounts(expected)

	for _, item := range actual {
		counts[item]--
	}

	return elementCountsAreZero(counts)
}

// elementCounts returns the multiplicity of each item.
func elementCounts[T comparable](items []T) map[T]int {
	counts := make(map[T]int, len(items))

	for _, item := range items {
		counts[item]++
	}

	return counts
}

// elementCountsAreZero reports whether all item multiplicities are zero.
func elementCountsAreZero[T comparable](counts map[T]int) bool {
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}

	return true
}

// requireSameJSON fails when expected and actual encode different JSON values.
func requireSameJSON(t *testing.T, expected, actual string) {
	t.Helper()

	if !reflect.DeepEqual(jsonValue(t, expected), jsonValue(t, actual)) {
		t.Fatalf("expected JSON %s, got %s", expected, actual)
	}
}

// jsonValue decodes JSON source into its generic representation.
func jsonValue(t *testing.T, source string) any {
	t.Helper()

	var value any

	if err := json.Unmarshal([]byte(source), &value); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	return value
}

// requireNoError fails when err is non-nil.
func requireNoError(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// requireErrorIs fails when err does not match target.
func requireErrorIs(t *testing.T, err, target error) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("expected error %v to match %v", err, target)
	}
}

// requireSlicesEqual fails when expected and actual contain different slices.
func requireSlicesEqual[T any](t *testing.T, expected [][]T, actual []collection.Slice[T]) {
	t.Helper()

	if len(expected) != len(actual) {
		t.Fatalf("expected length %d, got %d", len(expected), len(actual))
	}

	for index, expectedItems := range expected {
		if !reflect.DeepEqual(expectedItems, actual[index].Items()) {
			t.Fatal("values differ")
		}
	}
}

// requireMapSliceValue fails when groups lacks key or its items differ from expected.
func requireMapSliceValue[K comparable, T any](t *testing.T, groups collection.Map[K, collection.Slice[T]], key K, expected []T) {
	t.Helper()

	actual, ok := groups.Get(key)

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(expected, actual.Items()) {
		t.Fatal("values differ")
	}
}

// evenOddKey returns the parity group for v.
func evenOddKey(_ int, v int) string {
	if v%2 == 0 {
		return "even"
	}

	return "odd"
}

// evenOddTryKey returns the parity group for v without an error.
func evenOddTryKey(_ int, v int) (string, error) {
	if v%2 == 0 {
		return "even", nil
	}

	return "odd", nil
}

// Example keeps the package's introductory filtering example executable.
func Example() {
	numbers := collection.NewSlice([]int{1, 2, 3})

	even := numbers.Filter(func(_ int, number int) bool {
		return number%2 == 0
	})

	fmt.Println(even.Items())
	// Output: [2]
}

// ExampleSlice shows that windows alias their input while Filter results do not.
func ExampleSlice() {
	raw := []int{1, 2, 3}
	items := collection.NewSlice(raw)

	selected := items.Filter(func(_ int, value int) bool {
		return value > 0
	})

	items.Take(1).Items()[0] = 9

	fmt.Println(raw, selected.Items())
	// Output: [9 2 3] [1 2 3]
}

// ExampleNewSliceAs demonstrates named slice conversion without losing storage.
func ExampleNewSliceAs() {
	type Numbers []int
	items := collection.NewSliceAs(Numbers{1, 2})
	plain := items.ItemsAs[Numbers]()
	plain[0] = 9

	fmt.Println(items.Items())
	// Output: [9 2]
}

// ExampleSlice_MarshalJSONTo exercises direct streaming encoding and the nil
// encoding default through the legacy-compatible method name.
func ExampleSlice_MarshalJSONTo() {
	var encoded bytes.Buffer

	err := collection.NewSlice([]int{1, 2}).MarshalJSONTo(jsontext.NewEncoder(&encoded))

	if err != nil {
		panic(err)
	}

	fmt.Println(encoded.Len() > 0)

	data, err := collection.NewSlice[int](nil).MarshalJSON()

	if err != nil {
		panic(err)
	}

	fmt.Println(string(data))
	// Output:
	// true
	// []
}

// TestStrideSliceSlidingMaxIntStep verifies that advancing beyond the last
// complete window cannot wrap around, and that eager windows still alias input.
func TestStrideSliceSlidingMaxIntStep(t *testing.T) {
	t.Parallel()

	source := collection.NewSlice([]int{1, 2, 3})

	windows := source.Sliding(1, math.MaxInt)

	if len(windows) != 1 || !slices.Equal(windows[0].Items(), []int{1}) {
		t.Fatalf("windows = %v, want [[1]]", windows)
	}

	windows[0][0] = 9

	if source[0] != 9 {
		t.Fatal("eager window no longer shares source storage")
	}
}

// TestStrideSliceNthMaxIntStep verifies that a positive offset cannot make
// the next stride wrap into a negative index. Selected storage stays independent.
func TestStrideSliceNthMaxIntStep(t *testing.T) {
	t.Parallel()

	source := collection.NewSlice([]int{1, 2, 3})

	selected := source.Nth(math.MaxInt, 1)

	if !slices.Equal(selected.Items(), []int{2}) {
		t.Fatalf("selected = %v, want [2]", selected)
	}

	selected[0] = 9

	if source[1] != 2 {
		t.Fatal("selected storage aliases source")
	}
}

// TestStrideSliceNthClampsNegativeOffsets checks the eager policy at both
// ordinary and minimum-int offsets without depending on private normalization.
func TestStrideSliceNthClampsNegativeOffsets(t *testing.T) {
	t.Parallel()

	source := collection.NewSlice([]int{1, 2, 3, 4, 5})

	for _, offset := range []int{-1, math.MinInt} {
		if got := source.Nth(2, offset).Items(); !slices.Equal(got, []int{1, 3, 5}) {
			t.Fatalf("Nth(2, %d) = %v, want [1 3 5]", offset, got)
		}
	}
}

// TestStrideNthMaxIntOffset checks that a starting position beyond a finite
// source yields no values in any form, even when the step is also MaxInt.
func TestStrideNthMaxIntOffset(t *testing.T) {
	t.Parallel()

	source := collection.NewSlice([]int{1, 2, 3})

	if got := source.Nth(math.MaxInt, math.MaxInt); len(got) != 0 {
		t.Fatalf("eager selection = %v, want empty", got)
	}

	if got := source.Lazy().Nth(math.MaxInt, math.MaxInt).Items(); len(got) != 0 {
		t.Fatalf("lazy selection = %v, want empty", got)
	}

	got, err := source.Try().Nth(math.MaxInt, math.MaxInt).Items()

	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 0 {
		t.Fatalf("fallible selection = %v, want empty", got)
	}
}

// TestStrideNthMinIntOffsetWithMaxIntStep verifies that clamping happens
// before selection arithmetic, even for the full signed-int input range.
func TestStrideNthMinIntOffsetWithMaxIntStep(t *testing.T) {
	t.Parallel()

	source := collection.NewSlice([]int{1, 2, 3})

	if got := source.Nth(math.MaxInt, math.MinInt).Items(); !slices.Equal(got, []int{1}) {
		t.Fatalf("eager selection = %v, want [1]", got)
	}

	if got := source.Lazy().Nth(math.MaxInt, math.MinInt).Items(); !slices.Equal(got, []int{1}) {
		t.Fatalf("lazy selection = %v, want [1]", got)
	}

	got, err := source.Try().Nth(math.MaxInt, math.MinInt).Items()

	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got, []int{1}) {
		t.Fatalf("fallible selection = %v, want [1]", got)
	}
}

// TestSliceFilterReturnsNilWithoutMatches checks the representation of a newly
// owned empty result, without changing constructor or window aliasing semantics.
func TestSliceFilterReturnsNilWithoutMatches(t *testing.T) {
	t.Parallel()

	items := collection.NewSlice([]int{1, 3}).Filter(func(_ int, value int) bool {
		return value%2 == 0
	}).Items()

	if items != nil {
		t.Fatalf("items = %#v, want nil", items)
	}
}

// TestSlicePartitionReturnsNilForUnusedSide checks that each independently
// owned partition starts nil and allocates only when it receives an item.
func TestSlicePartitionReturnsNilForUnusedSide(t *testing.T) {
	t.Parallel()

	matching, rest := collection.NewSlice([]int{2, 4}).Partition(func(_ int, value int) bool {
		return value%2 == 0
	})

	if matching.Len() != 2 || rest.Items() != nil {
		t.Fatalf("partitions = %#v, %#v, want [2 4], nil", matching, rest)
	}
}

// TestSliceUniqueReturnsNilForEmptyInput checks that empty deduplication keeps
// the standard nil result without evaluating a projection.
func TestSliceUniqueReturnsNilForEmptyInput(t *testing.T) {
	t.Parallel()

	items := collection.NewSlice([]int{}).Unique(func(_ int, value int) int {
		t.Fatal("projection invoked for empty input")

		return value
	}).Items()

	if items != nil {
		t.Fatalf("items = %#v, want nil", items)
	}
}

// TestSliceTakeWhileReturnsNilForRejectedFirstItem checks that an empty prefix
// has no backing storage when the first predicate result stops selection.
func TestSliceTakeWhileReturnsNilForRejectedFirstItem(t *testing.T) {
	t.Parallel()

	items := collection.NewSlice([]int{1, 2}).TakeWhile(func(_ int, value int) bool {
		return value < 1
	}).Items()

	if items != nil {
		t.Fatalf("items = %#v, want nil", items)
	}
}

// TestSliceFlatMapReturnsNilWithoutOutput checks that empty expansions leave
// the independently owned output nil even when the source contains values.
func TestSliceFlatMapReturnsNilWithoutOutput(t *testing.T) {
	t.Parallel()

	items := collection.NewSlice([]int{1, 2}).FlatMap(func(_ int, _ int) []string {
		return nil
	}).Items()

	if items != nil {
		t.Fatalf("items = %#v, want nil", items)
	}
}

// TestSliceMapReturnsNilForEmptyInput checks that a type-changing operation
// does not allocate a result or invoke its callback when no values exist.
func TestSliceMapReturnsNilForEmptyInput(t *testing.T) {
	t.Parallel()

	items := collection.NewSlice([]int{}).Map(func(_ int, value int) string {
		t.Fatal("callback invoked for empty input")

		return "unexpected"
	}).Items()

	if items != nil {
		t.Fatalf("items = %#v, want nil", items)
	}
}

// TestNewSliceCreatesSliceFromSlice verifies that NewSlice stores the given items.
func TestNewSliceCreatesSliceFromSlice(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, c.Items()) {
		t.Fatal("values differ")
	}
}

// TestNewSliceAsAcceptsNamedSliceType verifies that NewSliceAs accepts named
// slice types whose underlying type is []T.
func TestNewSliceAsAcceptsNamedSliceType(t *testing.T) {
	t.Parallel()

	type numbers []int

	c := collection.NewSliceAs(numbers{
		1, 2, 3,
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, c.Items()) {
		t.Fatal("values differ")
	}
}

// TestNewSliceCreatesSliceFromPlainSlice verifies that NewSlice stores the
// given plain slice.
func TestNewSliceCreatesSliceFromPlainSlice(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, c.Items()) {
		t.Fatal("values differ")
	}
}

// TestNewSliceCreatesEmptySliceFromEmptySlice verifies that NewSlice with an empty
// (non-nil) slice produces an empty collection.
func TestNewSliceCreatesEmptySliceFromEmptySlice(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{})

	if !c.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestNewSliceCreatesEmptySliceFromNilSlice verifies that NewSlice with a nil slice
// produces an empty collection.
func TestNewSliceCreatesEmptySliceFromNilSlice(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	if !c.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestIterYieldsAllItemsInOrder verifies that Iter produces every item in
// insertion order.
func TestIterYieldsAllItemsInOrder(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		10, 20, 30,
	})

	var got []int

	for _, v := range c.Iter() {
		got = append(got, v)
	}

	if !reflect.DeepEqual([]int{
		10, 20, 30,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestIterYieldsNothingOnEmptyCollection verifies that Iter over an empty
// collection never calls the yield function.
func TestIterYieldsNothingOnEmptyCollection(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	var got []int

	for _, v := range c.Iter() {
		got = append(got, v)
	}

	if len(got) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazyContainsSameItemsAsCollection verifies that the LazySlice returned by
// Lazy() materializes the same items as the original collection.
func TestLazyContainsSameItemsAsCollection(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]string{
		"a", "b", "c",
	})

	if !reflect.DeepEqual(c.Items(), c.Lazy().Items()) {
		t.Fatal("values differ")
	}
}

// TestSliceReturnsUnderlyingItems verifies that Items returns the items passed
// to NewSlice.
func TestSliceReturnsUnderlyingItems(t *testing.T) {
	t.Parallel()

	items := []int{
		7, 8, 9,
	}

	c := collection.NewSlice(items)

	if !reflect.DeepEqual(items, c.Items()) {
		t.Fatal("values differ")
	}
}

// TestIsEmptyTrueForEmptyCollection verifies that IsEmpty returns true when the
// collection has no items.
func TestIsEmptyTrueForEmptyCollection(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	if !c.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestIsEmptyFalseForNonEmptyCollection verifies that IsEmpty returns false when
// the collection has items.
func TestIsEmptyFalseForNonEmptyCollection(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1,
	})

	if c.IsEmpty() {
		t.Fatal("expected false")
	}
}

// TestIsNotEmptyTrueForNonEmptyCollection verifies that IsNotEmpty returns true
// when the collection has items.
func TestIsNotEmptyTrueForNonEmptyCollection(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2,
	})

	if !c.IsNotEmpty() {
		t.Fatal("expected true")
	}
}

// TestIsNotEmptyFalseForEmptyCollection verifies that IsNotEmpty returns false
// when the collection has no items.
func TestIsNotEmptyFalseForEmptyCollection(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	if c.IsNotEmpty() {
		t.Fatal("expected false")
	}
}

// TestLenReturnsItemCount verifies that Len returns the number of items.
func TestLenReturnsItemCount(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4,
	})

	if !reflect.DeepEqual(4, c.Len()) {
		t.Fatal("values differ")
	}
}

// TestLenReturnsZeroForEmptyCollection verifies that Len returns zero for an
// empty collection.
func TestLenReturnsZeroForEmptyCollection(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	if !reflect.DeepEqual(0, c.Len()) {
		t.Fatal("values differ")
	}
}

// TestEveryTrueWhenAllItemsMatch verifies that Every returns true when the
// predicate holds for all items.
func TestEveryTrueWhenAllItemsMatch(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		2, 4, 6,
	})

	if !c.Every(func(i int, v int) bool {
		return v%2 == 0
	}) {
		t.Fatal("expected true")
	}
}

// TestEveryFalseWhenOneItemFails verifies that Every returns false when at
// least one item does not satisfy the predicate.
func TestEveryFalseWhenOneItemFails(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		2, 3, 6,
	})

	if c.Every(func(i int, v int) bool {
		return v%2 == 0
	}) {
		t.Fatal("expected false")
	}
}

// TestEveryTrueOnEmptyCollection verifies the vacuous truth: Every returns true
// when there are no items.
func TestEveryTrueOnEmptyCollection(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	if !c.Every(func(i int, v int) bool {
		return false
	}) {
		t.Fatal("expected true")
	}
}

// TestEachCallsFForEveryItem verifies that Each visits every item in order.
func TestEachCallsFForEveryItem(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	var got []int

	c.Each(func(i int, v int) {
		got = append(got, v)
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestEachDoesNotCallFOnEmpty verifies that Each never invokes f on an empty
// collection.
func TestEachDoesNotCallFOnEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	called := false

	c.Each(func(int, int) {
		called = true
	})

	if called {
		t.Fatal("expected false")
	}
}

// TestTapEachCallsFForEveryItem verifies that TapEach visits every item.
func TestTapEachCallsFForEveryItem(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		4, 5, 6,
	})

	var got []int

	c.TapEach(func(i int, v int) {
		got = append(got, v)
	})

	if !reflect.DeepEqual([]int{
		4, 5, 6,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestTapEachReturnsOriginalCollectionUnchanged verifies that TapEach returns
// the same items as the receiver.
func TestTapEachReturnsOriginalCollectionUnchanged(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		4, 5, 6,
	})

	result := c.TapEach(func(int, int) {
	})

	if !reflect.DeepEqual(c.Items(), result.Items()) {
		t.Fatal("values differ")
	}
}

// TestFilterReturnsMatchingItems verifies that Filter keeps only items for
// which the predicate is true.
func TestFilterReturnsMatchingItems(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	result := c.Filter(func(i int, v int) bool {
		return v%2 == 0
	})

	if !reflect.DeepEqual([]int{
		2, 4,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestFilterReturnsEmptyWhenNoneMatch verifies that Filter produces an empty
// collection when no items satisfy the predicate.
func TestFilterReturnsEmptyWhenNoneMatch(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 3, 5,
	})

	result := c.Filter(func(i int, v int) bool {
		return v%2 == 0
	})

	if !result.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestFilterReturnsEmptyOnEmptyInput verifies that Filter on an empty
// collection yields an empty collection.
func TestFilterReturnsEmptyOnEmptyInput(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	result := c.Filter(func(i int, v int) bool {
		return true
	})

	if !result.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestFlatMapFlattensMappedResults verifies that FlatMap maps each item to many
// items and flattens the results.
func TestFlatMapFlattensMappedResults(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	result := c.FlatMap(func(i int, v int) []int {
		return []int{
			v, v * 10,
		}
	})

	if !reflect.DeepEqual([]int{
		1, 10, 2, 20, 3, 30,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestFlatMapCanDropItems verifies that FlatMap supports zero-result mappings.
func TestFlatMapCanDropItems(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4,
	})

	result := c.FlatMap(func(i int, v int) []int {
		if v%2 == 0 {
			return nil
		}

		return []int{
			v,
		}
	})

	if !reflect.DeepEqual([]int{
		1, 3,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestKeyByIndexesItemsByDerivedKey verifies that KeyBy builds a lookup map from a slice.
func TestKeyByIndexesItemsByDerivedKey(t *testing.T) {
	t.Parallel()

	type user struct {
		// ID is the stable key used to index each fixture. Distinct names
		// make the associated values easy to distinguish in assertions.
		ID int

		// Name identifies the fixture value retained under its ID. It lets
		// the assertion verify the whole item, not only the derived key.
		Name string
	}

	c := collection.NewSlice([]user{
		{
			ID: 1, Name: "alice",
		}, {
			ID: 2, Name: "bob",
		},
	})

	result := c.KeyBy(func(i int, v user) int {
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

// TestKeyByLastItemWinsOnCollision verifies that KeyBy keeps the last item for duplicate keys.
func TestKeyByLastItemWinsOnCollision(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]string{
		"ant", "ape", "bat",
	})

	result := c.KeyBy(func(i int, v string) byte {
		return v[0]
	})

	if !reflect.DeepEqual(map[byte]string{
		'a': "ape", 'b': "bat",
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestCountByCountsItemsPerDerivedKey verifies that CountBy aggregates counts by key.
func TestCountByCountsItemsPerDerivedKey(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]string{
		"ant", "ape", "bat", "bee",
	})

	result := c.CountBy(func(i int, v string) byte {
		return v[0]
	})

	if !reflect.DeepEqual(map[byte]int{
		'a': 2, 'b': 2,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestTakeUntilStopsBeforeMatchingItem verifies that TakeUntil excludes the first matching item.
func TestTakeUntilStopsBeforeMatchingItem(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	result := c.TakeUntil(func(i int, v int) bool {
		return v >= 4
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestTakeUntilReturnsAllWhenNeverMatched verifies that TakeUntil returns the full slice when unmatched.
func TestTakeUntilReturnsAllWhenNeverMatched(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	result := c.TakeUntil(func(i int, v int) bool {
		return v > 10
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestSkipUntilStartsAtFirstMatchingItem verifies that SkipUntil keeps items from the first match onward.
func TestSkipUntilStartsAtFirstMatchingItem(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	result := c.SkipUntil(func(i int, v int) bool {
		return v >= 4
	})

	if !reflect.DeepEqual([]int{
		4, 5,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestSkipUntilReturnsEmptyWhenNeverMatched verifies that SkipUntil returns empty when no item matches.
func TestSkipUntilReturnsEmptyWhenNeverMatched(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	result := c.SkipUntil(func(i int, v int) bool {
		return v > 10
	})

	if !result.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestRejectReturnsOnlyNonMatchingItems verifies that Reject keeps only items
// for which the predicate is false.
func TestRejectReturnsOnlyNonMatchingItems(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	result := c.Reject(func(i int, v int) bool {
		return v%2 == 0
	})

	if !reflect.DeepEqual([]int{
		1, 3, 5,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapTransformsItemsToSameType verifies that Map applies f to every item
// and returns a collection of the same type.
func TestMapTransformsItemsToSameType(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	result := collection.Slice[int].Map(c, func(i int, v int) int {
		return v * 2
	})

	if !reflect.DeepEqual([]int{
		2, 4, 6,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapTransformsItemsToDifferentType verifies that Map can change the
// element type (int → bool).
func TestMapTransformsItemsToDifferentType(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4,
	})

	result := collection.Slice[int].Map(c, func(i int, v int) bool {
		return v%2 != 0
	})

	if !reflect.DeepEqual([]bool{
		true, false, true, false,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapReturnsEmptyCollectionOnEmptyInput verifies that Map on an empty
// collection produces an empty collection.
func TestMapReturnsEmptyCollectionOnEmptyInput(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	result := collection.Slice[int].Map(c, func(i int, v int) int {
		return v
	})

	if !result.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestFirstWhereReturnsFirstMatchingItem verifies that FirstWhere returns the
// first item satisfying f with ok=true.
func TestFirstWhereReturnsFirstMatchingItem(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4,
	})

	result, ok := c.FirstWhere(func(i int, v int) bool {
		return v > 2
	})

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(3, result) {
		t.Fatal("values differ")
	}
}

// TestFirstWhereReturnsOkFalseWhenNoMatch verifies that FirstWhere returns
// ok=false when no item satisfies f.
func TestFirstWhereReturnsOkFalseWhenNoMatch(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	_, ok := c.FirstWhere(func(i int, v int) bool {
		return v > 100
	})

	if ok {
		t.Fatal("expected false")
	}
}

// TestFirstWhereReturnsOkFalseOnEmpty verifies that FirstWhere on an empty
// collection returns ok=false.
func TestFirstWhereReturnsOkFalseOnEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	_, ok := c.FirstWhere(func(i int, v int) bool {
		return true
	})

	if ok {
		t.Fatal("expected false")
	}
}

// TestFirstReturnsFirstItemWithOkTrue verifies that First returns the leading
// item and ok=true for a non-empty collection.
func TestFirstReturnsFirstItemWithOkTrue(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		42, 99,
	})

	result, ok := c.First()

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(42, result) {
		t.Fatal("values differ")
	}
}

// TestFirstReturnsOkFalseOnEmpty verifies that First returns ok=false on an
// empty collection.
func TestFirstReturnsOkFalseOnEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	_, ok := c.First()

	if ok {
		t.Fatal("expected false")
	}
}

// TestLastReturnsLastItemWithOkTrue verifies that Last returns the final item
// and ok=true for a non-empty collection.
func TestLastReturnsLastItemWithOkTrue(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 77,
	})

	result, ok := c.Last()

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(77, result) {
		t.Fatal("values differ")
	}
}

// TestLastReturnsOkFalseOnEmpty verifies that Last returns ok=false on an
// empty collection.
func TestLastReturnsOkFalseOnEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	_, ok := c.Last()

	if ok {
		t.Fatal("expected false")
	}
}

// TestContainsTrueWhenFound verifies that Contains returns true when at least
// one item satisfies the predicate.
func TestContainsTrueWhenFound(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !c.Contains(func(i int, v int) bool {
		return v == 2
	}) {
		t.Fatal("expected true")
	}
}

// TestContainsFalseWhenNotFound verifies that Contains returns false when no
// item satisfies the predicate.
func TestContainsFalseWhenNotFound(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if c.Contains(func(i int, v int) bool {
		return v == 99
	}) {
		t.Fatal("expected false")
	}
}

// TestTakeReturnsFirstNItems verifies that Take returns exactly the first n
// items.
func TestTakeReturnsFirstNItems(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, c.Take(3).Items()) {
		t.Fatal("values differ")
	}
}

// TestTakeWithZeroLimitReturnsEmpty verifies that Take(0) produces an empty
// collection.
func TestTakeWithZeroLimitReturnsEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !c.Take(0).IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestTakeWithNegativeLimitReturnsEmpty verifies that Take with a negative
// limit produces an empty collection.
func TestTakeWithNegativeLimitReturnsEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !c.Take(-5).IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestTakeWithLimitExceedingLengthReturnsAll verifies that Take with a limit
// larger than the collection length returns all items.
func TestTakeWithLimitExceedingLengthReturnsAll(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, c.Take(100).Items()) {
		t.Fatal("values differ")
	}
}

// TestSkipRemovesFirstNItems verifies that Skip(n) removes exactly the first n
// items.
func TestSkipRemovesFirstNItems(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	if !reflect.DeepEqual([]int{
		3, 4, 5,
	}, c.Skip(2).Items()) {
		t.Fatal("values differ")
	}
}

// TestSkipZeroRemovesNothing verifies that Skip(0) returns all items.
func TestSkipZeroRemovesNothing(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, c.Skip(0).Items()) {
		t.Fatal("values differ")
	}
}

// TestSkipNegativeRemovesNothing verifies that Skip with a negative value
// returns all items.
func TestSkipNegativeRemovesNothing(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, c.Skip(-3).Items()) {
		t.Fatal("values differ")
	}
}

// TestSkipNExceedingLengthReturnsEmpty verifies that Skip with n >= length
// produces an empty collection.
func TestSkipNExceedingLengthReturnsEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !c.Skip(10).IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestTakeWhileReturnsItemsUntilFirstFalse verifies that TakeWhile stops at
// the first item that fails the predicate.
func TestTakeWhileReturnsItemsUntilFirstFalse(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		2, 4, 5, 6,
	})

	if !reflect.DeepEqual([]int{
		2, 4,
	}, c.TakeWhile(func(i int, v int) bool {
		return v%2 == 0
	}).Items()) {
		t.Fatal("values differ")
	}
}

// TestTakeWhileReturnsEmptyWhenFirstItemFails verifies that TakeWhile produces
// an empty collection when the first item already fails the predicate.
func TestTakeWhileReturnsEmptyWhenFirstItemFails(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !c.TakeWhile(func(i int, v int) bool {
		return v%2 == 0
	}).IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestTakeWhileReturnsAllWhenPredicateAlwaysTrue verifies that TakeWhile
// returns all items when the predicate is always true.
func TestTakeWhileReturnsAllWhenPredicateAlwaysTrue(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		2, 4, 6,
	})

	if !reflect.DeepEqual([]int{
		2, 4, 6,
	}, c.TakeWhile(func(i int, v int) bool {
		return v%2 == 0
	}).Items()) {
		t.Fatal("values differ")
	}
}

// TestSkipWhileSkipsLeadingMatchingItems verifies that SkipWhile drops items
// while the predicate holds and returns the remainder.
func TestSkipWhileSkipsLeadingMatchingItems(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		2, 4, 5, 6,
	})

	if !reflect.DeepEqual([]int{
		5, 6,
	}, c.SkipWhile(func(i int, v int) bool {
		return v%2 == 0
	}).Items()) {
		t.Fatal("values differ")
	}
}

// TestSkipWhileReturnsAllWhenFirstItemFailsPredicate verifies that SkipWhile
// returns all items when the very first item does not match.
func TestSkipWhileReturnsAllWhenFirstItemFailsPredicate(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, c.SkipWhile(func(i int, v int) bool {
		return v%2 == 0
	}).Items()) {
		t.Fatal("values differ")
	}
}

// TestSkipWhileReturnsEmptyWhenPredicateAlwaysTrue verifies that SkipWhile
// returns an empty collection when the predicate is always true.
func TestSkipWhileReturnsEmptyWhenPredicateAlwaysTrue(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		2, 4, 6,
	})

	if !c.SkipWhile(func(i int, v int) bool {
		return v%2 == 0
	}).IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestChunkSplitsEvenlyIntoSubCollections verifies that Chunk produces
// evenly-sized sub-collections when items divide cleanly.
func TestChunkSplitsEvenlyIntoSubCollections(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5, 6,
	})

	chunks := c.Chunk(2)

	requireSlicesEqual(t, [][]int{
		{1, 2},
		{3, 4},
		{5, 6},
	}, chunks)
}

// TestChunkLastChunkIsSmallerWhenItemsDontDivideEvenly verifies that the final
// chunk contains the remaining items when items do not divide evenly.
func TestChunkLastChunkIsSmallerWhenItemsDontDivideEvenly(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	chunks := c.Chunk(2)

	if value := chunks; len(value) != 3 {
		t.Fatalf("expected length %d, got %d", 3, len(value))
	}

	if !reflect.DeepEqual([]int{
		5,
	}, chunks[2].Items()) {
		t.Fatal("values differ")
	}
}

// TestChunkSizeLargerThanLengthReturnsSingleChunk verifies that Chunk returns a
// single sub-collection when the chunk size exceeds the collection length.
func TestChunkSizeLargerThanLengthReturnsSingleChunk(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	chunks := c.Chunk(10)

	if value := chunks; len(value) != 1 {
		t.Fatalf("expected length %d, got %d", 1, len(value))
	}

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, chunks[0].Items()) {
		t.Fatal("values differ")
	}
}

// TestChunkEmptyCollectionReturnsNil verifies that Chunk on an empty collection
// returns nil.
func TestChunkEmptyCollectionReturnsNil(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	if value := c.Chunk(3); value != nil {
		t.Fatalf("expected nil, got %v", value)
	}
}

// TestChunkPanicsOnZeroSize verifies that Chunk panics when given size=0.
func TestChunkPanicsOnZeroSize(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		c.Chunk(0)
	}()
}

// TestChunkPanicsOnNegativeSize verifies that Chunk panics when given a
// negative size.
func TestChunkPanicsOnNegativeSize(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		c.Chunk(-1)
	}()
}

// TestSlidingReturnsWindowsWithDefaultStepOfOne verifies that Sliding with no
// explicit step advances by one item between windows.
func TestSlidingReturnsWindowsWithDefaultStepOfOne(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4,
	})

	windows := c.Sliding(2)

	requireSlicesEqual(t, [][]int{
		{1, 2},
		{2, 3},
		{3, 4},
	}, windows)
}

// TestSlidingReturnsWindowsWithExplicitStep verifies that Sliding advances by
// the given step between windows.
func TestSlidingReturnsWindowsWithExplicitStep(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	windows := c.Sliding(2, 2)

	if value := windows; len(value) != 2 {
		t.Fatalf("expected length %d, got %d", 2, len(value))
	}

	if !reflect.DeepEqual([]int{
		1, 2,
	}, windows[0].Items()) {
		t.Fatal("values differ")
	}

	if !reflect.DeepEqual([]int{
		3, 4,
	}, windows[1].Items()) {
		t.Fatal("values differ")
	}
}

// TestSlidingReturnsNilWhenCollectionSmallerThanWindowSize verifies that
// Sliding returns nil when the window size exceeds the collection length.
func TestSlidingReturnsNilWhenCollectionSmallerThanWindowSize(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2,
	})

	if value := c.Sliding(5); value != nil {
		t.Fatalf("expected nil, got %v", value)
	}
}

// TestSlidingPanicsOnZeroSize verifies that Sliding panics when size=0.
func TestSlidingPanicsOnZeroSize(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		c.Sliding(0)
	}()
}

// TestSlidingPanicsOnNegativeSize verifies that Sliding panics when given a
// negative size.
func TestSlidingPanicsOnNegativeSize(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		c.Sliding(-1)
	}()
}

// TestSlidingPanicsOnZeroStep verifies that Sliding panics when step=0.
func TestSlidingPanicsOnZeroStep(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		c.Sliding(2, 0)
	}()
}

// TestSlidingPanicsOnNegativeStep verifies that Sliding panics when given a
// negative step.
func TestSlidingPanicsOnNegativeStep(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		c.Sliding(2, -1)
	}()
}

// TestReduceAccumulatesValuesWithSameType verifies that Reduce sums integers
// when K and T are the same type.
func TestReduceAccumulatesValuesWithSameType(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	sum := collection.Slice[int].Reduce(c, func(acc int, i int, v int) int {
		return acc + v
	}, 0)

	if !reflect.DeepEqual(15, sum) {
		t.Fatal("values differ")
	}
}

// TestReduceAccumulatesWithDifferentKType verifies that Reduce can accumulate
// int32 items into an int64 result.
func TestReduceAccumulatesWithDifferentKType(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int32{
		1, 2, 3, 4, 5,
	})

	sum := collection.Slice[int32].Reduce(c, func(acc int64, i int, v int32) int64 {
		return acc + int64(v)
	}, 0)

	if !reflect.DeepEqual(int64(15), sum) {
		t.Fatal("values differ")
	}
}

// TestReduceReturnsInitialValueOnEmptyCollection verifies that Reduce returns
// the initial value unchanged when the collection is empty.
func TestReduceReturnsInitialValueOnEmptyCollection(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	result := collection.Slice[int].Reduce(c, func(acc int, i int, v int) int {
		return acc + v
	}, 42)

	if !reflect.DeepEqual(42, result) {
		t.Fatal("values differ")
	}
}

// TestReverseReturnsReversedOrder verifies that Reverse returns items in
// reversed order.
func TestReverseReturnsReversedOrder(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !reflect.DeepEqual([]int{
		3, 2, 1,
	}, c.Reverse().Items()) {
		t.Fatal("values differ")
	}
}

// TestReverseReturnsEmptyOnEmptyInput verifies that Reverse on an empty
// collection produces an empty collection.
func TestReverseReturnsEmptyOnEmptyInput(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice[int](nil)

	if !c.Reverse().IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestReverseDoesNotMutateOriginal verifies that Reverse does not alter the
// original collection's order.
func TestReverseDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	_ = c.Reverse()

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, c.Items()) {
		t.Fatal("values differ")
	}
}

// TestSortReturnsSortedItems verifies that Sort returns items in ascending
// order using cmp.Compare.
func TestSortReturnsSortedItems(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		3, 1, 4, 1, 5, 9,
	})

	if !reflect.DeepEqual([]int{
		1, 1, 3, 4, 5, 9,
	}, c.Sort(cmp.Compare).Items()) {
		t.Fatal("values differ")
	}
}

// TestSortDoesNotMutateOriginal verifies that Sort does not alter the original
// collection.
func TestSortDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		3, 1, 2,
	})

	_ = c.Sort(cmp.Compare)

	if !reflect.DeepEqual([]int{
		3, 1, 2,
	}, c.Items()) {
		t.Fatal("values differ")
	}
}

// TestUniqueRemovesDuplicatesPreservingFirstOccurrence verifies that Unique
// keeps only the first occurrence of each key.
func TestUniqueRemovesDuplicatesPreservingFirstOccurrence(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 1, 3, 2,
	})

	result := collection.Slice[int].Unique(c, func(i int, v int) int {
		return v
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestUniqueWithCustomKeyFunction verifies that Unique deduplicates by an
// arbitrary key (e.g. first letter of a string).
func TestUniqueWithCustomKeyFunction(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]string{
		"apple", "avocado", "banana", "blueberry", "cherry",
	})

	result := collection.Slice[string].Unique(c, func(i int, v string) byte {
		return v[0]
	})

	if !reflect.DeepEqual([]string{
		"apple", "banana", "cherry",
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestPartitionSplitsIntoMatchingAndNonMatching verifies that Partition
// correctly separates items.
func TestPartitionSplitsIntoMatchingAndNonMatching(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5,
	})

	evens, odds := c.Partition(func(i int, v int) bool {
		return v%2 == 0
	})

	if !reflect.DeepEqual([]int{
		2, 4,
	}, evens.Items()) {
		t.Fatal("values differ")
	}

	if !reflect.DeepEqual([]int{
		1, 3, 5,
	}, odds.Items()) {
		t.Fatal("values differ")
	}
}

// TestPartitionAllMatchSecondResultIsEmpty verifies that the second result of
// Partition is empty when all items match.
func TestPartitionAllMatchSecondResultIsEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		2, 4, 6,
	})

	_, rest := c.Partition(func(i int, v int) bool {
		return v%2 == 0
	})

	if !rest.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestPartitionNoneMatchFirstResultIsEmpty verifies that the first result of
// Partition is empty when no items match.
func TestPartitionNoneMatchFirstResultIsEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 3, 5,
	})

	matching, _ := c.Partition(func(i int, v int) bool {
		return v%2 == 0
	})

	if !matching.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestConcatAppendsOtherItemsAfterSelf verifies that Concat appends the other
// collection's items in order after the receiver's items.
func TestConcatAppendsOtherItemsAfterSelf(t *testing.T) {
	t.Parallel()

	a := collection.NewSlice([]int{
		1, 2, 3,
	})

	b := collection.NewSlice([]int{
		4, 5, 6,
	})

	if !reflect.DeepEqual([]int{
		1, 2, 3, 4, 5, 6,
	}, a.Concat(b).Items()) {
		t.Fatal("values differ")
	}
}

// TestConcatDoesNotMutateOriginal verifies that Concat does not alter the
// receiver collection.
func TestConcatDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()

	a := collection.NewSlice([]int{
		1, 2,
	})

	b := collection.NewSlice([]int{
		3, 4,
	})

	_ = a.Concat(b)

	if !reflect.DeepEqual([]int{
		1, 2,
	}, a.Items()) {
		t.Fatal("values differ")
	}
}

// TestGroupByGroupsItemsByKeyFunction verifies that GroupBy partitions items
// into sub-collections by the returned key.
func TestGroupByGroupsItemsByKeyFunction(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5, 6,
	})

	grouped := collection.Slice[int].GroupBy(c, evenOddKey)

	if !reflect.DeepEqual(2, grouped.Len()) {
		t.Fatal("values differ")
	}

	requireMapSliceValue(t, grouped, "odd", []int{
		1, 3, 5,
	})

	requireMapSliceValue(t, grouped, "even", []int{
		2, 4, 6,
	})
}

// TestNthPicksEveryNthItemFromIndexZero verifies that Nth with no offset starts
// at index 0.
func TestNthPicksEveryNthItemFromIndexZero(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5, 6,
	})

	if !reflect.DeepEqual([]int{
		1, 3, 5,
	}, c.Nth(2).Items()) {
		t.Fatal("values differ")
	}
}

// TestNthPicksEveryNthItemStartingAtGivenOffset verifies that Nth respects an
// explicit starting offset.
func TestNthPicksEveryNthItemStartingAtGivenOffset(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5, 6,
	})

	if !reflect.DeepEqual([]int{
		2, 4, 6,
	}, c.Nth(2, 1).Items()) {
		t.Fatal("values differ")
	}
}

// TestNthOffsetBeyondLengthReturnsEmpty verifies that Nth with an offset
// beyond the collection length returns an empty collection.
func TestNthOffsetBeyondLengthReturnsEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !c.Nth(1, 10).IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestNthPanicsOnZeroN verifies that Nth panics when n=0.
func TestNthPanicsOnZeroN(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		c.Nth(0)
	}()
}

// TestNthPanicsOnNegativeN verifies that Nth panics when n is negative.
func TestNthPanicsOnNegativeN(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()

		c.Nth(-1)
	}()
}

// TestForPageReturnsFirstPage verifies that ForPage(1, perPage) returns the
// first page of items.
func TestForPageReturnsFirstPage(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5, 6,
	})

	if !reflect.DeepEqual([]int{
		1, 2,
	}, c.ForPage(1, 2).Items()) {
		t.Fatal("values differ")
	}
}

// TestForPageReturnsSecondPage verifies that ForPage(2, perPage) returns the
// second page of items.
func TestForPageReturnsSecondPage(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3, 4, 5, 6,
	})

	if !reflect.DeepEqual([]int{
		3, 4,
	}, c.ForPage(2, 2).Items()) {
		t.Fatal("values differ")
	}
}

// TestForPageBeyondDataReturnsEmpty verifies that ForPage returns an empty
// collection when the requested page is past the end of the data.
func TestForPageBeyondDataReturnsEmpty(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	if !c.ForPage(10, 2).IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestMarshalJSONMarshalsNonEmptyCollectionToJSONArray verifies that
// MarshalJSON produces a valid JSON array for a non-empty collection.
func TestMarshalJSONMarshalsNonEmptyCollectionToJSONArray(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		1, 2, 3,
	})

	data, err := json.Marshal(c)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requireSameJSON(t, `[1,2,3]`, string(data))
}

// TestMarshalJSONMarshalsEmptyCollectionToEmptyArray verifies that MarshalJSON
// produces `[]` for a non-nil empty collection.
func TestMarshalJSONMarshalsEmptyCollectionToEmptyArray(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{})

	data, err := json.Marshal(c)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(`[]`, string(data)) {
		t.Fatal("values differ")
	}
}

// TestUnmarshalJSONUnmarshalsJSONArrayIntoCollection verifies that
// UnmarshalJSON populates a collection from a JSON array.
func TestUnmarshalJSONUnmarshalsJSONArrayIntoCollection(t *testing.T) {
	t.Parallel()

	var c collection.Slice[int]

	err := json.Unmarshal([]byte(`[10,20,30]`), &c)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		10, 20, 30,
	}, c.Items()) {
		t.Fatal("values differ")
	}
}

// TestUnmarshalJSONOfInvalidJSONReturnsError verifies that UnmarshalJSON
// returns an error when given malformed JSON.
func TestUnmarshalJSONOfInvalidJSONReturnsError(t *testing.T) {
	t.Parallel()

	var c collection.Slice[int]

	err := json.Unmarshal([]byte(`not-json`), &c)

	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestMarshalJSONToEncodesNonEmptyCollectionToJSONArray verifies that
// MarshalJSONTo writes a valid JSON array to the encoder.
func TestMarshalJSONToEncodesNonEmptyCollectionToJSONArray(t *testing.T) {
	t.Parallel()

	c := collection.NewSlice([]int{
		4, 5, 6,
	})

	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)

	err := c.MarshalJSONTo(enc)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requireSameJSON(t, `[4,5,6]`, buf.String())
}

// TestUnmarshalJSONFromDecodesJSONArrayFromDecoder verifies that
// UnmarshalJSONFrom populates the collection from a JSON array.
func TestUnmarshalJSONFromDecodesJSONArrayFromDecoder(t *testing.T) {
	t.Parallel()

	dec := jsontext.NewDecoder(strings.NewReader(`[7,8,9]`))

	var c collection.Slice[int]

	err := c.UnmarshalJSONFrom(dec)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual([]int{
		7, 8, 9,
	}, c.Items()) {
		t.Fatal("values differ")
	}
}

// TestUnmarshalJSONFromOfInvalidJSONReturnsError verifies that
// UnmarshalJSONFrom returns an error when given malformed JSON.
func TestUnmarshalJSONFromOfInvalidJSONReturnsError(t *testing.T) {
	t.Parallel()

	dec := jsontext.NewDecoder(strings.NewReader(`not-json`))

	var c collection.Slice[int]

	err := c.UnmarshalJSONFrom(dec)

	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestNthNegativeOffsetStartsAtZero verifies that negative offsets use the
// first item as the starting point, preserving the requested stride.
func TestNthNegativeOffsetStartsAtZero(t *testing.T) {
	t.Parallel()

	if got := collection.NewSlice([]int{1, 2, 3}).Nth(2, -1); !reflect.DeepEqual([]int(got), []int{1, 3}) {
		t.Fatalf("nth = %v", got)
	}
}
