---
title: Collection Methods
description: Look up collection operations by task, including receiver forms, small examples, and consequential differences.
order: 40
summary:
  - Find constructors and operations by the selection, transformation, or lookup you need.
  - Compare eager, lazy, and fallible forms without losing timing, ownership, or error behavior.
  - Look up native-container conversions and JSON methods, including their interface variants.
---

# Collection Methods

Use this reference to choose an operation for an existing collection. Start
with the [overview](overview.md) for eager usage, [Lazy Collections](lazy-collections.md)
for evaluation timing, or [Fallible Collections](fallible-collections.md) for
error policy.

Availability labels use **E** for eager `Slice` or `Map`, **L** for `LazySlice`
or `LazyMap`, and **T** for `TryLazySlice` or `TryLazyMap`. Streaming L and T
operations defer work until traversal. Terminal calls consume immediately;
buffering exceptions are stated beside the method. T callbacks that can fail
return an additional `error`, and terminal methods return an additional error.
Unless named `All`, terminals stop at the first error. Failed yielded values
must be ignored; callbacks do not receive source errors.

Import `arkoren.dev/foundation/collection`. Entries use these independent
application values and predicates:

```go
type Order struct {
	ID    string
	Zone  string
	Units int
	Ready bool
}

orders := collection.NewSlice([]Order{
	{ID: "ord-41", Zone: "north", Units: 3, Ready: true},
	{ID: "ord-42", Zone: "south", Units: 1, Ready: false},
	{ID: "ord-43", Zone: "north", Units: 2, Ready: true},
})
capacity := collection.NewMap(map[string]int{"north": 8, "south": 0})
ready := func(_ int, order Order) bool {
	return order.Ready
}
zone := func(_ int, order Order) string {
	return order.Zone
}
```

