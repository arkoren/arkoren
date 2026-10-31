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

// ExampleMap shows presence of zero values and named map access.
func ExampleMap() {
	type Stock map[string]int
	stock := collection.NewMapAs(Stock{"apples": 0})

	fmt.Println(stock.Get("apples"))
	fmt.Println(stock.HasAny("pears", "apples"))

	stock.ItemsAs[Stock]()["apples"] = 3

	fmt.Println(stock.Items()["apples"])
	// Output:
	// 0 true
	// true
	// 3
}

// ExampleMap_UnmarshalJSONFrom exercises direct streaming map decoding.
func ExampleMap_UnmarshalJSONFrom() {
	var stock collection.Map[string, int]

	err := stock.UnmarshalJSONFrom(jsontext.NewDecoder(strings.NewReader(`{"apples":3}`)))

	if err != nil {
		panic(err)
	}

	fmt.Println(stock.Items()["apples"])
	// Output: 3
}

// TestMapMergeZeroReceiverCopiesOther verifies that a zero Map treats its nil
// storage as empty and returns an independent outer map for nonempty entries.
func TestMapMergeZeroReceiverCopiesOther(t *testing.T) {
	// receiver is the usable zero value, with no allocated map storage.
	var receiver collection.Map[string, int]
	// other supplies entries whose outer storage must not be returned directly.
	other := collection.NewMap(map[string]int{"count": 3})
	// merged must contain the other entries without changing the nil receiver.
	merged := receiver.Merge(other)

	if !maps.Equal(merged.Items(), map[string]int{"count": 3}) {
		t.Fatalf("merged = %v, want map[count:3]", merged.Items())
	}

	if receiver.Items() != nil {
		t.Fatalf("receiver = %v, want nil", receiver.Items())
	}

	merged.Items()["count"] = 9

	if other.Items()["count"] != 3 {
		t.Fatalf("other count = %d, want 3", other.Items()["count"])
	}
}

// TestMapMergeNilWrappedReceiverCopiesOther verifies that NewMap(nil) remains
// usable for merging nonempty entries without aliasing the other outer map.
func TestMapMergeNilWrappedReceiverCopiesOther(t *testing.T) {
	// receiver wraps a nil map rather than allocating storage at construction.
	receiver := collection.NewMap[string, int](nil)
	// other supplies entries whose outer storage must remain independent.
	other := collection.NewMap(map[string]int{"count": 3})
	// merged must copy other even though the receiver starts with nil storage.
	merged := receiver.Merge(other)

	if !maps.Equal(merged.Items(), map[string]int{"count": 3}) {
		t.Fatalf("merged = %v, want map[count:3]", merged.Items())
	}

	if receiver.Items() != nil {
		t.Fatalf("receiver = %v, want nil", receiver.Items())
	}

	other.Items()["count"] = 9

	if merged.Items()["count"] != 3 {
		t.Fatalf("merged count = %d, want 3", merged.Items()["count"])
	}
}

// TestMapMergeNilOperandsPreservesNil verifies that merging two empty nil
// maps retains the existing nil result rather than allocating unused storage.
func TestMapMergeNilOperandsPreservesNil(t *testing.T) {
	// receiver exercises zero storage without a constructor.
	var receiver collection.Map[string, int]
	// other exercises the equivalent constructor path with nil input.
	other := collection.NewMap[string, int](nil)
	// merged must preserve the nil representation when there are no entries.
	merged := receiver.Merge(other)

	if merged.Items() != nil {
		t.Fatalf("merged = %v, want nil", merged.Items())
	}
}

// TestMapMergeNilReceiverWithEmptyOtherPreservesNil verifies that an allocated
// empty other map still needs no result storage when the receiver is nil.
func TestMapMergeNilReceiverWithEmptyOtherPreservesNil(t *testing.T) {
	// receiver has no entries and no allocated storage.
	var receiver collection.Map[string, int]
	// other is empty but non-nil, unlike the zero receiver.
	other := collection.NewMap(map[string]int{})
	// merged retains the representation that Merge used before the nil fix.
	merged := receiver.Merge(other)

	if merged.Items() != nil {
		t.Fatalf("merged = %v, want nil", merged.Items())
	}
}

