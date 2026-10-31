package collection

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"iter"
	"maps"
)

// LazyMap is a lazily-evaluated map collection backed by an [iter.Seq2].
// Its nil zero value cannot be traversed. Traversals invoke the source anew;
// replayability and resource cleanup belong to the source. Unlike a Go map,
// the sequence may contain duplicate keys and has the source's ordering.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy()
//	for key, value := range stock {
//		fmt.Println(key, value) // apples 3
//	}
type LazyMap[K comparable, V any] func(yield func(K, V) bool)

// NewLazyMap creates a new [LazyMap] from the given iterator.
// It retains seq and its captures without invoking them. Nil is not an empty
// iterator and panics on traversal. The source must honor false from yield and
// release its own resources; blocking work needs source-owned cancellation.
//
// Example:
//
//	stock := collection.NewLazyMap(maps.All(map[string]int{"apples": 3}))
//	fmt.Println(stock.Items()["apples"]) // 3
func NewLazyMap[K comparable, V any](seq iter.Seq2[K, V]) LazyMap[K, V] {
	return LazyMap[K, V](seq)
}

// Eager materializes the lazy map into an eager [Map].
// It consumes the finite source now and owns new outer map storage. The last
// yielded value for a duplicate key wins; values are shallow copies.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy().Eager()
//	fmt.Println(stock.Items()["apples"]) // 3
func (lazyMapping LazyMap[K, V]) Eager() Map[K, V] {
	return NewMap(lazyMapping.Items())
}

// Items materializes all entries from the iterator into a Go map.
// It consumes the finite source now and returns an allocated map, even for no
// entries. The last duplicate key wins; values themselves are shallow copies.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy().Items()
//	fmt.Println(stock["apples"]) // 3
func (lazyMapping LazyMap[K, V]) Items() map[K]V {
	return maps.Collect(lazyMapping.Iter())
}

// ItemsAs materializes all entries from the iterator into the given map type.
// Like Items, it owns new outer storage, including for empty input, and retains
// the last value for each duplicate key.
//
// Example:
//
//	type Stock map[string]int
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy().ItemsAs[Stock]()
//	fmt.Println(stock["apples"]) // 3
func (lazyMapping LazyMap[K, V]) ItemsAs[M ~map[K]V]() M {
	return lazyMapping.Items()
}

// Iter returns the underlying iterator over map entries.
// It consumes nothing and preserves ordering, duplicate keys and replayability.
// A nil receiver produces a nil iterator, not an iterable empty sequence.
//
// Example:
//
//	for key, value := range collection.NewMap(map[string]int{"apples": 3}).Lazy().Iter() {
//		fmt.Println(key, value) // apples 3
//	}
func (lazyMapping LazyMap[K, V]) Iter() iter.Seq2[K, V] {
	return iter.Seq2[K, V](lazyMapping)
}

// Try returns a [TryLazyMap] backed by the entries in the lazy map.
// It consumes nothing until iteration and attaches nil errors without changing
// source ordering, duplicate keys or replayability.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Lazy().Try().Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock["apples"]) // 3
func (lazyMapping LazyMap[K, V]) Try() TryLazyMap[K, V] {
	return NewTryLazyMap(func(yield func(MapEntry[K, V], error) bool) {
		for k, v := range lazyMapping {
			if !yield(NewMapEntry(k, v), nil) {
				return
			}
		}
	})
}

// IsEmpty reports whether the iterator yields no entries.
// It consumes at most one entry and can advance a single-use source.
//
// Example:
//
//	fmt.Println(collection.NewMap[string, int](nil).Lazy().IsEmpty()) // true
func (lazyMapping LazyMap[K, V]) IsEmpty() bool {
	for range lazyMapping {
		return false
	}

	return true
}