Examples are focused fragments, not consecutive declarations in one scope.
Map result comments describe entries, not a guaranteed display order. A slice
callback receives `(index, item)`; a map callback receives `(key, value)`.
Fallible callback indexes follow the [success rules](fallible-collections.md#limits-and-indexes),
not stable source identities.

## Creating Collections

- [`NewSlice`](#newslice), [`NewMap`](#newmap): wrap in-memory containers.
- [`NewSliceAs`](#newsliceas), [`NewMapAs`](#newmapas): accept named containers.
- [`NewLazySlice`](#newlazyslice), [`NewLazyMap`](#newlazymap): wrap infallible iterators.
- [`NewTryLazySlice`](#newtrylazyslice), [`NewTryLazyMap`](#newtrylazymap), [`NewMapEntry`](#newmapentry): expose fallible sources.

### `NewSlice`

`NewSlice` wraps a `[]T` without copying: `collection.NewSlice([]Order{{ID: "ord-44"}})`.
The wrapper, original slice, and [`Items`](#items) result share storage.

### `NewMap`

`NewMap` wraps a `map[K]V` without copying:
`collection.NewMap(map[string]int{"north": 8})`. Keys must be comparable.
Nil eager maps support reads but still panic on indexed writes.

### `NewSliceAs`

`NewSliceAs` accepts a named slice type. With `type Orders []Order`, use
`collection.NewSliceAs(Orders{{ID: "ord-44"}})`. It returns `Slice[Order]`
without copying; [`ItemsAs`](#itemsas) converts back to a named type.

### `NewMapAs`

`NewMapAs` is the named-map counterpart of [`NewMap`](#newmap). With
`type Capacity map[string]int`, use `collection.NewMapAs(Capacity{"north": 8})`.

### `NewLazySlice`

`NewLazySlice` retains an `(index, item)` iterator. For a standard-library
`slices` import, `collection.NewLazySlice(slices.All(orders.Items()))` wraps
the same source data without consuming it.

Supply a non-nil function for all four iterator constructors. Nil is not an
empty iterator and panics on traversal. Sources must honor false yields and
own their [cleanup and cancellation](lazy-collections.md#source-ownership).

### `NewLazyMap`

`NewLazyMap` retains an `iter.Seq2[K, V]`. With the standard-library `maps`
package, `collection.NewLazyMap(maps.All(capacity.Items()))` wraps a Go map
with unspecified order. It does not copy or traverse it.

### `NewTryLazySlice`

`NewTryLazySlice` retains an `(item, error)` iterator. A one-record source is:

```go
source := collection.NewTryLazySlice(func(yield func(Order, error) bool) {
	yield(Order{ID: "ord-44"}, nil)
})
```

No work runs yet. `source.Items()` returns that order and nil error. A source
that yields more records must stop after a false yield.

### `NewTryLazyMap`

`NewTryLazyMap` retains an iterator over `(MapEntry, error)` pairs:

```go
source := collection.NewTryLazyMap(func(yield func(collection.MapEntry[string, int], error) bool) {
	yield(collection.NewMapEntry("north", 8), nil)
})
```

`source.Items()` returns `north: 8` and nil error. Ignore entries paired with
non-nil errors.

### `NewMapEntry`

`NewMapEntry` constructs a typed pair: `collection.NewMapEntry("north", 8)`
has `Key == "north"` and `Value == 8`. The equivalent struct is
`collection.MapEntry[string, int]{Key: "north", Value: 8}`.

## Changing Evaluation

- [`Lazy`](#lazy), [`Try`](#try): defer later work or permit errors.
- [`Eager`](#eager), [`EagerAll`](#eagerall): collect reusable eager results.

### `Lazy`

*Slice, Map · E*

`Lazy` returns an iterator over shared source data. For example,
`orders.Lazy().Take(1).Items()` collects only `ord-41`. Conversion does not
copy elements, run callbacks, or cache a result.

### `Try`

*Slice, Map · E L*

`Try` exposes successful values through the fallible lazy form. For example,
`orders.Try().Items()` returns the orders and nil error. Later transformation
callbacks can return errors; conversion itself consumes nothing.

### `Eager`

*Slice, Map · L T*

`Eager` collects into an eager wrapper: `orders.Lazy().Take(1).Eager()` returns
a `Slice[Order]` containing `ord-41`. T returns `(collection, error)` and keeps
only the successful prefix before the first error. Outer result storage is new;
element copies remain shallow.

### `EagerAll`

*Slice, Map · T*

`EagerAll` is the continuing counterpart of [`Eager`](#eager):
`snapshot, err := orders.Try().EagerAll()`. It retains successes and joins
errors with `errors.Join`; it cannot retry or resume a terminal source.

## Selecting Records

- [`Filter`](#filter), [`Reject`](#reject), [`Unique`](#unique): select by values.
- [`Only`](#only), [`Except`](#except): select by map keys.

### `Filter`

*Slice, Map · E L T*

`Filter` retains matching values in source order: `orders.Filter(ready)` keeps
`ord-41` and `ord-43`. E allocates independent shallow result storage. On T,
source and predicate errors are forwarded, not filtered out.

### `Reject`

*Slice, Map · E L T*

`Reject` retains values for which the predicate is false:
`orders.Reject(ready)` keeps `ord-42`. It is the inverse selection of
[`Filter`](#filter), not an alias; T still forwards errors unchanged.

### `Unique`

*Slice · E L T; Map · L T*

`Unique` keeps the first slice item for each comparable callback key:
`orders.Unique(zone)` keeps `ord-41` and `ord-42`. Lazy forms retain a seen-key
set per traversal. On lazy maps, `capacity.Lazy().Unique()` takes no callback
and keeps the first entry per key. Materialization without `Unique` keeps the
last duplicate instead.

### `Only`

*Map · E L T*

`Only` retains listed keys: `capacity.Only("north")` contains `north: 8`.
T forwards all source errors, even if the associated key would be excluded.

### `Except`

*Map · E L T*

`Except` excludes listed keys: `capacity.Except("south")` contains `north: 8`.
Like [`Only`](#only), it does not hide source errors on T.

## Reshaping Values

- [`Map`](#map), [`FlatMap`](#flatmap), [`MapValues`](#mapvalues), [`MapKeys`](#mapkeys): change value or key types.
- [`Sort`](#sort), [`Reverse`](#reverse), [`Concat`](#concat), [`Merge`](#merge): order or combine collections.

### `Map`

*Slice · E L T*

`Map` transforms each item and can change its type:

```go
ids := orders.Map(func(_ int, order Order) string {
	return order.ID
})
```

`ids` contains `ord-41`, `ord-42`, and `ord-43`, in order. T callbacks return
`(value, error)` instead; a failed conversion yields an error rather than a value.

### `FlatMap`

*Slice · E L T*

`FlatMap` expands each item into zero or more values, preserving source and
expansion order:

```go
labels := orders.Take(1).FlatMap(func(_ int, order Order) []string {
	return []string{order.ID, order.Zone}
})
```

`labels` contains `ord-41` and `north`. A nil or empty expansion contributes
no values. T callbacks return an additional error.

### `MapValues`

*Map · E L T*

`MapValues` retains keys and transforms values, possibly to a new type:

```go
available := capacity.MapValues(func(_ string, slots int) bool {
	return slots > 0
})
```

`available` contains `north: true` and `south: false`.

### `MapKeys`

*Map · E L T*

`MapKeys` retains values and transforms keys. With `strings` imported:

```go
labels := capacity.MapKeys(func(name string, _ int) string {
	return strings.ToUpper(name)
})
```

`labels` contains `NORTH: 8` and `SOUTH: 0`. Colliding keys keep the last
materialized value. A Go map's unspecified order makes that winner unspecified;
lazy sources can retain duplicates until materialization.

### `Sort`

*Slice · E L T*

`Sort` sorts a shallow copy using a comparison function. With `cmp` imported:

```go
sorted := orders.Sort(func(left, right Order) int {
	return cmp.Compare(left.Units, right.Units)
})
```

`sorted` contains `ord-42`, `ord-43`, then `ord-41`; `orders` keeps its order.
The comparator must define a strict weak ordering. Sorting is not stable.
L and T read the whole source on each traversal before yielding; T yields only
the error if materialization fails, discarding the prefix.

### `Reverse`

*Slice · E L T*

`Reverse` reverses a shallow copy: `orders.Reverse()` begins with `ord-43`.
L and T buffer the whole source on each traversal; T discards partial results
if source materialization fails, as with [`Sort`](#sort).

### `Concat`

*Slice · E L T; Map · L T*

`Concat` appends another collection of the same form:
`orders.Take(1).Concat(orders.Skip(2))` contains `ord-41`, then `ord-43`.
E copies outer storage. Lazy forms traverse the first source before the second;
early stop can avoid the second source entirely. Lazy-map duplicate keys remain
until materialization, where later entries win. Eager maps use [`Merge`](#merge).

### `Merge`

*Map · E*

`Merge` gives the argument precedence in independent outer storage:
`capacity.Merge(collection.NewMap(map[string]int{"south": 4}))` contains
`north: 8` and `south: 4`, without changing either input. Nested references
remain shared. Nil inputs count as empty; a nil receiver merged with no entries
stays nil. Keep the return value rather than expecting receiver mutation.

## Windowing Records

- [`Take`](#take), [`Skip`](#skip), [`TakeWhile`](#takewhile), [`TakeUntil`](#takeuntil), [`SkipWhile`](#skipwhile), [`SkipUntil`](#skipuntil): select prefixes or suffixes.
- [`Nth`](#nth), [`ForPage`](#forpage), [`Chunk`](#chunk), [`Sliding`](#sliding), [`Partition`](#partition): select positions or groups.

### `Take`

*Slice · E L T; Map · L T*

`Take` keeps at most a count: `orders.Take(2)` contains the first two orders.
E shares the source array and clamps to bounds. Nonpositive limits produce no
items and do not invoke lazy sources, including no source errors on T.
Positive T limits count successes, not errors; duplicate map entries count
separately. Reaching the last requested success stops upstream immediately.

### `Skip`

*Slice · E L T; Map · L T*

`Skip` discards a prefix: `orders.Skip(2)` contains `ord-43`. Nonpositive
counts skip nothing; E clamps counts and shares the source array. T counts
successes while forwarding errors even inside skipped positions.

### `TakeWhile`

*Slice · E L T*

`TakeWhile` retains a prefix until the predicate is false:
`orders.TakeWhile(ready)` contains `ord-41`. It consumes but excludes the
first false item. E copies the selected prefix; T errors do not end selection
if the consumer continues.

### `TakeUntil`

*Slice · E L T*

`TakeUntil` retains a prefix until the predicate is true:
`orders.TakeUntil(ready)` is empty because the first order matches. The matching
item is excluded. It uses [`TakeWhile`](#takewhile) with the predicate inverted;
errors are forwarded, not inverted.

### `SkipWhile`

*Slice · E L T*

`SkipWhile` drops the leading matches: `orders.SkipWhile(ready)` contains
`ord-42` and `ord-43`. It includes the first false item and does not evaluate
the predicate on the rest. E shares the source array; T errors leave the
skipping phase unchanged.

### `SkipUntil`

*Slice · E L T*

`SkipUntil` drops items until the predicate is true:
`orders.SkipUntil(ready)` contains all three orders. It includes the matching
item and uses [`SkipWhile`](#skipwhile) with the predicate inverted. E shares
the source array.

### `Nth`

*Slice · E L T*

`Nth` samples every nth position: `orders.Nth(2)` contains `ord-41` and
`ord-43`; `orders.Nth(2, 1)` contains `ord-42`. The optional zero-based offset
defaults to zero; negative offsets clamp to zero. Offsets beyond the source
select nothing. The step must be positive or the call panics.

Every positive `int` step, including `math.MaxInt`, is valid without wrapping.
E allocates shallow result storage and returns nil for no selections. L counts
positions rather than source-supplied indexes and yields dense indexes. T
counts only successes and forwards errors even within skipped positions.
Continued traversal resumes selection progress; stopping avoids the next gap.

### `ForPage`

*Slice · E L T*

`ForPage` selects a one-based page: `orders.ForPage(2, 2)` contains `ord-43`.
It composes `Skip((page-1)*perPage)` and `Take(perPage)` without validating
arguments or overflow. Validate positive inputs and a representable product
before use. E shares storage; T forwards errors in skipped positions. This
selects collection positions, not [database result pages](../queries/pagination.md).

### `Chunk`

*Slice · E L T*

`Chunk` forms consecutive groups: `orders.Chunk(2)` returns groups of two and
one orders. The size must be positive or the call panics. E returns
`[]Slice[T]` whose windows share the source array. L returns
`iter.Seq2[int, []T]`; T returns `iter.Seq2[[]T, error]`. Lazy chunks own
independent shallow storage and include the final short chunk. T errors leave
the partial chunk unchanged if traversal continues; stopping on an error does
not flush that prefix as a chunk.

### `Sliding`

*Slice · E L T*

`Sliding` forms complete windows: `orders.Sliding(2)` returns windows
`[ord-41, ord-42]` and `[ord-42, ord-43]`. An optional step defaults to one;
`orders.Sliding(2, 3)` returns only the first window. Incomplete final windows
are omitted. Nonpositive sizes or steps panic; positive steps through
`math.MaxInt` do not wrap the next start.

Return types match [`Chunk`](#chunk). E windows alias the source and each other
where they overlap; lazy windows own independent shallow storage. T counts
only successes, forwarding errors even in gaps without changing window or gap
progress. Stopping on a window or error stops consumption before the next gap.

### `Partition`

*Slice · E L T*

`Partition` splits matching and remaining items:
`selected, rest := orders.Partition(ready)` gives two ready orders and one
other order. E owns both shallow result slices. L and T consume at the call,
not on later traversal, and buffer replayable results. T returns two fallible
iterators: on a source or predicate error, both yield that error and no partial
partition values.

## Grouping Records

- [`GroupBy`](#groupby), [`KeyBy`](#keyby), [`CountBy`](#countby): retain groups, one item per key, or counts.

### `GroupBy`

*Slice · E L T*

`GroupBy` retains ordered groups: `orders.GroupBy(zone)["north"]` contains
`ord-41` and `ord-43`. Group slices and the outer map own shallow storage;
outer-map order is unspecified. L and T consume immediately. T returns
`(groups, error)` and preserves successful prefix groups on failure.

### `KeyBy`

*Slice · E L T*

`KeyBy` retains the last item for each key: `orders.KeyBy(zone)["north"]`
is `ord-43`. It allocates a new outer map. L and T consume immediately;
T returns its prefix map and the first error.

### `CountBy`

*Slice · E L T*

`CountBy` counts observed keys: `orders.CountBy(zone)` contains `north: 2`
and `south: 1`. Absent map keys read as zero. L and T consume immediately;
T returns prefix counts with the first error.

## Finding Answers

- [`First`](#first), [`FirstWhere`](#firstwhere), [`Last`](#last), [`Get`](#get): retrieve a value and an absence flag.
- [`Has`](#has), [`HasAny`](#hasany), [`Contains`](#contains), [`Every`](#every): test membership or conditions.
- [`Reduce`](#reduce), [`Len`](#len), [`IsEmpty`](#isempty), [`IsNotEmpty`](#isnotempty): summarize values or presence.

### `First`

*Slice · E L T*

`First` retrieves the initial item: `order, found := orders.First()` gives
`ord-41` and true. Empty input returns zero and false without panic. L consumes
at most one item; T returns `(item, found, error)` and stops even if the first
result is an error.

### `FirstWhere`

*Slice · E L T*

`FirstWhere` retrieves the first match:
`order, found := orders.FirstWhere(ready)` gives `ord-41` and true.
No match returns zero and false. Lazy traversal stops at the first match;
T also stops at the first source or predicate error.

### `Last`

*Slice · E L T*

`Last` retrieves the final item: `order, found := orders.Last()` gives
`ord-43` and true. Empty input returns zero and false. L reads the whole
source; T stops at an error and returns the last successful prefix item,
its flag, and that error.

### `Get`

*Map · E*

`Get` checks a key: `slots, found := capacity.Get("south")` gives zero and
true. An absent key gives zero and false, distinguishing absence from a stored zero.

### `Has`

*Map · E*

`Has` tests key presence: `capacity.Has("south")` is true even though its
stored value is zero. Lazy maps use [`HasAny`](#hasany) for key searches.

### `HasAny`

*Map · E L T*

`HasAny` tests whether any requested key exists:
`capacity.HasAny("west", "south")` is true. Lazy forms stop at a match;
T also stops at a source error. No requested keys returns false without
traversing the source or observing its errors.

### `Contains`

*Slice, Map · E L T*

`Contains` tests a predicate: `orders.Contains(ready)` is true. It stops at
the first match, or the first source/predicate error on T. Empty input is false.
It does not accept a plain value; use standard-library `slices.Contains` on
`Items()` for direct comparable-value membership.

### `Every`

*Slice, Map · E L T*

`Every` tests all items: `orders.Every(ready)` is false. It stops at the first
false result or, on T, error. Empty input returns true without invoking the predicate.

### `Reduce`

*Slice · E L T*

`Reduce` folds items into a value of any type, starting with the initial value:

```go
units := orders.Reduce(func(total, _ int, order Order) int {
	return total + order.Units
}, 0)
```

`units` is 6; empty input returns the initial zero. L and T consume at the
call. On T, source errors return the preceding accumulator; callback errors
return the accumulator supplied by that failing callback, with its error.

### `Len`

*Slice, Map · E L T*

`Len` counts items: `orders.Len()` is 3. Lazy forms traverse the entire source;
T returns the successful prefix count and first error. Lazy-map duplicate
entries count separately, even if materializing would collapse their keys.

### `IsEmpty`

*Slice, Map · E L T*

`IsEmpty` tests absence: `orders.IsEmpty()` is false. Lazy forms read at most
one result, not the whole source. An initial T error returns false and that
error; check the error before interpreting the boolean.

### `IsNotEmpty`

*Slice, Map · E L T*

`IsNotEmpty` negates [`IsEmpty`](#isempty): `orders.IsNotEmpty()` is true.
On T, an initial error returns true with that error, not proof of a successful
item. Always check its error first.

## Reading Map Parts

- [`Keys`](#keys), [`Values`](#values), [`LazyKeys`](#lazykeys), [`LazyValues`](#lazyvalues), [`Invert`](#invert): extract or reverse key/value roles.

### `Keys`

*Map · E · Package Function*

`Keys` collects keys into a `Slice`: `collection.Keys(capacity)` contains
`north` and `south`, in unspecified order. With `strings` imported,
`collection.Keys(capacity).Sort(strings.Compare)` gives repeatable order.

### `Values`

*Map · E · Package Function*

`Values` collects values into a `Slice`: `collection.Values(capacity)`
contains 8 and 0 in unspecified order. It does not establish an ordering
relationship with a separate [`Keys`](#keys) traversal.

### `LazyKeys`

*Map · L T*

`LazyKeys` projects keys without consuming now. On L,
`collection.LazyKeys(capacity.Lazy())` returns `LazySlice[string]`. On T,
`capacity.Try().LazyKeys()` is a method returning `iter.Seq2[string, error]`,
not a collection wrapper. Both preserve duplicates and source order.

### `LazyValues`

*Map · L T*

`LazyValues` is the value projection corresponding to [`LazyKeys`](#lazykeys):
`collection.LazyValues(capacity.Lazy())` returns `LazySlice[int]`;
`capacity.Try().LazyValues()` returns `iter.Seq2[int, error]`.
Ignore any key or value paired with a non-nil error.

### `Invert`

*Map · E · Package Function*

`Invert` swaps comparable keys and values: `collection.Invert(capacity)`
contains `8: north` and `0: south`. If values collide, the retained original
key is unspecified because Go-map iteration order is unspecified.

## Consuming Results

- [`Items`](#items), [`ItemsAs`](#itemsas), [`ItemsAll`](#itemsall), [`ItemsAsAll`](#itemsasall): extract or materialize containers.
- [`Iter`](#iter), [`Each`](#each), [`EachAll`](#eachall), [`TapEach`](#tapeach): consume or observe items.

### `Items`

*Slice, Map · E L T*

`Items` returns the native container: `orders.Items()` is a `[]Order` sharing
the eager source's storage. E preserves nil versus allocated-empty storage.
L and T materialize new shallow outer storage; T returns a prefix with the first
error. Slice materialization with no successes is nil; map materialization is
allocated even when empty. Repeated lazy calls repeat traversal, not cached data.

### `ItemsAs`

*Slice, Map · E L T*

`ItemsAs` is [`Items`](#items) with a named container result. With
`type Orders []Order`, use `orders.ItemsAs[Orders]()`. It has the same eager
aliasing, lazy allocation, and T first-error rules.

### `ItemsAll`

*Slice, Map · T*

`ItemsAll` retains successes and joins errors:
`items, err := orders.Try().ItemsAll()`. It reads the full finite source while
continuation is possible. It does not retry failed reads. Empty successful
slice output is nil; map output is allocated.

### `ItemsAsAll`

*Slice, Map · T*

`ItemsAsAll` is [`ItemsAll`](#itemsall) returning a named container. With
`type Orders []Order`, use `items, err := orders.Try().ItemsAsAll[Orders]()`.
Errors still accompany any collected successes.

### `Iter`

*Slice, Map · E L T*

`Iter` returns a standard `iter.Seq2` without consuming now:
`orders.Iter()` yields `(index, Order)`. Maps yield `(key, value)`.
T slices yield `(item, error)` and T maps `(MapEntry, error)`. Lazy collections
can also be ranged over directly. Iterator conversion changes no replay or
ownership guarantees.

### `Each`

*Slice, Map · E L T*

`Each` consumes items for side effects. With `fmt` imported:

```go
orders.Take(1).Each(func(_ int, order Order) {
	fmt.Println(order.ID)
})
```

This prints `ord-41`. T callbacks return an error; `Each` returns the first
source or callback error. Earlier side effects are not rolled back.

### `EachAll`

*Slice, Map · T*

`EachAll` continues after source or callback errors and joins them:

```go
err := orders.Try().EachAll(func(_ int, order Order) error {
	fmt.Println(order.ID)
	return nil
})
```

It prints all three IDs and returns nil. On fallible slices its callback index
advances for every successful source value, even if that callback fails.
Source errors bypass the callback and do not advance its index. Continuation
cannot make a terminal source recover.

### `TapEach`

*Slice, Map · E L T*

`TapEach` observes items while passing them along unchanged:

```go
observed := orders.Lazy().TapEach(func(_ int, order Order) {
	fmt.Println(order.ID)
})
```

Nothing prints until traversal. E instead calls the observer immediately and
returns the original collection, not a copy. On T, callback failures replace
their value with an error and do not advance that streaming callback's index.

## Encoding JSON

- [`JSONSchemaType`](#jsonschematype): declare eager wire shapes.
- [`MarshalJSON`](#marshaljson), [`MarshalJSONTo`](#marshaljsonto): encode containers.
- [`UnmarshalJSON`](#unmarshaljson), [`UnmarshalJSONFrom`](#unmarshaljsonfrom): decode containers.

Use `encoding/json/v2` for ordinary `json.Marshal` and `json.Unmarshal` calls.
The compatibility method names below still use JSON v2 behavior, not legacy
encoding defaults.

### `JSONSchemaType`

*Slice, Map · E*

`JSONSchemaType` declares the identical native container type as a
`reflect.Type`: `orders.JSONSchemaType()` is `reflect.TypeFor[[]Order]()`.
Schema tools use that declaration for [output descriptions](../validation/json-schema.md#describe-output),
including nested element metadata and byte slices' base64 shape. It adds no
runtime rules; use the [named-container adapter](../validation/collections.md#named-containers)
for logical validation. Lazy types do not declare this protocol.

### `MarshalJSON`

*Slice, Map · E L T*

`MarshalJSON` is the compatibility encoding method. Prefer ordinary encoding,
for example `data, err := json.Marshal(capacity.Only("north"))`, whose successful
result is `{"north":8}`. Default nil non-byte slices encode as `[]`, maps as
`{}`, and byte slices as base64 strings, with `""` for empty storage.

Lazy forms materialize the whole finite source; encoding is not progressive
item delivery. T returns the first source error instead of encoding a partial
collection. Encoding errors also remain visible.

### `MarshalJSONTo`

*Slice, Map · E L T*

`MarshalJSONTo` implements the JSON v2 streaming interface. With a configured
`*jsontext.Encoder` named `encoder`, call `err := orders.MarshalJSONTo(encoder)`.
Encoder options control formatting; encoding and writer errors are returned.
Lazy forms still materialize first, and T writes no collection data if source
materialization fails. For ordinary use, prefer [`MarshalJSON`](#marshaljson)'s
`json.Marshal` example.

### `UnmarshalJSON`

*Slice, Map · E L T · Pointer Receiver*

`UnmarshalJSON` is the compatibility decoding method. Prefer `json.Unmarshal`:

```go
var decoded collection.Map[string, int]
err := json.Unmarshal([]byte(`{"north":8}`), &decoded)
```

On success, `decoded["north"]` is 8. E follows JSON v2 merging rules, and a
decoding error can leave partial receiver updates. Lazy receivers replace their
iterator only after successful decoding, installing a replayable source over
the entire decoded container. T decoded values carry nil source errors. Lazy
decoding is not streaming input traversal.

### `UnmarshalJSONFrom`

*Slice, Map · E L T · Pointer Receiver*

`UnmarshalJSONFrom` implements JSON v2 decoding from a `*jsontext.Decoder`.
Given a decoder named `decoder` and the eager map `decoded` above, call
`err := decoded.UnmarshalJSONFrom(decoder)`. Receiver updates and failure rules
match [`UnmarshalJSON`](#unmarshaljson). Both streaming interface types come
from `encoding/json/jsontext`.

For exact declarations, see the [`collection` API reference](https://pkg.go.dev/arkoren.dev/foundation/collection).