// TestMapMergeNilOtherCopiesReceiver verifies that a nil argument contributes
// no entries without causing the returned outer map to alias the receiver.
func TestMapMergeNilOtherCopiesReceiver(t *testing.T) {
	// receiver supplies entries to be copied even when other contributes none.
	receiver := collection.NewMap(map[string]int{"count": 3})
	// other is a valid empty argument with nil storage.
	var other collection.Map[string, int]
	// merged must own its outer storage rather than return receiver directly.
	merged := receiver.Merge(other)

	if !maps.Equal(merged.Items(), map[string]int{"count": 3}) {
		t.Fatalf("merged = %v, want map[count:3]", merged.Items())
	}

	merged.Items()["count"] = 9

	if receiver.Items()["count"] != 3 {
		t.Fatalf("receiver count = %d, want 3", receiver.Items()["count"])
	}
}

// TestMapMergeCopiesBothOuterMaps verifies that combining populated inputs
// leaves both unchanged, including collisions, and detaches result entries.
func TestMapMergeCopiesBothOuterMaps(t *testing.T) {
	// receiver supplies a unique key and the original collision value.
	receiver := collection.NewMap(map[string]int{"receiver": 1, "shared": 2})
	// other supplies a unique key and the winning collision value.
	other := collection.NewMap(map[string]int{"other": 3, "shared": 4})
	// merged can be changed without updating either input's outer entries.
	merged := receiver.Merge(other)

	if !maps.Equal(receiver.Items(), map[string]int{"receiver": 1, "shared": 2}) {
		t.Fatalf("receiver changed during merge: %v", receiver.Items())
	}

	if !maps.Equal(other.Items(), map[string]int{"other": 3, "shared": 4}) {
		t.Fatalf("other changed during merge: %v", other.Items())
	}

	merged.Items()["receiver"] = 9
	merged.Items()["other"] = 9
	merged.Items()["shared"] = 9

	if !maps.Equal(receiver.Items(), map[string]int{"receiver": 1, "shared": 2}) {
		t.Fatalf("receiver aliases merged entries: %v", receiver.Items())
	}

	if !maps.Equal(other.Items(), map[string]int{"other": 3, "shared": 4}) {
		t.Fatalf("other aliases merged entries: %v", other.Items())
	}
}

// TestMapMergeKeepsNestedValuesShared verifies that copying the outer maps
// does not deep-copy slices stored in values from either input.
func TestMapMergeKeepsNestedValuesShared(t *testing.T) {
	// receiver retains a slice whose backing storage must remain shared.
	receiver := collection.NewMap(map[string][]int{"receiver": {1}})
	// other retains a separate slice subject to the same shallow-copy rule.
	other := collection.NewMap(map[string][]int{"other": {2}})
	// merged owns its keys but keeps the input slices' backing storage.
	merged := receiver.Merge(other)

	merged.Items()["receiver"][0] = 3
	merged.Items()["other"][0] = 4

	if receiver.Items()["receiver"][0] != 3 {
		t.Fatalf("receiver value = %v, want [3]", receiver.Items()["receiver"])
	}

	if other.Items()["other"][0] != 4 {
		t.Fatalf("other value = %v, want [4]", other.Items()["other"])
	}
}

// ExampleMap_Merge demonstrates the inline Merge example: a zero map accepts
// initial entries, then a later override wins without relying on map order.
func ExampleMap_Merge() {
	// settings starts empty without requiring an allocated map.
	var settings collection.Map[string, string]
	settings = settings.Merge(collection.NewMap(map[string]string{"theme": "light"}))
	settings = settings.Merge(collection.NewMap(map[string]string{"theme": "dark"}))

	fmt.Println(settings.Items()["theme"])
	// Output: dark
}

