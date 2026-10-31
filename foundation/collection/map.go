package collection

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"iter"
	"maps"
	"slices"
)

// Map is an eager, in-memory map collection of key-value pairs of type K and V.
// Its nil zero value supports reads and Merge, but direct assignment needs an
// allocated map. Iteration order is unspecified; copies of values are shallow.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3})
//	fmt.Println(stock.Get("apples")) // 3 true
type Map[K comparable, V any] map[K]V

// NewMap creates a new [Map] from a plain Go map of key-value pairs.
// It shares the supplied storage and preserves nil; it does not copy entries.
//
// Example:
//
//	raw := map[string]int{"apples": 3}
//	stock := collection.NewMap(raw)
//	stock.Items()["apples"] = 4
//	fmt.Println(raw["apples"]) // 4
func NewMap[K comparable, V any](items map[K]V) Map[K, V] {
	return Map[K, V](items)
}

// NewMapAs creates a new [Map] from the given map type, preserving compatibility
// with named map types whose underlying type is map[K]V.
// No copy or allocation is made, and nil input stays nil.
//
// Example:
//
//	type Stock map[string]int
//	stock := collection.NewMapAs(Stock{"apples": 3})
//	fmt.Println(stock.Len()) // 1
func NewMapAs[M ~map[K]V, K comparable, V any](items M) Map[K, V] {
	return NewMap(map[K]V(items))
}

// Items returns the underlying map.
// Changes through the result affect the receiver. A nil receiver returns nil,
// which must be allocated before adding entries by direct assignment.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3})
//	stock.Items()["apples"] = 4
//	fmt.Println(stock.Items()["apples"]) // 4
func (mapping Map[K, V]) Items() map[K]V {
	return map[K]V(mapping)
}

// ItemsAs returns the underlying items as the given map type.
// It shares storage and preserves nil, rather than allocating a conversion copy.
//
// Example:
//
//	type Stock map[string]int
//	stock := collection.NewMap(map[string]int{"apples": 3}).ItemsAs[Stock]()
//	fmt.Println(stock["apples"]) // 3
func (mapping Map[K, V]) ItemsAs[M ~map[K]V]() M {
	return M(mapping)
}

// Iter returns an iterator over the map entries.
// Each traversal reads shared map storage in unspecified order and stops when
// the consumer stops. Nil storage yields no entries.
//
// Example:
//
//	for key, value := range collection.NewMap(map[string]int{"apples": 3}).Iter() {
//		fmt.Println(key, value) // apples 3
//	}
func (mapping Map[K, V]) Iter() iter.Seq2[K, V] {
	return maps.All(map[K]V(mapping))
}

// Lazy returns a [LazyMap] backed by the entries in the map.
// It consumes nothing and retains the map, so subsequent traversals see edits.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).Lazy()
//	fmt.Println(stock.Items()["apples"]) // 3
func (mapping Map[K, V]) Lazy() LazyMap[K, V] {
	return NewLazyMap(mapping.Iter())
}

// Try returns a [TryLazyMap] backed by the entries in the map.
// It is lazy, shares the source map and yields only nil source errors.
//
// Example:
//
//	stock, err := collection.NewMap(map[string]int{"apples": 3}).Try().Items()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock["apples"]) // 3
func (mapping Map[K, V]) Try() TryLazyMap[K, V] {
	return NewTryLazyMap(func(yield func(MapEntry[K, V], error) bool) {
		for k, v := range mapping {
			if !yield(NewMapEntry(k, v), nil) {
				return
			}
		}
	})
}

// Has reports whether the key exists in the map.
// A present zero value counts as present; a nil map contains no keys.
//
// Example:
//
//	fmt.Println(collection.NewMap(map[string]int{"apples": 0}).Has("apples")) // true
func (mapping Map[K, V]) Has(key K) bool {
	_, ok := mapping[key]

	return ok
}