// IsNotEmpty reports whether the iterator yields at least one entry.
// It consumes at most one entry and stops upstream as soon as one is found.
//
// Example:
//
//	fmt.Println(collection.NewMap(map[string]int{"apples": 3}).Lazy().IsNotEmpty()) // true
func (lazyMapping LazyMap[K, V]) IsNotEmpty() bool {
	return !lazyMapping.IsEmpty()
}

// Len returns the total number of entries yielded by the iterator.
// It fully consumes the sequence.
// Duplicate keys count separately, so Len can exceed the materialized map size.
//
// Example:
//
//	fmt.Println(collection.NewMap(map[string]int{"apples": 3}).Lazy().Len()) // 1
func (lazyMapping LazyMap[K, V]) Len() int {
	count := 0

	for range lazyMapping {
		count++
	}

	return count
}

// Every reports whether f returns true for every entry yielded by the iterator.
// Returns true on empty.
// It consumes entries now and stops at the first false predicate result.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy()
//	positive := stock.Every(func(_ string, value int) bool {
//		return value > 0
//	})
//	fmt.Println(positive) // true
func (lazyMapping LazyMap[K, V]) Every(f func(K, V) bool) bool {
	for k, v := range lazyMapping {
		if !f(k, v) {
			return false
		}
	}

	return true
}

// Each calls f for every entry yielded by the iterator.
// It consumes the finite source now, synchronously and in source order.
//
// Example:
//
//	collection.NewMap(map[string]int{"apples": 3}).Lazy().Each(func(key string, value int) {
//		fmt.Println(key, value) // apples 3
//	})
func (lazyMapping LazyMap[K, V]) Each(f func(K, V)) {
	for k, v := range lazyMapping {
		f(k, v)
	}
}