// TestNewMapItemsReturnsUnderlyingMap verifies that Items returns the supplied map.
func TestNewMapItemsReturnsUnderlyingMap(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"a": 1, "b": 2,
	}
	m := collection.NewMap(input)

	if !reflect.DeepEqual(input, m.Items()) {
		t.Fatal("values differ")
	}
}

// TestNewMapAsAcceptsNamedMapType verifies that NewMapAs accepts named map types.
func TestNewMapAsAcceptsNamedMapType(t *testing.T) {
	t.Parallel()

	type counts map[string]int

	m := collection.NewMapAs(counts{
		"a": 1, "b": 2,
	})

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "b": 2,
	}, m.Items()) {
		t.Fatal("values differ")
	}
}

// TestNewMapCreatesMapFromPlainMap verifies that NewMap wraps a plain map.
func TestNewMapCreatesMapFromPlainMap(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"a": 1, "b": 2,
	}
	m := collection.NewMap(input)

	if !reflect.DeepEqual(input, m.Items()) {
		t.Fatal("values differ")
	}
}

// TestNewMapNilItemsReturnsNilMap verifies that NewMap preserves a nil map.
func TestNewMapNilItemsReturnsNilMap(t *testing.T) {
	t.Parallel()

	m := collection.NewMap[string, int](nil)

	if value := m.Items(); value != nil {
		t.Fatalf("expected nil, got %v", value)
	}
}

// TestNewMapEmptyItems verifies that NewMap preserves an empty map.
func TestNewMapEmptyItems(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{})

	if len(m.Items()) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestMapHasTrueWhenKeyExists verifies that Has reports an existing key.
func TestMapHasTrueWhenKeyExists(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"x": 10,
	})

	if !m.Has("x") {
		t.Fatal("expected true")
	}
}

// TestMapHasFalseWhenKeyAbsent verifies that Has rejects an absent key.
func TestMapHasFalseWhenKeyAbsent(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"x": 10,
	})

	if m.Has("z") {
		t.Fatal("expected false")
	}
}

// TestMapGetReturnsValueAndTrueWhenFound verifies that Get returns a present value.
func TestMapGetReturnsValueAndTrueWhenFound(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"k": 42,
	})

	v, ok := m.Get("k")

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(42, v) {
		t.Fatal("values differ")
	}
}

// TestMapGetReturnsZeroAndFalseWhenNotFound verifies that Get reports a missing key.
func TestMapGetReturnsZeroAndFalseWhenNotFound(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"k": 42,
	})

	v, ok := m.Get("missing")

	if ok {
		t.Fatal("expected false")
	}

	if value := v; value != 0 {
		t.Fatalf("expected zero value, got %v", value)
	}
}

// TestMapHasAnyTrueWhenAnyKeyExists verifies that HasAny finds a requested key.
func TestMapHasAnyTrueWhenAnyKeyExists(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2,
	})

	if !m.HasAny("x", "b") {
		t.Fatal("expected true")
	}
}

// TestMapHasAnyFalseWhenNoKeysExist verifies that HasAny rejects absent keys.
func TestMapHasAnyFalseWhenNoKeysExist(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2,
	})

	if m.HasAny("x", "y") {
		t.Fatal("expected false")
	}
}

// TestMapLenReturnsCorrectCount verifies that Len counts map entries.
func TestMapLenReturnsCorrectCount(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2, "c": 3,
	})

	if !reflect.DeepEqual(3, m.Len()) {
		t.Fatal("values differ")
	}
}

// TestMapLenZeroForEmpty verifies that Len returns zero for an empty map.
func TestMapLenZeroForEmpty(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{})

	if value := m.Len(); value != 0 {
		t.Fatalf("expected zero value, got %v", value)
	}
}

