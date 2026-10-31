package collection_test

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/foundation/collection"
)

// orderedSource returns a LazyMap with a deterministic iteration order.
func orderedSource() collection.LazyMap[string, int] {
	return collection.NewLazyMap(func(yield func(string, int) bool) {
		keys := []string{
			"a", "b", "c",
		}
		vals := []int{
			1, 2, 3,
		}

		for i, k := range keys {
			if !yield(k, vals[i]) {
				return
			}
		}
	})
}

// ExampleLazyMap contrasts the first-key policy of Unique with materialization.
func ExampleLazyMap() {
	first := collection.NewMap(map[string]int{"apples": 3}).Lazy()
	last := collection.NewMap(map[string]int{"apples": 4}).Lazy()

	fmt.Println(first.Concat(last).Items()["apples"])
	fmt.Println(first.Concat(last).Unique().Items()["apples"])
	fmt.Println(collection.LazyKeys(first).Items())
	fmt.Println(collection.LazyValues(first).Items())
	// Output:
	// 4
	// 3
	// [apples]
	// [3]
}

// ExampleLazyMap_MarshalJSON exercises lazy materialization during encoding.
func ExampleLazyMap_MarshalJSON() {
	data, err := collection.NewMap(map[string]int{"apples": 3}).Lazy().MarshalJSON()

	if err != nil {
		panic(err)
	}

	fmt.Println(string(data))
	// Output: {"apples":3}
}

// TestNewLazyMapCanBeUsedInForRange verifies that LazyMap can be ranged over.
func TestNewLazyMapCanBeUsedInForRange(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"x": 1, "y": 2,
	}))
	collected := make(map[string]int)

	for k, v := range lm {
		collected[k] = v
	}

	if !reflect.DeepEqual(map[string]int{
		"x": 1, "y": 2,
	}, collected) {
		t.Fatal("values differ")
	}
}

// TestNewLazyMapYieldsAllEntriesFromSource verifies that NewLazyMap yields source entries.
func TestNewLazyMapYieldsAllEntriesFromSource(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"a": 1, "b": 2, "c": 3,
	}
	lm := collection.NewLazyMap(maps.All(input))

	if !reflect.DeepEqual(input, lm.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazyMapEagerMaterializesToMap verifies that Eager materializes every entry.
func TestLazyMapEagerMaterializesToMap(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"a": 1, "b": 2,
	}
	lm := collection.NewLazyMap(maps.All(input))

	eager := lm.Eager()

	if !reflect.DeepEqual(input, eager.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazyMapItemsReturnsGoMap verifies that Items materializes a Go map.
func TestLazyMapItemsReturnsGoMap(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"x": 10,
	}
	lm := collection.NewLazyMap(maps.All(input))

	if !reflect.DeepEqual(input, lm.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazyMapIsEmptyTrueForEmpty verifies that IsEmpty recognizes an empty sequence.
func TestLazyMapIsEmptyTrueForEmpty(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{}))

	if !lm.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestLazyMapIsEmptyFalseForNonEmpty verifies that IsEmpty rejects a non-empty sequence.
func TestLazyMapIsEmptyFalseForNonEmpty(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1,
	}))

	if lm.IsEmpty() {
		t.Fatal("expected false")
	}
}

// TestLazyMapIsNotEmptyTrueForNonEmpty verifies that IsNotEmpty recognizes a non-empty sequence.
func TestLazyMapIsNotEmptyTrueForNonEmpty(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1,
	}))

	if !lm.IsNotEmpty() {
		t.Fatal("expected true")
	}
}

// TestLazyMapIsNotEmptyFalseForEmpty verifies that IsNotEmpty rejects an empty sequence.
func TestLazyMapIsNotEmptyFalseForEmpty(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{}))

	if lm.IsNotEmpty() {
		t.Fatal("expected false")
	}
}

// TestLazyMapLenReturnsCorrectCount verifies that Len counts yielded entries.
func TestLazyMapLenReturnsCorrectCount(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 2, "c": 3,
	}))

	if !reflect.DeepEqual(3, lm.Len()) {
		t.Fatal("values differ")
	}
}