// Get returns the value for the key and whether it was present.
// Missing keys return the zero V and false, including when the receiver is nil.
//
// Example:
//
//	value, found := collection.NewMap(map[string]int{"apples": 3}).Get("apples")
//	fmt.Println(value, found) // 3 true
func (mapping Map[K, V]) Get(key K) (V, bool) {
	v, ok := mapping[key]

	return v, ok
}

// HasAny reports whether any of the given keys exists in the map.
// It stops at the first present key. No requested keys returns false.
//
// Example:
//
//	found := collection.NewMap(map[string]int{"apples": 3}).HasAny("pears", "apples")
//	fmt.Println(found) // true
func (mapping Map[K, V]) HasAny(keys ...K) bool {
	return slices.ContainsFunc(keys, func(key K) bool {
		return mapping.Has(key)
	})
}

// Len returns the number of entries in the map.
// It is constant-time and returns zero for a nil map.
//
// Example:
//
//	fmt.Println(collection.NewMap(map[string]int{"apples": 3}).Len()) // 1
func (mapping Map[K, V]) Len() int {
	return len(mapping)
}

// IsEmpty reports whether the map contains no entries.
// Nil and allocated empty maps both return true.
//
// Example:
//
//	fmt.Println(collection.NewMap[string, int](nil).IsEmpty()) // true
func (mapping Map[K, V]) IsEmpty() bool {
	return len(mapping) == 0
}

// IsNotEmpty reports whether the map contains at least one entry.
// Nil and allocated empty maps both return false.
//
// Example:
//
//	fmt.Println(collection.NewMap(map[string]int{"apples": 3}).IsNotEmpty()) // true
func (mapping Map[K, V]) IsNotEmpty() bool {
	return len(mapping) > 0
}

// Each calls f for every entry in the map. Order is not guaranteed.
// Callbacks run synchronously; their panics and side effects are not intercepted.
//
// Example:
//
//	collection.NewMap(map[string]int{"apples": 3}).Each(func(key string, value int) {
//		fmt.Println(key, value) // apples 3
//	})
func (mapping Map[K, V]) Each(f func(K, V)) {
	for k, v := range mapping {
		f(k, v)
	}
}

// TapEach calls f for every entry and returns the map unchanged. Order is not guaranteed.
// It runs immediately and returns the same shared map rather than copying it.
//
// Example:
//
//	total := 0
//	stock := collection.NewMap(map[string]int{"apples": 3}).TapEach(func(_ string, value int) {
//		total += value
//	})
//	fmt.Println(total, stock.Len()) // 3 1
func (mapping Map[K, V]) TapEach(f func(K, V)) Map[K, V] {
	for k, v := range mapping {
		f(k, v)
	}

	return mapping
}

// Every reports whether f returns true for all entries. Returns true on empty.
// It stops on the first false result; unspecified order determines which entry
// can stop the traversal first.
//
// Example:
//
//	positive := collection.NewMap(map[string]int{"apples": 3}).Every(func(_ string, value int) bool {
//		return value > 0
//	})
//	fmt.Println(positive) // true
func (mapping Map[K, V]) Every(f func(K, V) bool) bool {
	for k, v := range mapping {
		if !f(k, v) {
			return false
		}
	}

	return true
}

// Contains reports whether any entry satisfies f.
// It stops at the first match and returns false for an empty map.
//
// Example:
//
//	found := collection.NewMap(map[string]int{"apples": 3}).Contains(func(_ string, value int) bool {
//		return value == 3
//	})
//	fmt.Println(found) // true
func (mapping Map[K, V]) Contains(f func(K, V) bool) bool {
	for k, v := range mapping {
		if f(k, v) {
			return true
		}
	}

	return false
}

// Filter returns a new [Map] containing only entries for which f returns true.
// The result owns its outer map, including for no matches. Values are shallow
// copies and the source map remains unchanged.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3, "pears": 0})
//	available := stock.Filter(func(_ string, value int) bool {
//		return value > 0
//	})
//	fmt.Println(available.Len()) // 1
func (mapping Map[K, V]) Filter(f func(K, V) bool) Map[K, V] {
	result := make(map[K]V, len(mapping))

	for k, v := range mapping {
		if f(k, v) {
			result[k] = v
		}
	}

	return NewMap(result)
}