// TestMapIsEmptyTrueForEmpty verifies that IsEmpty recognizes an empty map.
func TestMapIsEmptyTrueForEmpty(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{})

	if !m.IsEmpty() {
		t.Fatal("expected true")
	}
}

// TestMapIsEmptyFalseForNonEmpty verifies that IsEmpty rejects a non-empty map.
func TestMapIsEmptyFalseForNonEmpty(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1,
	})

	if m.IsEmpty() {
		t.Fatal("expected false")
	}
}

// TestMapIsNotEmptySymmetric verifies that IsNotEmpty complements IsEmpty.
func TestMapIsNotEmptySymmetric(t *testing.T) {
	t.Parallel()

	empty := collection.NewMap(map[string]int{})
	nonEmpty := collection.NewMap(map[string]int{
		"a": 1,
	})

	if empty.IsNotEmpty() {
		t.Fatal("expected false")
	}

	if !nonEmpty.IsNotEmpty() {
		t.Fatal("expected true")
	}
}

// TestMapEachCallsFForEveryEntry verifies that Each visits every map entry.
func TestMapEachCallsFForEveryEntry(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"a": 1, "b": 2, "c": 3,
	}
	m := collection.NewMap(input)
	collected := make(map[string]int)

	m.Each(func(k string, v int) {
		collected[k] = v
	})

	if !reflect.DeepEqual(input, collected) {
		t.Fatal("values differ")
	}
}

// TestMapTapEachCallsFAndReturnsOriginal verifies that TapEach preserves the map.
func TestMapTapEachCallsFAndReturnsOriginal(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"a": 1, "b": 2,
	}
	m := collection.NewMap(input)
	collected := make(map[string]int)

	result := m.TapEach(func(k string, v int) {
		collected[k] = v
	})

	if !reflect.DeepEqual(input, collected) {
		t.Fatal("values differ")
	}

	if !reflect.DeepEqual(input, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapEveryTrueWhenAllMatch verifies that Every accepts matching entries.
func TestMapEveryTrueWhenAllMatch(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 2, "b": 4, "c": 6,
	})

	if !m.Every(func(_ string, v int) bool {
		return v%2 == 0
	}) {
		t.Fatal("expected true")
	}
}

// TestMapEveryFalseWhenOneFails verifies that Every rejects a failing entry.
func TestMapEveryFalseWhenOneFails(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 2, "b": 3, "c": 6,
	})

	if m.Every(func(_ string, v int) bool {
		return v%2 == 0
	}) {
		t.Fatal("expected false")
	}
}

// TestMapEveryTrueOnEmpty verifies that Every accepts an empty map.
func TestMapEveryTrueOnEmpty(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{})

	if !m.Every(func(_ string, v int) bool {
		return false
	}) {
		t.Fatal("expected true")
	}
}

// TestMapContainsTrueWhenAnyEntryMatches verifies that Contains finds a matching entry.
func TestMapContainsTrueWhenAnyEntryMatches(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 99, "c": 3,
	})

	if !m.Contains(func(_ string, v int) bool {
		return v == 99
	}) {
		t.Fatal("expected true")
	}
}

// TestMapContainsFalseWhenNoneMatch verifies that Contains rejects non-matching entries.
func TestMapContainsFalseWhenNoneMatch(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2,
	})

	if m.Contains(func(_ string, v int) bool {
		return v == 99
	}) {
		t.Fatal("expected false")
	}
}

// TestMapFilterReturnsMatchingEntries verifies that Filter retains matching entries.
func TestMapFilterReturnsMatchingEntries(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2, "c": 3, "d": 4,
	})

	filtered := m.Filter(func(_ string, v int) bool {
		return v%2 == 0
	})

	if !reflect.DeepEqual(map[string]int{
		"b": 2, "d": 4,
	}, filtered.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapFilterReturnsEmptyWhenNothingMatches verifies that Filter returns no non-matches.
func TestMapFilterReturnsEmptyWhenNothingMatches(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 3,
	})

	filtered := m.Filter(func(_ string, v int) bool {
		return v%2 == 0
	})

	if len(filtered.Items()) != 0 {
		t.Fatal("expected empty value")
	}
}