// TestLazyMapLenZeroForEmpty verifies that Len returns zero for an empty sequence.
func TestLazyMapLenZeroForEmpty(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{}))

	if value := lm.Len(); value != 0 {
		t.Fatalf("expected zero value, got %v", value)
	}
}

// TestLazyMapEveryTrueWhenAllMatch verifies that Every accepts matching entries.
func TestLazyMapEveryTrueWhenAllMatch(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 2, "b": 4,
	}))

	if !lm.Every(func(_ string, v int) bool {
		return v%2 == 0
	}) {
		t.Fatal("expected true")
	}
}

// TestLazyMapEveryFalseWhenOneFails verifies that Every rejects a failing entry.
func TestLazyMapEveryFalseWhenOneFails(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 2, "b": 3,
	}))

	if lm.Every(func(_ string, v int) bool {
		return v%2 == 0
	}) {
		t.Fatal("expected false")
	}
}

// TestLazyMapEveryTrueOnEmpty verifies that Every accepts an empty sequence.
func TestLazyMapEveryTrueOnEmpty(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{}))

	if !lm.Every(func(_ string, v int) bool {
		return false
	}) {
		t.Fatal("expected true")
	}
}

// TestLazyMapEachCallsFForEveryEntry verifies that Each visits every entry.
func TestLazyMapEachCallsFForEveryEntry(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"a": 1, "b": 2,
	}
	lm := collection.NewLazyMap(maps.All(input))
	collected := make(map[string]int)

	lm.Each(func(k string, v int) {
		collected[k] = v
	})

	if !reflect.DeepEqual(input, collected) {
		t.Fatal("values differ")
	}
}

// TestLazyMapTapEachIsLazyBeforeConsumption verifies that TapEach defers callbacks.
func TestLazyMapTapEachIsLazyBeforeConsumption(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1,
	}))
	called := false

	tapped := lm.TapEach(func(_ string, _ int) {
		called = true
	})

	// f must not have been called yet — only the lazy map was constructed.
	if called {
		t.Fatal("expected false")
	}
	// Consume to trigger f.
	_ = tapped.Items()

	if !called {
		t.Fatal("expected true")
	}
}

// TestLazyMapTapEachCallsFUponConsumption verifies that TapEach calls callbacks on traversal.
func TestLazyMapTapEachCallsFUponConsumption(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"a": 1, "b": 2,
	}
	lm := collection.NewLazyMap(maps.All(input))
	collected := make(map[string]int)

	tapped := lm.TapEach(func(k string, v int) {
		collected[k] = v
	})

	// Consume.
	_ = tapped.Items()

	if !reflect.DeepEqual(input, collected) {
		t.Fatal("values differ")
	}
}

// TestLazyMapContainsTrueWhenFound verifies that Contains finds a matching entry.
func TestLazyMapContainsTrueWhenFound(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 99,
	}))

	if !lm.Contains(func(_ string, v int) bool {
		return v == 99
	}) {
		t.Fatal("expected true")
	}
}

// TestLazyMapContainsFalseWhenNotFound verifies that Contains rejects non-matching entries.
func TestLazyMapContainsFalseWhenNotFound(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 2,
	}))

	if lm.Contains(func(_ string, v int) bool {
		return v == 99
	}) {
		t.Fatal("expected false")
	}
}

// TestLazyMapHasAnyTrueWhenAnyKeyExists verifies that HasAny finds a requested key.
func TestLazyMapHasAnyTrueWhenAnyKeyExists(t *testing.T) {
	t.Parallel()

	lm := orderedSource()

	if !lm.HasAny("x", "b") {
		t.Fatal("expected true")
	}
}

// TestLazyMapHasAnyFalseWhenNoKeysExist verifies that HasAny rejects absent keys.
func TestLazyMapHasAnyFalseWhenNoKeysExist(t *testing.T) {
	t.Parallel()

	lm := orderedSource()

	if lm.HasAny("x", "y") {
		t.Fatal("expected false")
	}
}