// Reject returns a new [Map] containing only entries for which f returns false.
// The source map is unchanged; iteration order remains unspecified.
//
// Example:
//
//	odd := collection.NewMap(map[string]int{"a": 1, "b": 2}).Reject(func(_ string, value int) bool {
//		return value%2 == 0
//	}).Items()
//	_ = odd["a"] // 1
func (mapping Map[K, V]) Reject(f func(K, V) bool) Map[K, V] {
	return mapping.Filter(func(k K, v V) bool {
		return !f(k, v)
	})
}

// Only returns a new [Map] containing only the entries whose keys were given.
// Missing keys are ignored and no requested keys produces an allocated empty
// map. Outer storage is independent; retained values are shallow copies.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3, "pears": 2}).Only("apples")
//	fmt.Println(stock.Len(), stock.Items()["apples"]) // 1 3
func (mapping Map[K, V]) Only(keys ...K) Map[K, V] {
	result := make(map[K]V, len(keys))

	for _, key := range keys {
		if value, ok := mapping[key]; ok {
			result[key] = value
		}
	}

	return NewMap(result)
}

// Except returns a new [Map] containing all entries except those whose keys were given.
// It shallow-copies the source map before deleting requested keys. A nil
// receiver remains nil, and missing keys have no effect.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3, "pears": 2}).Except("pears")
//	fmt.Println(stock.Len()) // 1
func (mapping Map[K, V]) Except(keys ...K) Map[K, V] {
	result := maps.Clone(map[K]V(mapping))

	for _, key := range keys {
		delete(result, key)
	}

	return NewMap(result)
}

// MapValues transforms values using f and returns a new [Map] with the same keys.
// It invokes f immediately in unspecified order and owns the result's outer map.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).MapValues(func(_ string, value int) string {
//		return strconv.Itoa(value)
//	})
//	fmt.Println(stock.Items()["apples"]) // 3
func (mapping Map[K, V]) MapValues[W any](f func(K, V) W) Map[K, W] {
	result := make(map[K]W, len(mapping))

	for k, v := range mapping {
		result[k] = f(k, v)
	}

	return NewMap(result)
}

// MapKeys transforms keys using f and returns a new [Map] with the same values.
// When multiple keys map to the same new key, one of them wins; which one is
// unspecified, because map iteration order is random.
// The new map owns its outer storage; values remain shallow copies.
//
// Example:
//
//	stock := collection.NewMap(map[string]int{"apples": 3}).MapKeys(func(key string, _ int) string {
//		return strings.ToUpper(key)
//	})
//	fmt.Println(stock.Items()["APPLES"]) // 3
func (mapping Map[K, V]) MapKeys[J comparable](f func(K, V) J) Map[J, V] {
	result := make(map[J]V, len(mapping))

	for k, v := range mapping {
		result[f(k, v)] = v
	}

	return NewMap(result)
}

// rawKeys returns all keys as a plain Go slice.
// Storage is newly collected, nil on empty input, and ordering is unspecified.
func (mapping Map[K, V]) rawKeys() []K {
	return slices.Collect(maps.Keys(map[K]V(mapping)))
}

// rawValues returns all values as a plain Go slice.
// Outer storage is independently collected and nil on empty input; contained
// values are shallow copies in unspecified order.
func (mapping Map[K, V]) rawValues() []V {
	return slices.Collect(maps.Values(map[K]V(mapping)))
}

// Keys returns a [Slice] of all keys in the given [Map]. Order is not guaranteed.
// The new slice owns its storage; an empty map produces a nil slice.
//
// Example:
//
//	keys := collection.Keys(collection.NewMap(map[string]int{"apples": 3}))
//	fmt.Println(keys.Items()) // [apples]
func Keys[K comparable, V any](mapping Map[K, V]) Slice[K] {
	return NewSlice(mapping.rawKeys())
}