// TestMapRejectReturnsNonMatchingEntries verifies that Reject removes matching entries.
func TestMapRejectReturnsNonMatchingEntries(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2, "c": 3, "d": 4,
	})

	rejected := m.Reject(func(_ string, v int) bool {
		return v%2 == 0
	})

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "c": 3,
	}, rejected.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapOnlyKeepsRequestedKeys verifies that Only retains requested keys.
func TestMapOnlyKeepsRequestedKeys(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2, "c": 3,
	})

	result := m.Only("b", "missing")

	if !reflect.DeepEqual(map[string]int{
		"b": 2,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapExceptRemovesRequestedKeys verifies that Except removes requested keys.
func TestMapExceptRemovesRequestedKeys(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2, "c": 3,
	})

	result := m.Except("b", "missing")

	if !reflect.DeepEqual(map[string]int{
		"a": 1, "c": 3,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapValuesTransformsValuesPreservesKeys verifies that MapValues preserves keys.
func TestMapValuesTransformsValuesPreservesKeys(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2,
	})

	result := m.MapValues(func(k string, v int) string {
		return strings.Repeat("x", v)
	})

	if !reflect.DeepEqual(map[string]string{
		"a": "x", "b": "xx",
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapKeysTransformsKeysPreservesValues verifies that MapKeys preserves values.
func TestMapKeysTransformsKeysPreservesValues(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2,
	})

	result := m.MapKeys(func(k string, v int) string {
		return strings.ToUpper(k)
	})

	if !reflect.DeepEqual(map[string]int{
		"A": 1, "B": 2,
	}, result.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapKeysLastWriteWinsOnCollision verifies that MapKeys keeps the last collision.
func TestMapKeysLastWriteWinsOnCollision(t *testing.T) {
	t.Parallel()

	// Both "a" and "A" map to "X" via ToUpper-then-force; use a fixed collision.
	m := collection.NewMap(map[string]int{
		"hello": 1, "world": 2,
	})
	// Map all keys to the same key — last write wins.
	result := m.MapKeys(func(_ string, _ int) string {
		return "same"
	})

	if !reflect.DeepEqual(1, result.Len()) {
		t.Fatal("values differ")
	}
}

// TestKeysContainsAllKeys verifies that Keys includes every map key.
func TestKeysContainsAllKeys(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2, "c": 3,
	})

	keys := collection.Keys(m)

	requireSameElements(t, []string{
		"a", "b", "c",
	}, keys.Items())
}

// TestValuesContainsAllValues verifies that Values includes every map value.
func TestValuesContainsAllValues(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2, "c": 3,
	})

	values := collection.Values(m)

	requireSameElements(t, []int{
		1, 2, 3,
	}, values.Items())
}

