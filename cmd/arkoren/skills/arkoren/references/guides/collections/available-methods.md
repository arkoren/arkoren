---
title: Available Collection Methods
nav: Available Methods
description: A short entry for every function and method of the collection package, with the types it works on and a tiny example.
order: 40
---

# Available Collection Methods

This page lists every function and method of the
[`collection` package](overview.md), one entry per name. Most names exist
on several collection types and behave the same way, so an entry says where it
is available and only notes what differs.

Each entry starts with an availability line: the family it belongs to, `Slice`
or `Map`, then which forms have it.

- **E** is the eager form: `Slice` or `Map`. It runs immediately.
- **L** is the lazy form: `LazySlice` or `LazyMap`. Streaming transformations
  run as you consume the result. Terminal operations and methods such as
  `Partition` and `GroupBy` consume the source when called; their entries
  identify these exceptions.
- **T** is the error-aware lazy form: `TryLazySlice` or `TryLazyMap`. Its
  callbacks return an extra `error`, its methods that end a chain return an
  extra `error`. Ordinary terminal methods stop at the first error; the `All`
  variants collect successes and join errors. See
  [Collections that can fail](fallible-collections.md).

Slice callbacks receive `(index, item)`; map callbacks receive `(key, value)`.
On `TryLazySlice`, streaming callback indices advance only when the callback
succeeds; source and callback errors leave the next callback at the same index.
`EachAll` instead counts every successful source value, even when its callback
fails. See [Callback indexes after errors](fallible-collections.md#callback-indexes-after-errors).

The examples use these values, and show results as comments. Grouping examples
also use the `users` and `User` from the [overview](overview.md#creating-collections):

```go
nums := collection.NewSlice([]int{1, 2, 3, 4, 5, 6})
words := collection.NewSlice([]string{"apple", "fig", "banana", "fig", "kiwi"})
stock := collection.NewMap(map[string]int{"apple": 3, "pear": 0, "fig": 12})
even := func(_ int, n int) bool {
	return n%2 == 0
}
```

## Creating and converting

### `NewSlice`

Wraps an existing `[]T` as a `Slice[T]` without copying. `NewMap` does the
same for a `map[K]V`.

```go
nums := collection.NewSlice([]int{1, 2, 3}) // Slice[int]
```

### `NewMap`

Wraps an existing `map[K]V` as a `Map[K, V]` without copying. See [`NewSlice`](#newslice).

### `NewSliceAs`

*Slice · E*

Like `NewSlice`, for a slice with its own named type such as `type IDs []int`.
`ItemsAs` converts back to that type. `NewMapAs` is the map counterpart.

### `NewMapAs`

Like [`NewMap`](#newmap), for a map with its own named type. See [`NewSliceAs`](#newsliceas).

### `NewLazySlice`

Builds a `LazySlice` from an iterator function that yields an index and an
item. You supply the index yourself. The function and its captures are retained,
not invoked or copied. A nil function is not an empty iterator and panics on
traversal. The same prerequisite applies to the other three iterator constructors;
see [Supplying an iterator](lazy-collections.md#supplying-an-iterator) for empty
sequences, cleanup, and cancellation responsibilities.

```go
letters := collection.NewLazySlice(func(yield func(int, string) bool) {
	for i, s := range []string{"a", "b"} {
		if !yield(i, s) {
			return
		}
	}
})
letters.Items() // [a b]
```

### `NewLazyMap`

Builds a `LazyMap` from an `iter.Seq2[K, V]`, so `maps.All(m)` and any library
iterator plugs in directly.

### `NewTryLazySlice`

Builds a `TryLazySlice` from a function that yields `(item, error)`. A
`yield(item, err)` with a non-nil error reports a failure; the item is then
ignored.

### `NewTryLazyMap`

Builds a `TryLazyMap` from a function that yields `(MapEntry, error)`.

### `NewMapEntry`

Builds the `MapEntry[K, V]` that a `TryLazyMap` yields: `NewMapEntry("a", 1)`,
or the literal `MapEntry[string, int]{Key: "a", Value: 1}`.

### `Lazy`

*Slice, Map · E*

Returns the lazy form of an eager collection. Streaming steps after it defer
work until the result is read; operations that materialize the source still
consume it, as described in their entries.

```go
nums.Lazy().Filter(even).Take(1).Items() // [2]
```

### `Try`

*Slice, Map · E L*

Returns the error-aware lazy form, so the following steps can return errors.
It is how you feed an in-memory collection into a chain that can fail.

### `Eager`

*Slice, Map · L T*

Reads a lazy collection to the end and returns an eager `Slice` or `Map` you
can keep chaining on. On `T` it returns `(collection, error)` and stops at the
first error, handing back what it had read.

```go
nums.Lazy().Take(2).Eager() // [1 2]
```

### `EagerAll`

*Slice, Map · T*

Like `Eager`, but reads to the end, keeps every item that succeeded, and joins
all errors with `errors.Join`.

## Transforming

### `Map`

*Slice · E L T*

Transforms each item and returns a collection of the new type, which is how a
chain changes types.

```go
nums.Map(func(_ int, n int) string {
	return strconv.Itoa(n * n)
}) // [1 4 9 16 25 36]
```

### `FlatMap`

*Slice · E L T*

Maps each item to a slice and flattens the results into one collection.

```go
nums.Take(3).FlatMap(func(_ int, n int) []int {
	return []int{n, n * 10}
}) // [1 10 2 20 3 30]
```

### `MapValues`

*Map · E L T*

Rewrites every value and keeps the keys. The value type may change.

```go
stock.MapValues(func(_ string, n int) bool {
	return n > 0
}) // map[apple:true fig:true pear:false]
```

### `MapKeys`

*Map · E L T*

Rewrites every key and keeps the values. When materialized entries share a key,
the last yielded value wins. For eager maps or a lazy source backed by an ordinary
Go map, traversal order is unspecified, so you cannot predict that winner. A
lazy pipeline can yield repeated keys before materialization; use `Unique()`
on that lazy map when you want the first yielded value instead.

```go
stock.MapKeys(func(k string, _ int) string {
	return strings.ToUpper(k)
}) // map[APPLE:3 FIG:12 PEAR:0]
```

### `TapEach`

*Slice, Map · E L T*

Calls a function on each item and passes the item through unchanged. Use it
for logging or counting. On `L` and `T` it runs as items are read, not when
the chain is built. The eager form returns the original collection, not a copy.

```go
nums.Take(2).TapEach(func(_ int, n int) {
	fmt.Println(n)
}) // prints 1 and 2, returns [1 2]
```

### `Sort`

*Slice · E L T*

Returns a sorted copy using a comparison function like `slices.SortFunc`. The
original keeps its order. Lazy forms read and sort the whole source on each
traversal; they do not cache the sorted result.

```go
words.Sort(strings.Compare) // [apple banana fig fig kiwi]
```

### `Reverse`

*Slice · E L T*

Returns the items in reverse order. Lazy forms read the whole source first.

```go
nums.Take(3).Reverse() // [3 2 1]
```

### `Concat`

*Slice · E L T; Map · L T*

Returns this collection followed by another of the same type. On a lazy map,
a key present in both takes the later value once materialized. An eager `Map`
has `Merge` instead.

```go
nums.Take(2).Concat(collection.NewSlice([]int{9})) // [1 2 9]
```

### `Merge`

*Map · E*

`Merge` combines two maps into a new one; the argument's value wins on a shared
key, regardless of map iteration order. Neither input is changed.

```go
merged := stock.Merge(collection.NewMap(map[string]int{"fig": 1, "plum": 4}))
// merged is map[apple:3 fig:1 pear:0 plum:4]; stock["fig"] is still 12.
```

The result has independent outer map storage, even when just one input has
entries. Values are copied shallowly: nested pointers, maps, and slices remain
shared. A nil receiver or argument is treated as empty, so a zero `Map` and
`NewMap(nil)` can receive entries through the returned result. If the receiver
is nil and the argument has no entries, the result remains nil; an allocated
empty receiver stays allocated. See [Merging map overrides](overview.md#merging-map-overrides).

## Filtering

### `Filter`

*Slice, Map · E L T*

Keeps the items for which the function returns true.

```go
nums.Filter(even) // [2 4 6]
```

### `Reject`

*Slice, Map · E L T*

The inverse of `Filter`: drops the items for which the function returns true.

```go
nums.Reject(even) // [1 3 5]
```

### `Unique`

*Slice · E L T; Map · L T*

On a slice, keeps the first item for each key the function returns. The lazy
forms of a map deduplicate by key, which only matters for sources that can
repeat a key.

```go
words.Unique(func(_ int, s string) string {
	return s
}) // [apple fig banana kiwi]
```

### `Only`

*Map · E L T*

Keeps only the listed keys.

```go
stock.Only("apple", "fig") // map[apple:3 fig:12]
```

### `Except`

*Map · E L T*

Drops the listed keys.

```go
stock.Except("pear") // map[apple:3 fig:12]
```

## Slicing and windowing

Out-of-range counts never panic: `Take` and `Skip` clamp, and a page past the
end is empty. `Take` and `Skip` on an eager `Slice` return a window onto the
same array; see [Aliasing and allocation](overview.md#aliasing-and-allocation).

### `Take`

*Slice, Map · E L T (map: L T)*

Keeps the first `n` items. Zero or negative `n` produces an empty result without
consuming a lazy source, including no errors on the fallible form. Positive
fallible limits count only successful values or entries, not forwarded errors.
Lazy map entries with duplicate keys count separately. Consumption stops at the
last requested success or when you stop iterating, whichever comes first.

```go
nums.Take(2) // [1 2]
```

### `Skip`

*Slice, Map · E L T (map: L T)*

Drops the first `n` items. Zero or negative `n` drops nothing. On the fallible
form, only successful values or entries consume skipped positions; errors are
forwarded even within that skipped prefix. Duplicate map keys count as separate
entries. Continuing after an error preserves the remaining skipped count.

```go
nums.Skip(4) // [5 6]
```

### `TakeWhile`

*Slice · E L T*

Keeps items from the start until the function first returns false.

```go
nums.TakeWhile(func(_ int, n int) bool {
	return n < 4
}) // [1 2 3]
```

### `TakeUntil`

*Slice · E L T*

Keeps items from the start until the function first returns true.

```go
nums.TakeUntil(func(_ int, n int) bool {
	return n > 3
}) // [1 2 3]
```

### `SkipWhile`

*Slice · E L T*

Drops items from the start until the function first returns false, then keeps
the rest.

```go
nums.SkipWhile(func(_ int, n int) bool {
	return n < 4
}) // [4 5 6]
```

### `SkipUntil`

*Slice · E L T*

Drops items from the start until the function first returns true, then keeps
the rest.

```go
nums.SkipUntil(func(_ int, n int) bool {
	return n > 3
}) // [4 5 6]
```

### `Nth`

*Slice · E L T*

`Nth` keeps every `n`th item, starting at an optional zero-based offset. The
offset defaults to zero, and negative offsets are clamped to zero on all three
forms. An offset at or beyond the source length selects no items. It panics if
`n` is zero or less.

```go
nums.Nth(2)     // [1 3 5]
nums.Nth(2, 1)  // [2 4 6]
nums.Nth(2, -1) // [1 3 5]
```

Every positive `int` step is valid, including `math.MaxInt` from the standard
library's `math` package. For example, `nums.Nth(math.MaxInt, 1)` selects only
`[2]`; the next position does not wrap into the source. An empty eager result
is nil, and a nonempty eager result has independent slice storage with shallow
element copies.

Lazy forms defer source consumption until iteration and stop immediately when
you stop iterating. Selection counts source positions, not supplied iterator
indices; `LazySlice` yields dense output indices from zero. On `TryLazySlice`,
only successful items count toward the offset and step. Errors are forwarded
in order, even while skipping items. If the source can continue, continuing
after an error resumes the same selection. An error's accompanying value must
be ignored.

### `ForPage`

*Slice · E L T*

Returns one page for a one-based page number and page size.
It composes `Skip((page-1)*perPage)` with `Take(perPage)`; it does not normalize
request parameters or detect integer overflow. Validate untrusted page inputs
before calling it. A nonpositive page size produces no items.

```go
nums.ForPage(2, 4) // [5 6]
```

### `Chunk`

*Slice · E L T*

Splits the items into consecutive groups of a size. The eager form returns
`[]Slice[T]`, sharing the input's backing array. `LazySlice` returns
`iter.Seq2[int, []T]`; `TryLazySlice` returns `iter.Seq2[[]T, error]`.
The final group may be shorter. It panics if the size is zero or less.

```go
nums.Chunk(4) // [[1 2 3 4] [5 6]]
```

### `Sliding`

*Slice · E L T*

`Sliding` returns complete windows of a size, advancing their starts by an
optional step (default one). A step larger than the size skips items between
windows.
Incomplete windows at the end are dropped. It panics if the size or step
is zero or less. The eager form returns `[]Slice[T]`, the lazy forms an
`iter.Seq2` with the same index/error distinction as [`Chunk`](#chunk).

```go
nums.Sliding(3)    // [[1 2 3] [2 3 4] [3 4 5] [4 5 6]]
nums.Sliding(3, 2) // [[1 2 3] [3 4 5]]
nums.Sliding(2, 3) // [[1 2] [4 5]]
```

Positive steps through `math.MaxInt` are valid without wrapping the next window
start. For example, `nums.Sliding(1, math.MaxInt)` returns only `[[1]]`.
If no complete window fits, the eager result is nil and the lazy forms yield
no windows.

Eager windows share the input's backing array, including overlapping items.
Lazy windows copy elements into independent slice storage, but pointers, maps,
and slices inside elements still refer to the source's underlying objects.
The lazy forms consume nothing until iteration. On the fallible form, only
successful items count toward the size and step. Errors are forwarded
in order, including in skipped gaps, without changing a partial window or the
remaining gap. Continuing after an error resumes windowing; ignore the window
yielded with that error. Continuation still depends on the source having more
results. Stopping iteration on a window or an error stops
source consumption immediately, without reading the next gap.

### `Partition`

*Slice · E L T*

Splits a slice in two: the items that satisfy the function, then the rest. On
lazy forms it reads the whole source when called, not when the results are read.
On `T`, a source or predicate error makes both returned collections yield that
error; they do not expose a partial partition.

```go
evens, odds := nums.Partition(even) // [2 4 6] [1 3 5]
```

## Grouping

### `GroupBy`

*Slice · E L T*

Groups items by a key into a `Map[K, Slice[T]]`, keeping the original order
within each group. Lazy forms read the whole source when called.

```go
teams := users.GroupBy(func(_ int, u User) string {
	return u.Team
})
teams["core"].Len() // 2
```

### `KeyBy`

*Slice · E L T*

Builds a `Map` from a key you choose per item. If two items give the same key,
the later one wins.

```go
names := users.KeyBy(func(_ int, u User) string {
	return u.Name
})
names["Bob"].Age // 17
```

### `CountBy`

*Slice · E L T*

Counts items per key into a `Map[K, int]`.

```go
users.CountBy(func(_ int, u User) string {
	return u.Team
}) // map[core:2 web:1]
```

## Retrieving and searching

### `First`

*Slice · E L T*

Returns the first item and whether there was one, so an empty collection never
panics. On `T` it returns `(item, ok, err)`.

```go
n, ok := nums.First() // 1 true
```

### `FirstWhere`

*Slice · E L T*

Returns the first item that satisfies the function, and whether one did.

```go
n, ok := nums.FirstWhere(func(_ int, n int) bool {
	return n > 3
}) // 4 true
```

### `Last`

*Slice · E L T*

Returns the last item and whether there was one. Lazy forms read the whole
source.

```go
n, ok := nums.Last() // 6 true
```

### `Get`

*Map · E*

Returns the value for a key and whether the key exists.

```go
n, ok := stock.Get("fig") // 12 true
```

### `Has`

*Map · E*

Reports whether the key exists.

```go
stock.Has("kiwi") // false
```

### `HasAny`

*Map · E L T*

Reports whether at least one of the keys exists.

```go
stock.HasAny("kiwi", "pear") // true
```

### `Contains`

*Slice, Map · E L T*

Reports whether any item satisfies a function. It takes a condition, not a
value; for a plain value use `slices.Contains` on `Items()`. Lazy forms stop
at the first match.

```go
nums.Contains(func(_ int, n int) bool {
	return n == 4
}) // true
```

### `Every`

*Slice, Map · E L T*

Reports whether every item satisfies the function. It is true for an empty
collection.

```go
stock.Every(func(_ string, n int) bool {
	return n > 0
}) // false
```

## Aggregating and counting

### `Reduce`

*Slice · E L T*

Folds the items into one value of any type, starting from an initial value.

```go
nums.Reduce(func(sum, _ int, n int) int {
	return sum + n
}, 0) // 21
```

### `Len`

*Slice, Map · E L T*

Returns the number of items. Lazy forms read the whole source to count it.

```go
nums.Len() // 6
```

### `IsEmpty`

*Slice, Map · E L T*

Reports whether there are no items. Lazy forms read at most one item.

```go
nums.IsEmpty() // false
```

### `IsNotEmpty`

*Slice, Map · E L T*

The inverse of [`IsEmpty`](#isempty).

```go
nums.IsNotEmpty() // true
```

## Map keys and values

Getting the keys or values of an eager map uses package functions, `Keys` and
`Values`. Map iteration order is not
defined, so sort the result when order matters.

### `Keys`

*Map · E*

Returns the keys as a `Slice`.

```go
collection.Keys(stock).Sort(strings.Compare) // [apple fig pear]
```

### `Values`

*Map · E*

Returns the values as a `Slice`.

```go
collection.Values(stock).Sort(cmp.Compare[int]) // [0 3 12]
```

### `LazyKeys`

*Map · L T*

Returns the keys of a lazy map. On `L` it is the function
`collection.LazyKeys(m)` and returns a `LazySlice`; on `T` it is the method
`m.LazyKeys()` and returns an `iter.Seq2[K, error]`.

```go
collection.LazyKeys(stock.Lazy()).Len() // 3
```

### `LazyValues`

*Map · L T*

The values counterpart of `LazyKeys`: `collection.LazyValues(m)` on `L`,
`m.LazyValues()` on `T`.

### `Invert`

*Map · E*

Swaps keys and values. When several keys share a value, one of them wins and
which one is not defined.

```go
collection.Invert(collection.NewMap(map[string]int{"a": 1, "b": 2})) // map[1:a 2:b]
```

## Iterating and materializing

### `Items`

*Slice, Map · E L T*

Returns the plain Go slice or map. On `L` and `T` it reads the lazy collection
to the end; on `T` it returns `(items, error)` and stops at the first error.
Eager forms share storage and preserve nil versus allocated-empty inputs.
Lazy slice materialization returns nil if no values succeed; lazy map
materialization returns an allocated map even when empty. Materialized values
have newly owned outer storage, with shallow element copies.

```go
nums.Lazy().Take(2).Items() // []int{1, 2}
```

### `ItemsAs`

*Slice, Map · E L T*

Like `Items`, returning your own named slice or map type.

```go
ids := nums.ItemsAs[IDs]() // IDs
```

Here `IDs` is a named slice type declared as `type IDs []int`.

### `ItemsAll`

*Slice, Map · T*

Like `Items`, but reads to the end, keeps every item that succeeded, and
joins the errors. The slice result is nil when no successful values are
collected, even if errors were yielded; the map result is allocated.

### `ItemsAsAll`

*Slice, Map · T*

`ItemsAll` into your own named type.

### `Iter`

*Slice, Map · E L T*

Returns an `iter.Seq2` for `for range` and the `iter` package: `(index, item)`
or `(key, value)`. On `T`, slices yield `(item, error)` and maps yield
`(MapEntry, error)`. Map order is undefined.

```go
for i, n := range nums.Take(2).Iter() {
	fmt.Println(i, n)
}
// 0 1, then 1 2
```

### `Each`

*Slice, Map · E L T*

Calls a function for every item. On `T` the function returns an `error`, and
`Each` stops at the first error, whether the source or your function returned
it.

```go
nums.Take(2).Each(func(i int, n int) {
	fmt.Println(i, n)
}) // 0 1, then 1 2
```

### `EachAll`

*Slice, Map · T*

Like `Each`, but keeps going after an error and returns all errors joined.
On `TryLazySlice`, callback indices count successful source values, including
ones whose callback fails. Source errors do not advance that index or invoke
the callback; this differs from streaming callback index reuse after an error.

## JSON

Use `encoding/json/v2` for ordinary `json.Marshal` and `json.Unmarshal` calls.
All collection JSON methods use that package, including methods named for
compatibility with the legacy interfaces; their names do not select legacy
encoding or decoding defaults.

### `JSONSchemaType`

*Slice, Map · E*

Declares the identical native slice or map type used for default JSON encoding.
Schema tools can inspect this metadata without reading values or importing the
collection module. Import the standard-library `fmt` and `reflect` packages to
compare the type directly:

```go
fmt.Println(nums.JSONSchemaType() == reflect.TypeFor[[]int]()) // true
```

You normally let [`validate.Infer`](../validation/advanced.md#describing-types-without-rules)
read the declaration. It preserves nested element/value schemas rather than
treating eager collections as opaque custom marshalers. Byte slices retain
their base64 string shape. Lazy collections do not declare this protocol;
describing a source does not materialize or validate it. To attach runtime rules
to an eager collection field, use the
[named-container adapter](../validation/collections.md#checking-named-containers).

### `MarshalJSON`

*Slice, Map · E L T*

Every collection encodes as the slice or map it wraps; lazy forms read their
source to the end. A nil non-byte eager slice encodes as `[]` and a nil eager map
as `{}` with default settings, including through their compatibility `MarshalJSON`
methods. Byte-slice collections encode base64 strings, including an empty string
for nil or empty storage. `MarshalJSONTo` implements the JSON v2 streaming
interface and returns
encoding or writer errors. Fallible forms return the first source error instead
of encoding a partial collection.

```go
data, err := json.Marshal(nums.Take(2)) // On success, data contains [1,2].
```

### `UnmarshalJSON`

*Slice, Map · E L T*

All six collection types provide pointer-receiver `UnmarshalJSON` and its
`encoding/json/v2` counterpart, `UnmarshalJSONFrom`. Lazy forms decode the whole
array or object, then wrap the decoded data as a replayable iterator; decoding
is not streaming. Fallible forms contain successful items after decoding.
Lazy receivers replace their iterator only after successful decoding; eager
receivers follow JSON v2 decoding and merging rules, and errors can leave partial
updates. Both compatibility and streaming methods return decoding errors.

```go
var items collection.Map[string, int]
err := items.UnmarshalJSON([]byte(`{"fig":3}`))
// On success, items is map[fig:3].
```

For exact signatures, see the
[`collection` API reference](https://pkg.go.dev/arkoren.dev/foundation/collection).