// Values returns a [Slice] of all values in the given [Map]. Order is not guaranteed.
// The new slice owns its outer storage; values are shallow copies. Empty input
// produces a nil slice.
//
// Example:
//
//	values := collection.Values(collection.NewMap(map[string]int{"apples": 3}))
//	fmt.Println(values.Items()) // [3]
func Values[K comparable, V any](mapping Map[K, V]) Slice[V] {
	return NewSlice(mapping.rawValues())
}

// Merge returns a new [Map] with all entries from both maps.
// When keys collide, other's values take precedence. A nil receiver or other
// map is treated as empty. Neither input map is modified, and the result's
// outer storage is independent of both inputs. Values are copied shallowly:
// pointers, maps, and slices within values remain shared.
//
// Example:
//
//	var settings collection.Map[string, string]
//	settings = settings.Merge(collection.NewMap(map[string]string{"theme": "light"}))
//	settings = settings.Merge(collection.NewMap(map[string]string{"theme": "dark"}))
//	_ = settings.Items()["theme"] // "dark"
func (mapping Map[K, V]) Merge(other Map[K, V]) Map[K, V] {
	// result owns the outer entries. Allocate only when copying needs storage
	// so an empty merge from a nil receiver preserves its nil result.
	result := maps.Clone(map[K]V(mapping))

	if result == nil && len(other) > 0 {
		result = make(map[K]V, len(other))
	}

	maps.Copy(result, map[K]V(other))

	return NewMap(result)
}

// MarshalJSONTo encodes the map as a JSON object into enc.
// It returns encoding and writer errors; the encoder controls streaming options.
//
// Example:
//
//	var encoded bytes.Buffer
//	err := collection.NewMap(map[string]int{"apples": 3}).MarshalJSONTo(jsontext.NewEncoder(&encoded))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(encoded.Len() > 0) // true
func (mapping Map[K, V]) MarshalJSONTo(enc *jsontext.Encoder) error {
	return json.MarshalEncode(enc, map[K]V(mapping))
}

// UnmarshalJSONFrom decodes a JSON object from dec into the map.
// It follows JSON v2 merge semantics. Decoding errors can leave partial updates.
//
// Example:
//
//	var stock collection.Map[string, int]
//	err := stock.UnmarshalJSONFrom(jsontext.NewDecoder(strings.NewReader(`{"apples":3}`)))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(stock.Items()["apples"]) // 3
func (mapping *Map[K, V]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return json.UnmarshalDecode(dec, (*map[K]V)(mapping))
}

// MarshalJSON implements [encoding/json.Marshaler] for v1 compatibility.
// When used with [encoding/json/v2], [Map.MarshalJSONTo] takes precedence.
// JSON v2 defaults apply, so a nil map encodes as {}, not legacy null.
//
// Example:
//
//	data, err := collection.NewMap(map[string]int{"apples": 3}).MarshalJSON()
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(string(data)) // {"apples":3}
func (mapping Map[K, V]) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[K]V(mapping))
}

// UnmarshalJSON implements [encoding/json.Unmarshaler] for v1 compatibility.
// When used with [encoding/json/v2], [Map.UnmarshalJSONFrom] takes precedence.
// JSON v2 decoding errors are returned; partial updates can remain in the map.
//
// Example:
//
//	var stock collection.Map[string, int]
//	if err := stock.UnmarshalJSON([]byte(`{"apples":3}`)); err != nil {
//		panic(err)
//	}
//	fmt.Println(stock.Items()["apples"]) // 3
func (mapping *Map[K, V]) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, (*map[K]V)(mapping))
}

// Invert returns a new [Map] with keys and values swapped.
// When multiple keys share the same value, one of them wins; which one is
// unspecified, because map iteration order is random.
// The result owns its outer storage and leaves the input map unchanged.
//
// Example:
//
//	stock := collection.Invert(collection.NewMap(map[string]int{"apples": 3}))
//	fmt.Println(stock.Items()[3]) // apples
func Invert[K, V comparable](mapping Map[K, V]) Map[V, K] {
	result := make(map[V]K, len(mapping))

	for k, v := range mapping {
		result[v] = k
	}

	return NewMap(result)
}