// TestMapMergeOtherValuesTakePrecedenceOnCollision verifies that Merge prefers other values.
func TestMapMergeOtherValuesTakePrecedenceOnCollision(t *testing.T) {
	t.Parallel()

	a := collection.NewMap(map[string]int{
		"x": 1, "y": 2,
	})
	b := collection.NewMap(map[string]int{
		"x": 99,
	})

	merged := a.Merge(b)

	v, ok := merged.Get("x")

	if !ok {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(99, v) {
		t.Fatal("values differ")
	}
}

// TestMapMergePreservesEntriesUniqueToSelf verifies that Merge retains unique receiver entries.
func TestMapMergePreservesEntriesUniqueToSelf(t *testing.T) {
	t.Parallel()

	a := collection.NewMap(map[string]int{
		"x": 1, "y": 2,
	})
	b := collection.NewMap(map[string]int{
		"z": 3,
	})

	merged := a.Merge(b)

	if !merged.Has("y") {
		t.Fatal("expected true")
	}
}

// TestMapMergeAddsEntriesUniqueToOther verifies that Merge adds unique other entries.
func TestMapMergeAddsEntriesUniqueToOther(t *testing.T) {
	t.Parallel()

	a := collection.NewMap(map[string]int{
		"x": 1,
	})
	b := collection.NewMap(map[string]int{
		"z": 3,
	})

	merged := a.Merge(b)

	if !merged.Has("z") {
		t.Fatal("expected true")
	}

	if !reflect.DeepEqual(3, merged.Items()["z"]) {
		t.Fatal("values differ")
	}
}

// TestMapLazyYieldsAllEntries verifies that Lazy yields every map entry.
func TestMapLazyYieldsAllEntries(t *testing.T) {
	t.Parallel()

	input := map[string]int{
		"a": 1, "b": 2, "c": 3,
	}
	m := collection.NewMap(input)

	lazy := m.Lazy()

	if !reflect.DeepEqual(input, lazy.Items()) {
		t.Fatal("values differ")
	}
}

// TestInvertSwapsKeysAndValues verifies that Invert exchanges map keys and values.
func TestInvertSwapsKeysAndValues(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2,
	})

	inverted := collection.Invert(m)

	if !reflect.DeepEqual(map[int]string{
		1: "a", 2: "b",
	}, inverted.Items()) {
		t.Fatal("values differ")
	}
}

// TestInvertLastWriteWinsOnValueCollision verifies that Invert keeps the last collision.
func TestInvertLastWriteWinsOnValueCollision(t *testing.T) {
	t.Parallel()

	// Two keys with the same value — inverted map has one entry.
	m := collection.NewMap(map[string]int{
		"a": 1, "b": 1,
	})

	inverted := collection.Invert(m)

	if !reflect.DeepEqual(1, inverted.Len()) {
		t.Fatal("values differ")
	}

	if !inverted.Has(1) {
		t.Fatal("expected true")
	}
}

// TestMapMarshalJSONProducesObject verifies that MarshalJSON encodes an object.
func TestMapMarshalJSONProducesObject(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1, "b": 2,
	})

	data, err := json.Marshal(m)

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

// TestMapUnmarshalJSONFromObject verifies that UnmarshalJSON decodes an object.
func TestMapUnmarshalJSONFromObject(t *testing.T) {
	t.Parallel()

	data := []byte(`{"x":10,"y":20}`)
	var m collection.Map[string, int]

	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(map[string]int{
		"x": 10, "y": 20,
	}, m.Items()) {
		t.Fatal("values differ")
	}
}

// TestMapUnmarshalJSONInvalidReturnsError verifies that UnmarshalJSON rejects invalid input.
func TestMapUnmarshalJSONInvalidReturnsError(t *testing.T) {
	t.Parallel()

	var m collection.Map[string, int]

	err := json.Unmarshal([]byte(`not json`), &m)

	if err == nil {
		t.Fatal("expected an error")
	}
}

// TestMapMarshalJSONToEncodesAsObject verifies that MarshalJSONTo encodes an object.
func TestMapMarshalJSONToEncodesAsObject(t *testing.T) {
	t.Parallel()

	m := collection.NewMap(map[string]int{
		"a": 1,
	})
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)

	if err := m.MarshalJSONTo(enc); err != nil {
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

// TestMapUnmarshalJSONFromDecodesFromDecoder verifies that UnmarshalJSONFrom decodes a decoder.
func TestMapUnmarshalJSONFromDecodesFromDecoder(t *testing.T) {
	t.Parallel()

	data := []byte(`{"p":7,"q":8}`)
	dec := jsontext.NewDecoder(bytes.NewReader(data))

	var m collection.Map[string, int]

	if err := m.UnmarshalJSONFrom(dec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(map[string]int{
		"p": 7, "q": 8,
	}, m.Items()) {
		t.Fatal("values differ")
	}
}