// TestLazyMapFilterOnlyMatchingEntriesWhenMaterialized verifies that Filter retains matches.
func TestLazyMapFilterOnlyMatchingEntriesWhenMaterialized(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 2, "c": 3, "d": 4,
	}))

	filtered := lm.Filter(func(_ string, v int) bool {
		return v%2 == 0
	}).Items()

	if !reflect.DeepEqual(map[string]int{
		"b": 2, "d": 4,
	}, filtered) {
		t.Fatal("values differ")
	}
}

// TestLazyMapFilterEmptyResultWhenNothingMatches verifies that Filter can yield no entries.
func TestLazyMapFilterEmptyResultWhenNothingMatches(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 3,
	}))

	filtered := lm.Filter(func(_ string, v int) bool {
		return v%2 == 0
	}).Items()

	if len(filtered) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazyMapRejectOnlyNonMatchingEntries verifies that Reject removes matching entries.
func TestLazyMapRejectOnlyNonMatchingEntries(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 2, "c": 3, "d": 4,
	}))

	rejected := lm.Reject(func(_ string, v int) bool {
		return v%2 == 0
	}).Items()

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "c": 3,
	}, rejected) {
		t.Fatal("values differ")
	}
}

// TestLazyMapOnlyKeepsRequestedKeys verifies that Only retains requested keys.
func TestLazyMapOnlyKeepsRequestedKeys(t *testing.T) {
	t.Parallel()

	result := orderedSource().Only("b", "missing").Items()

	if !reflect.DeepEqual(map[string]int{
		"b": 2,
	}, result) {
		t.Fatal("values differ")
	}
}

// TestLazyMapExceptRemovesRequestedKeys verifies that Except removes requested keys.
func TestLazyMapExceptRemovesRequestedKeys(t *testing.T) {
	t.Parallel()

	result := orderedSource().Except("b", "missing").Items()

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "c": 3,
	}, result) {
		t.Fatal("values differ")
	}
}

// TestLazyMapValuesTransformsValuesLazily verifies that MapValues defers value transformation.
func TestLazyMapValuesTransformsValuesLazily(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 2,
	}))

	result := lm.MapValues(func(k string, v int) string {
		return strings.Repeat("*", v)
	}).Items()

	if !reflect.DeepEqual(map[string]string{
		"a": "*", "b": "**",
	}, result) {
		t.Fatal("values differ")
	}
}

// TestLazyMapKeysTransformsKeysLazily verifies that MapKeys defers key transformation.
func TestLazyMapKeysTransformsKeysLazily(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 2,
	}))

	result := lm.MapKeys(func(k string, v int) string {
		return strings.ToUpper(k)
	}).Items()

	if !reflect.DeepEqual(map[string]int{
		"A": 1, "B": 2,
	}, result) {
		t.Fatal("values differ")
	}
}

// TestLazyKeysReturnsLazySliceOfKeys verifies that LazyKeys yields map keys.
func TestLazyKeysReturnsLazySliceOfKeys(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 2, "c": 3,
	}))

	keys := collection.LazyKeys(lm).Items()

	requireSameElements(t, []string{
		"a", "b", "c",
	}, keys)
}

// TestLazyValuesReturnsLazySliceOfValues verifies that LazyValues yields map values.
func TestLazyValuesReturnsLazySliceOfValues(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 2, "c": 3,
	}))

	values := collection.LazyValues(lm).Items()

	requireSameElements(t, []int{
		1, 2, 3,
	}, values)
}

// TestLazyMapTakeYieldsAtMostNEntries verifies that Take limits yielded entries.
func TestLazyMapTakeYieldsAtMostNEntries(t *testing.T) {
	t.Parallel()

	source := orderedSource()

	taken := source.Take(2).Items()

	if value := taken; len(value) != 2 {
		t.Fatalf("expected length %d, got %d", 2, len(value))
	}
}