// TapEach returns a [LazyMap] that calls f on each entry as it is consumed,
// then yields the same entry unchanged.
// Work runs only during iteration; effects repeat if the source is replayed.
//
// Example:
//
//	total := 0
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy().TapEach(func(_ string, value int) {
//		total += value
//	})
//	fmt.Println(total) // 0
//	stock.Items()
//	fmt.Println(total) // 3
func (lazyMapping LazyMap[K, V]) TapEach(f func(K, V)) LazyMap[K, V] {
	return NewLazyMap(func(yield func(K, V) bool) {
		for k, v := range lazyMapping {
			f(k, v)

			if !yield(k, v) {
				return
			}
		}
	})
}

// Contains reports whether any entry satisfies f.
// It consumes entries now, stops at the first match and returns false on empty.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy()
//	found := stock.Contains(func(_ string, value int) bool {
//		return value == 3
//	})
//	fmt.Println(found) // true
func (lazyMapping LazyMap[K, V]) Contains(f func(K, V) bool) bool {
	for k, v := range lazyMapping {
		if f(k, v) {
			return true
		}
	}

	return false
}

// HasAny reports whether any of the given keys exists in the lazy map.
// It stops at the first matching entry. No requested keys returns false without
// invoking the source; values have no effect on key presence.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 0}).Lazy()
//	fmt.Println(stock.HasAny("pears", "apples")) // true
func (lazyMapping LazyMap[K, V]) HasAny(keys ...K) bool {
	if len(keys) == 0 {
		return false
	}

	wanted := mapKeySet(keys)

	for key := range lazyMapping {
		if _, ok := wanted[key]; ok {
			return true
		}
	}

	return false
}

// Filter returns a [LazyMap] containing only entries for which f returns true.
// Filtering runs during iteration, preserves source order and duplicates, and
// forwards retained values without deep copying.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3, "pears": 0}).Lazy()
//	available := stock.Filter(func(_ string, value int) bool {
//		return value > 0
//	})
//	fmt.Println(available.Items()["apples"]) // 3
func (lazyMapping LazyMap[K, V]) Filter(f func(K, V) bool) LazyMap[K, V] {
	return NewLazyMap(func(yield func(K, V) bool) {
		for k, v := range lazyMapping {
			if !f(k, v) {
				continue
			}

			if !yield(k, v) {
				return
			}
		}
	})
}

// Reject returns a [LazyMap] containing only entries for which f returns false.
// Filtering runs during iteration and preserves the source iterator's order.
//
// Example:
//
//	odd := collection.NewMap(map[string]int{"a": 1, "b": 2}).Lazy().Reject(func(_ string, value int) bool {
//		return value%2 == 0
//	}).Items()
//	_ = odd["a"] // 1
func (lazyMapping LazyMap[K, V]) Reject(f func(K, V) bool) LazyMap[K, V] {
	return lazyMapping.Filter(func(k K, v V) bool {
		return !f(k, v)
	})
}

// Only returns a [LazyMap] containing only the entries whose keys were given.
// The requested keys are captured independently at the method call. Iteration
// filters in source order and retains duplicate matching entries.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3, "pears": 2}).Lazy().Only("apples")
//	fmt.Println(stock.Items()["apples"]) // 3
func (lazyMapping LazyMap[K, V]) Only(keys ...K) LazyMap[K, V] {
	wanted := make(map[K]struct{}, len(keys))

	for _, key := range keys {
		wanted[key] = struct{}{}
	}

	return lazyMapping.Filter(func(k K, v V) bool {
		_, ok := wanted[k]

		return ok
	})
}

// Except returns a [LazyMap] containing all entries except those whose keys were given.
// It snapshots the key selection now, then filters during iteration without
// deep-copying values. Missing requested keys have no effect.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3, "pears": 2}).Lazy().Except("pears")
//	fmt.Println(stock.Items()["apples"]) // 3
func (lazyMapping LazyMap[K, V]) Except(keys ...K) LazyMap[K, V] {
	blocked := make(map[K]struct{}, len(keys))

	for _, key := range keys {
		blocked[key] = struct{}{}
	}

	return lazyMapping.Filter(func(k K, v V) bool {
		_, ok := blocked[k]

		return !ok
	})
}

// MapValues transforms values using f and returns a new [LazyMap] with the same keys.
// The callback runs during iteration; ordering and duplicate keys are preserved.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy()
//	labels := stock.MapValues(func(_ string, value int) string {
//		return strconv.Itoa(value)
//	})
//	fmt.Println(labels.Items()["apples"]) // 3
func (lazyMapping LazyMap[K, V]) MapValues[W any](f func(K, V) W) LazyMap[K, W] {
	return NewLazyMap(func(yield func(K, W) bool) {
		for k, v := range lazyMapping {
			if !yield(k, f(k, v)) {
				return
			}
		}
	})
}

// MapKeys transforms keys using f and returns a new [LazyMap] with the same values.
// When multiple keys map to the same new key, the last one in iteration order wins upon materialization.
// Rewriting occurs during iteration and does not itself discard duplicates.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy()
//	upper := stock.MapKeys(func(key string, _ int) string {
//		return strings.ToUpper(key)
//	})
//	fmt.Println(upper.Items()["APPLES"]) // 3
func (lazyMapping LazyMap[K, V]) MapKeys[J comparable](f func(K, V) J) LazyMap[J, V] {
	return NewLazyMap(func(yield func(J, V) bool) {
		for k, v := range lazyMapping {
			if !yield(f(k, v), v) {
				return
			}
		}
	})
}

// LazyKeys returns a [LazySlice] yielding all keys from the given [LazyMap].
// It consumes nothing until iteration, preserves duplicate keys and assigns
// dense output indices from zero without changing source replayability.
//
// Example:
//
//	keys := collection.LazyKeys(collection.NewMap(map[string]int{"apples": 3}).Lazy())
//	fmt.Println(keys.Items()) // [apples]
func LazyKeys[K comparable, V any](lazyMapping LazyMap[K, V]) LazySlice[K] {
	return NewLazySlice(func(yield func(int, K) bool) {
		index := 0

		for k := range lazyMapping {
			if !yield(index, k) {
				return
			}

			index++
		}
	})
}

// LazyValues returns a [LazySlice] yielding all values from the given [LazyMap].
// It consumes nothing until iteration and assigns dense output indices from
// zero. Values retain source order and are not deep-copied.
//
// Example:
//
//	values := collection.LazyValues(collection.NewMap(map[string]int{"apples": 3}).Lazy())
//	fmt.Println(values.Items()) // [3]
func LazyValues[K comparable, V any](lazyMapping LazyMap[K, V]) LazySlice[V] {
	return NewLazySlice(func(yield func(int, V) bool) {
		index := 0

		for _, v := range lazyMapping {
			if !yield(index, v) {
				return
			}

			index++
		}
	})
}

// Take returns a [LazyMap] that yields at most n entries. If n<=0, yields nothing.
// Nonpositive limits do not invoke the source. Duplicate keys count separately;
// a positive limit stops upstream immediately after its last selected entry.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy().Take(1)
//	fmt.Println(stock.Items()["apples"]) // 3
func (lazyMapping LazyMap[K, V]) Take(n int) LazyMap[K, V] {
	return NewLazyMap(func(yield func(K, V) bool) {
		lazyMapTake(lazyMapping, n, yield)
	})
}

// Skip returns a [LazyMap] that skips the first n entries.
// It runs during iteration in source order; nonpositive n skips nothing.
// Duplicate keys count as separate positions, rather than distinct map keys.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy().Skip(1)
//	fmt.Println(stock.Len()) // 0
func (lazyMapping LazyMap[K, V]) Skip(n int) LazyMap[K, V] {
	return NewLazyMap(func(yield func(K, V) bool) {
		skipped := 0

		for k, v := range lazyMapping {
			if skipped < n {
				skipped++

				continue
			}

			if !yield(k, v) {
				return
			}
		}
	})
}

// Unique returns a [LazyMap] that yields only the first entry for each key,
// deduplicating by key.
// A fresh seen-key set is created for each traversal. The first value wins,
// unlike ordinary materialization, which keeps the last duplicate value.
//
// Example:
//
//	first := collection.NewMap(map[string]int{"apples": 3}).Lazy()
//	last := collection.NewMap(map[string]int{"apples": 4}).Lazy()
//	fmt.Println(first.Concat(last).Unique().Items()["apples"]) // 3
func (lazyMapping LazyMap[K, V]) Unique() LazyMap[K, V] {
	return NewLazyMap(func(yield func(K, V) bool) {
		seen := make(map[K]struct{})

		for k, v := range lazyMapping {
			if _, exists := seen[k]; exists {
				continue
			}

			seen[k] = struct{}{}

			if !yield(k, v) {
				return
			}
		}
	})
}

// Concat returns a [LazyMap] that yields all entries from this map
// followed by all entries from other.
// Sources run during iteration. Stopping in the first prevents invoking other;
// duplicate keys are retained until a later deduplication or materialization.
//
// Example:
//
//	first := collection.NewMap(map[string]int{"apples": 3}).Lazy()
//	last := collection.NewMap(map[string]int{"apples": 4}).Lazy()
//	fmt.Println(first.Concat(last).Items()["apples"]) // 4
func (lazyMapping LazyMap[K, V]) Concat(other LazyMap[K, V]) LazyMap[K, V] {
	return NewLazyMap(func(yield func(K, V) bool) {
		if !yieldLazyMap(lazyMapping, yield) {
			return
		}

		yieldLazyMap(other, yield)
	})
}

// mapKeySet returns a set containing keys.
// It copies key membership into an independent map, so edits to the input slice
// cannot change a deferred selection. Duplicate keys collapse into one entry.
func mapKeySet[K comparable](keys []K) map[K]struct{} {
	set := make(map[K]struct{}, len(keys))

	for _, key := range keys {
		set[key] = struct{}{}
	}

	return set
}

// lazyMapTake yields no more than n entries from lazyMapping.
// Nonpositive limits do not invoke the source. Positive limits count duplicate
// keys separately and stop upstream immediately when the limit is reached.
func lazyMapTake[K comparable, V any](lazyMapping LazyMap[K, V], n int, yield func(K, V) bool) {
	if n <= 0 {
		return
	}

	lazyMapTakePositive(lazyMapping, n, yield)
}

// lazyMapTakePositive yields exactly up to a positive entry limit.
// The caller validates n. Source exhaustion or downstream refusal ends traversal
// without reading one extra entry beyond the limit.
func lazyMapTakePositive[K comparable, V any](lazyMapping LazyMap[K, V], n int, yield func(K, V) bool) {
	for k, v := range lazyMapping {
		if !yield(k, v) {
			return
		}

		n--

		if n == 0 {
			return
		}
	}
}

// yieldLazyMap passes entries from lazyMapping to yield until either is exhausted.
// It returns false only when the consumer refuses an entry, allowing Concat to
// avoid starting its second source. Entry storage is not copied deeply.
func yieldLazyMap[K comparable, V any](lazyMapping LazyMap[K, V], yield func(K, V) bool) bool {
	for k, v := range lazyMapping {
		if !yield(k, v) {
			return false
		}
	}

	return true
}

// MarshalJSONTo materializes the iterator and encodes it as a JSON object into enc.
// It consumes the finite source now, keeping the last duplicate value. Encoding
// and writer errors are returned rather than changing the source iterator.
//
// Example:
//
//	var encoded bytes.Buffer
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy()
//	if err := stock.MarshalJSONTo(jsontext.NewEncoder(&encoded)); err != nil {
//		panic(err)
//	}
//	fmt.Println(encoded.Len() > 0) // true
func (lazyMapping LazyMap[K, V]) MarshalJSONTo(enc *jsontext.Encoder) error {
	return json.MarshalEncode(enc, lazyMapping.Items())
}

// UnmarshalJSONFrom decodes a JSON object from dec into the lazy map.
// Only successful decoding replaces the source. The replacement is map-backed
// and replayable, with unspecified map order; errors leave the old source intact.
//
// Example:
//
//	var stock collection.LazyMap[string, int]
//	err := stock.UnmarshalJSONFrom(jsontext.NewDecoder(strings.NewReader(`{"apples":3}`)))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock.Items()["apples"]) // 3
func (lazyMapping *LazyMap[K, V]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var items map[K]V

	if err := json.UnmarshalDecode(dec, &items); err != nil {
		return err
	}

	*lazyMapping = NewLazyMap(maps.All(items))

	return nil
}

// MarshalJSON implements [encoding/json.Marshaler] for v1 compatibility.
// When used with [encoding/json/v2], [LazyMap.MarshalJSONTo] takes precedence.
// It consumes the finite source now and returns JSON v2 encoding errors.
//
// Example:
//
//	data, err := collection.NewMap(map[string]int{"apples": 3}).Lazy().MarshalJSON()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(string(data)) // {"apples":3}
func (lazyMapping LazyMap[K, V]) MarshalJSON() ([]byte, error) {
	return json.Marshal(lazyMapping.Items())
}

// UnmarshalJSON implements [encoding/json.Unmarshaler] for v1 compatibility.
// When used with [encoding/json/v2], [LazyMap.UnmarshalJSONFrom] takes precedence.
// It uses JSON v2 defaults and replaces the source only after successful decode.
//
// Example:
//
//	var stock collection.LazyMap[string, int]
//	if err := stock.UnmarshalJSON([]byte(`{"apples":3}`)); err != nil {
//		panic(err)
//	}
//	fmt.Println(stock.Items()["apples"]) // 3
func (lazyMapping *LazyMap[K, V]) UnmarshalJSON(data []byte) error {
	var items map[K]V

	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}

	*lazyMapping = NewLazyMap(maps.All(items))

	return nil
}
