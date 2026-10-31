// Package collection provides generic eager and lazy slice and map collections.
//
// # Eager collections
//
// [Slice] and [Map] wrap in-memory Go slices and maps. Their constructors and
// Items methods convert without copying, so the wrapper and the supplied or
// returned collection alias the same storage. Mutating either is observable
// through the other. Operations that allocate a result return independent
// storage; operations that select a portion of a Slice can retain its backing
// array. Copies are shallow: pointers, maps and slices within elements remain
// shared. A nil eager collection is usable as an empty collection. Constructors
// and Items preserve nil versus allocated-empty storage; newly collected slice
// results from Filter, Map, FlatMap, TakeWhile, Unique, Partition and fallible
// materialization are nil when no values are collected. Windowing operations
// retain their source's representation instead of normalizing it.
//
// Example:
//
//	numbers := collection.NewSlice([]int{1, 2, 3})
//	even := numbers.Filter(func(_ int, number int) bool {
//		return number%2 == 0
//	})
//	fmt.Println(even.Items()) // [2]
//
// # Lazy collections
//
// [LazySlice] and [LazyMap] are iterator function types. Transformations such
// as Filter and Map compose lazy collections and run when the result is
// iterated. They can be ranged over directly, and Items or Eager materializes
// all yielded values into a Go collection or eager wrapper. A lazy collection
// invokes its underlying iterator for each traversal. It is replayable only
// when that iterator is replayable; callers must not assume that a traversal
// can be repeated or that it has no side effects.
// The zero value of a lazy collection is a nil function, not an empty iterator;
// traversing it panics. Use an empty eager collection's Lazy or Try conversion
// to obtain an iterable empty sequence. Iterator constructors retain functions
// and their captures without copying or invoking them.
//
// Streaming operations stop upstream as soon as a downstream yield returns
// false. Sources must honor that signal and release their own resources before
// returning. The package starts no goroutines and supplies no context or timeout;
// a source doing blocking work must capture and observe its caller's context.
// Materializers and aggregations require a finite source. Reverse and Sort read
// the whole source on each traversal; Partition consumes it at the method call.
//
// LazySlice callbacks receive the index from their source sequence. LazySlice
// transformations yield dense output indices, starting at zero, even when
// filtering, expanding, reordering, or combining source items.
//
// [TryLazySlice] and [TryLazyMap] are the error-aware lazy forms. They yield a
// value and an error, and a value yielded with a non-nil error must be ignored.
// Their Items and Eager methods stop at the first error and return values
// collected before it. ItemsAll and EagerAll consume the full sequence, retain
// successful values, and join yielded errors. Like the non-error lazy forms,
// their iteration and replayability depend on their underlying iterator.
// Streaming fallible transformations forward source and callback errors in
// order; continuing iteration after an error continues the source if it permits
// that. They do not recover panics. Take, Skip, Nth, Chunk and Sliding count only
// successful values; forwarded errors do not consume positions or limits.
//
// TryLazySlice callbacks start at index zero. Source errors do not advance the
// index. Streaming callbacks advance it only after the callback succeeds, even
// when a predicate rejects a value or FlatMap expands it to no values. A callback
// error therefore leaves the next callback at the same index. EachAll instead
// counts every successful source value, including one whose callback fails.
// Callbacks in later steps see the successful input positions of those steps,
// not stable positions in the original source.
//
// Slice callbacks receive an index, while map callbacks receive keys and
// values. Map iteration order follows the underlying iterator and is not
// guaranteed for Go maps.
// Lazy maps can yield duplicate keys: materialization keeps the last successful
// value, while Unique keeps the first. Len counts yielded entries, not distinct
// keys. Concurrent reads follow the source and callback's own safety rules;
// shared mutable inputs and captures require caller synchronization.
//
// JSON methods use encoding/json/v2, including the compatibility methods named
// MarshalJSON and UnmarshalJSON. Nil eager non-byte slices and maps encode as
// [] and {} by default, not the legacy encoder's null. Streaming JSON encoding
// and decoding return their underlying encoding, decoding or writer errors. Lazy decoding
// replaces the iterator only on success; eager decoding follows the underlying
// decoder's merging and partial-update behavior. Use json.Marshal and
// json.Unmarshal for ordinary calls; the *JSONTo and *JSONFrom methods implement
// the corresponding streaming interfaces.
// Slice.JSONSchemaType and Map.JSONSchemaType declare their identical native
// container types through a structural reflect.Type protocol. Schema tools can
// preserve element and value metadata without a dependency on collection.
// Byte slices retain native base64 string encoding, including through this
// declaration. Lazy collections do not declare a native schema shape.
package collection