// TestLazyMapTakeZeroLimitYieldsNothing verifies that Take yields nothing at zero.
func TestLazyMapTakeZeroLimitYieldsNothing(t *testing.T) {
	t.Parallel()

	source := orderedSource()

	taken := source.Take(0).Items()

	if len(taken) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestLazyMapSkipSkipsFirstNEntries verifies that Skip omits initial entries.
func TestLazyMapSkipSkipsFirstNEntries(t *testing.T) {
	t.Parallel()

	source := orderedSource() // a:1, b:2, c:3 in deterministic order

	skipped := source.Skip(2).Items()

	if !reflect.DeepEqual(map[string]int{
		"c": 3,
	}, skipped) {
		t.Fatal("values differ")
	}
}

// TestLazyMapSkipZeroYieldsAll verifies that Skip preserves entries at zero.
func TestLazyMapSkipZeroYieldsAll(t *testing.T) {
	t.Parallel()

	source := orderedSource()

	skipped := source.Skip(0).Items()

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "b": 2, "c": 3,
	}, skipped) {
		t.Fatal("values differ")
	}
}

// TestLazyMapUniqueDeduplicatesByKeyKeepingFirstOccurrence verifies that Unique retains first keys.
func TestLazyMapUniqueDeduplicatesByKeyKeepingFirstOccurrence(t *testing.T) {
	t.Parallel()

	// Hand-written iterator with a duplicate key "a".
	source := collection.NewLazyMap(func(yield func(string, int) bool) {
		for _, e := range [][2]any{
			{
				"a", 1,
			}, {
				"b", 2,
			}, {
				"a", 99,
			},
		} {
			if !yield(e[0].(string), e[1].(int)) {
				return
			}
		}
	})

	result := source.Unique().Items()

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "b": 2,
	}, result) {
		t.Fatal("values differ")
	}
}

// TestLazyMapConcatYieldsEntriesFromBothMaps verifies that Concat yields both sources.
func TestLazyMapConcatYieldsEntriesFromBothMaps(t *testing.T) {
	t.Parallel()

	first := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1,
	}))
	second := collection.NewLazyMap(maps.All(map[string]int{
		"b": 2,
	}))

	combined := first.Concat(second).Items()

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "b": 2,
	}, combined) {
		t.Fatal("values differ")
	}
}

// TestLazyMapMarshalJSONProducesObject verifies that MarshalJSON encodes an object.
func TestLazyMapMarshalJSONProducesObject(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1, "b": 2,
	}))

	data, err := json.Marshal(lm)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]int

	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "b": 2,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazyMapUnmarshalJSONFromObject verifies that UnmarshalJSON decodes an object.
func TestLazyMapUnmarshalJSONFromObject(t *testing.T) {
	t.Parallel()

	data := []byte(`{"x":10,"y":20}`)
	var lm collection.LazyMap[string, int]

	if err := json.Unmarshal(data, &lm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(map[string]int{
		"x": 10, "y": 20,
	}, lm.Items()) {
		t.Fatal("values differ")
	}
}

// TestLazyMapUnmarshalJSONInvalidReturnsError verifies that UnmarshalJSON rejects invalid input.
func TestLazyMapUnmarshalJSONInvalidReturnsError(t *testing.T) {
	t.Parallel()

	var lm collection.LazyMap[string, int]

	err := json.Unmarshal([]byte(`not json`), &lm)

	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestLazyMapMarshalJSONToEncodesAsObject verifies that MarshalJSONTo encodes an object.
func TestLazyMapMarshalJSONToEncodesAsObject(t *testing.T) {
	t.Parallel()

	lm := collection.NewLazyMap(maps.All(map[string]int{
		"a": 1,
	}))
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)

	if err := lm.MarshalJSONTo(enc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]int

	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(map[string]int{
		"a": 1,
	}, got) {
		t.Fatal("values differ")
	}
}

// TestLazyMapUnmarshalJSONFromDecodesFromDecoder verifies that UnmarshalJSONFrom decodes a decoder.
func TestLazyMapUnmarshalJSONFromDecodesFromDecoder(t *testing.T) {
	t.Parallel()

	data := []byte(`{"p":7,"q":8}`)
	dec := jsontext.NewDecoder(bytes.NewReader(data))

	var lm collection.LazyMap[string, int]

	if err := lm.UnmarshalJSONFrom(dec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(map[string]int{
		"p": 7, "q": 8,
	}, lm.Items()) {
		t.Fatal("values differ")
	}
}
