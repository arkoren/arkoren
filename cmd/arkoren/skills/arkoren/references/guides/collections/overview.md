---
title: Collections Overview
nav: Overview
description: Transform, filter, group, and aggregate slices and maps with chainable generic collections, and stream large data lazily.
order: 10
---

# Collections Overview

## Introduction

To reshape application data, you might filter users, extract their names,
then sort the result. A loop or the standard library's `slices` and `maps`
packages can handle individual steps; the `collection` package lets you connect
those steps as one typed chain of calls.

It offers two kinds of collection. **Eager** collections, `Slice` and `Map`,
are ordinary Go slices and maps with methods: transformations run immediately,
while lookups and aggregations return ordinary values. **Lazy** collections, `LazySlice` and `LazyMap`, are
iterators: streaming transformations defer work until you consume the result.
They can stop early and process sources larger than memory, or even infinite
sources, as long as the chain does not need to retain or read every item.
Operations such as grouping and sorting still need the full input; see
[Lazy Collections](lazy-collections.md) for when each operation reads its source.
Each lazy kind has an error-aware form, `TryLazySlice` and `TryLazyMap`, for
sources that can fail halfway, such as a database cursor.

`collection` is a foundation module that imports only the standard library, so
it works in any Go program, with or without the rest of Arkoren.
Install it from your application's module directory:

```sh
go get arkoren.dev/foundation/collection
```

Because the module needs Go 1.27, collections can have methods that introduce
their own type parameter, such as `Map` changing the item type. That is why a
chain can change types in the middle.

## Creating collections

`NewSlice` and `NewMap` wrap an existing slice or map. These examples use your
application's `User` type with `Name`, `Age`, `Active`, and `Team` fields:

```go
import "arkoren.dev/foundation/collection"

users := collection.NewSlice([]User{
	{Name: "Ada", Age: 36, Active: true, Team: "core"},
	{Name: "Bob", Age: 17, Active: true, Team: "web"},
	{Name: "Cy", Age: 52, Active: false, Team: "core"},
})
stock := collection.NewMap(map[string]int{"apple": 3, "pear": 0, "fig": 12})
```

Wrapping copies nothing. `Slice[T]` is defined as `[]T` and `Map[K, V]` as
`map[K]V`, so you can also convert with `collection.Slice[User](users)`, range
over a collection directly, index it, and pass it to anything that takes the
underlying type after calling `Items()`. When your slice has its own named
type, use `NewSliceAs` or `NewMapAs`, and `ItemsAs` to get the named type back.

Collections use the underlying slice or map's JSON representation and decode
through `encoding/json/v2`, so a `collection.Slice[Post]` field in a response or
request body needs no special encoding helper. Most slices encode as arrays and
maps as objects; `collection.Slice[byte]` encodes as a base64 string, like
`[]byte`. A defined element type such as `type Octet uint8` instead produces an
array. [`validate.Infer`](../validation/advanced.md#describing-types-without-rules)
recognizes eager collections through their explicit `JSONSchemaType` declaration.
Inference adds no runtime rules; use the
[named-container adapter](../validation/collections.md#checking-named-containers)
to attach constraints to a collection field without changing its Go type.

## Chaining operations

Most operations take a function that receives the item's index and the item
(a map's functions receive the key and the value) and return a new collection,
so they chain. `Map` changes the item type, so `names` below is a
`Slice[string]` even though `users` holds `User` values:

```go
names := users.Filter(func(_ int, u User) bool {
	return u.Active && u.Age >= 18
}).Map(func(_ int, u User) string {
	return u.Name
})
// names is a collection.Slice[string]: [Ada]
```

Operations that end a chain return a plain value. Lookups such as `FirstWhere`
return the item and whether there was one, so an empty collection is never a
panic:

```go
if u, ok := users.FirstWhere(func(_ int, u User) bool {
	return u.Age > 50
}); ok {
	fmt.Println(u.Name) // Cy
}
```

This prints `Cy`. To fold all items into one value instead, use
[`Reduce`](available-methods.md#reduce).

`GroupBy`, `KeyBy`, and `CountBy` turn a slice into a map, and `Partition`
splits one slice in two. `Sort` sorts a copy and leaves the original in order.
A `Map` filters and rewrites the same way:

```go
available := stock.Filter(func(_ string, n int) bool {
	return n > 0
})
// map[apple:3 fig:12]
```

> [!WARNING]
> Go randomizes map iteration order, and a `Map` does not change that.
> `Keys`, `Values`, `Each`, and `Iter` visit entries in an unspecified order,
> which may differ between calls. Sort the result when order matters:
> `collection.Keys(stock).Sort(strings.Compare)`.

### Merging map overrides

To combine defaults with per-request settings, call `Merge` with the overrides:

```go
defaults := collection.NewMap(map[string]string{"theme": "light", "language": "en"})
overrides := collection.NewMap(map[string]string{"theme": "dark"})
settings := defaults.Merge(overrides)

settings["language"] = "fr"
fmt.Println(settings["theme"], defaults["language"]) // dark en
```

The argument's values win on matching keys. `Merge` returns independent outer
map storage and changes neither input, so keep its return value. Copies are
shallow: pointers, maps, and slices stored within values still refer to the
same underlying objects. Either input can be nil and is treated as empty;
you can merge entries into a zero `Map` or a map created with `NewMap(nil)`.

## Lazy and fallible collections

The transformations above are eager: each runs immediately and returns its
result. Calling `Lazy()` defers streaming steps such as `Filter` and `Map`
until you read the result, one item at a time. Combined with an early-stopping
operation such as `Take`, this avoids processing items you do not need. A
streaming chain can also read a source too large to hold in memory, provided
its operations do not require the full input.
[Lazy Collections](lazy-collections.md) explains when the difference matters.

Sources such as database cursors and files can fail halfway through. The
error-aware forms, `TryLazySlice` and `TryLazyMap`, carry an error next to
each item and stop or keep going as you choose.
[Fallible Collections](fallible-collections.md) covers them.

## Working with the standard library

Collections are plain Go underneath, so they combine with `slices`, `maps`,
and `iter` without conversion. `Items()` returns the wrapped slice or map for
any function that wants one, and `Iter()` returns an `iter.Seq2`, which
`for range` and the `iter` package accept:

```go
sorted := slices.Sorted(maps.Keys(stock.Items()))
idx := slices.IndexFunc(users.Items(), func(u User) bool {
	return u.Name == "Cy"
})
```

Going the other way, `NewLazySlice` and `NewLazyMap` wrap an iterator you
already have. A `LazyMap` takes an `iter.Seq2[K, V]` directly, so
`NewLazyMap(maps.All(m))` and any iterator from a library plugs in. For a
`LazySlice`, supply the index yourself in the function you pass.

### Aliasing and allocation

Wrapping does not copy, so a collection and the slice it was made from share
storage. Writing through one is visible through the other:

```go
raw := []int{1, 2, 3}
items := collection.NewSlice(raw)
items[0] = 99
fmt.Println(raw) // [99 2 3]
```

Methods that build a new result, such as `Filter`, `Map`, `Sort`, `Reverse`,
`Concat`, `Nth`, and `Merge`, return independent storage. The ones that select
a portion of the slice, `Take`, `Skip`, `SkipWhile`, `SkipUntil`, and `ForPage`,
return a window onto the same array instead, which is cheaper but means a change to the
window changes the original, and a small window keeps the large array alive.
Use `slices.Clone(page.Items())` when you keep a page for a long time or modify
it.

Eager `Chunk` and `Sliding` windows share that same backing array too. Lazy
`Sliding` windows have independent slice storage. Allocated results copy
elements only: pointers, nested slices, and maps inside an element still refer
to the same underlying objects.

Eager collections have the concurrency rules of ordinary slices and maps:
reading from many goroutines is safe, and writing while anything else reads is
not. A lazy chain runs its callbacks in the goroutine that iterates it. Concurrent
traversals are safe only if the source and callbacks are safe; synchronize shared
mutable inputs and captures yourself.

### Nil and empty collections

A nil `Slice` or `Map` behaves like an empty one for every read: `Len` is zero,
`Slice.First` reports `ok == false`, and `Filter` returns an empty collection.
Slice operations that collect new values, such as `Filter`, `Map`, `FlatMap`,
`TakeWhile`, `Unique`, and each side of `Partition`, return nil when they collect
no values. Lazy slice materialization, including fallible materialization before
any success, also returns a nil slice.

This does not normalize every empty collection. Eager constructors and their
`Items` and `ItemsAs` methods preserve nil or allocated-empty input; eager
windows such as `Take` and `Skip` retain the source's representation and backing
array:

```go
empty := collection.NewSlice([]int{})
filtered := collection.NewSlice([]int{1, 3}).Filter(func(_ int, value int) bool {
	return value%2 == 0
})
fmt.Println(filtered.Items() == nil)      // true
fmt.Println(empty.Items() == nil)         // false
fmt.Println(empty.Take(0).Items() == nil) // false
```

Lazy map materializers instead return allocated outer maps, even when no
successful entries are collected. The four lazy types themselves have nil
function zero values, not iterable empty values; see
[Supplying an iterator](lazy-collections.md#supplying-an-iterator).

`Merge` can return a populated map from a nil receiver. If the receiver is nil
and the other map has no entries, the result remains nil. As with any nil Go
map, assigning an entry directly still panics; keep the populated merge result
or allocate a map before indexed writes.

Collection JSON methods use `encoding/json/v2`, including the compatibility
methods named `MarshalJSON` and `UnmarshalJSON`. With default settings, a nil
non-byte eager slice encodes as `[]` and a nil eager map as `{}`, just like
allocated empty collections. Byte-slice collections instead encode base64
strings, with an empty string for nil or empty storage. Calling a compatibility
method does not switch to legacy
encoder defaults. Use `encoding/json/v2` for ordinary `json.Marshal` and
`json.Unmarshal` calls; see the [JSON method entries](available-methods.md#marshaljson).

## Panics

A few methods reject arguments that cannot work and panic: `Chunk` and `Sliding`
with a size of zero or less, `Sliding` with a step of zero or less, and `Nth`
with a step of zero or less. These are programming errors, like a bad index, so
validate a size that comes from a request before passing it. Out-of-range
counts in `Take` and `Skip` do not panic: they clamp to the slice bounds.
`Nth` defaults its zero-based offset to zero and clamps negative offsets to
zero on eager, lazy, and fallible slices. `Nth` and `Sliding` accept positive
steps through `math.MaxInt` without wrapping the next selected position or
window start; see the [`Nth`](available-methods.md#nth) and
[`Sliding`](available-methods.md#sliding) entries.

`ForPage` composes `Skip((page-1)*perPage)` with `Take(perPage)`, so a page
past the end is empty. It does not validate page parameters or detect integer
overflow; validate untrusted page inputs before calling it.

## Available methods

Every function and method has an entry, with its availability and a tiny
example, on the [collection methods](available-methods.md) page. A name that
exists on several collection types has one entry, which says which types have
it.

- **Creating and converting:**
[`NewSlice`](available-methods.md#newslice) ·
[`NewMap`](available-methods.md#newmap) ·
[`NewSliceAs`](available-methods.md#newsliceas) ·
[`NewMapAs`](available-methods.md#newmapas) ·
[`NewLazySlice`](available-methods.md#newlazyslice) ·
[`NewLazyMap`](available-methods.md#newlazymap) ·
[`NewTryLazySlice`](available-methods.md#newtrylazyslice) ·
[`NewTryLazyMap`](available-methods.md#newtrylazymap) ·
[`NewMapEntry`](available-methods.md#newmapentry) ·
[`Lazy`](available-methods.md#lazy) ·
[`Try`](available-methods.md#try) ·
[`Eager`](available-methods.md#eager) ·
[`EagerAll`](available-methods.md#eagerall)

- **Transforming:**
[`Map`](available-methods.md#map) ·
[`FlatMap`](available-methods.md#flatmap) ·
[`MapValues`](available-methods.md#mapvalues) ·
[`MapKeys`](available-methods.md#mapkeys) ·
[`TapEach`](available-methods.md#tapeach) ·
[`Sort`](available-methods.md#sort) ·
[`Reverse`](available-methods.md#reverse) ·
[`Concat`](available-methods.md#concat) ·
[`Merge`](available-methods.md#merge)

- **Filtering:**
[`Filter`](available-methods.md#filter) ·
[`Reject`](available-methods.md#reject) ·
[`Unique`](available-methods.md#unique) ·
[`Only`](available-methods.md#only) ·
[`Except`](available-methods.md#except)

- **Slicing and windowing:**
[`Take`](available-methods.md#take) ·
[`Skip`](available-methods.md#skip) ·
[`TakeWhile`](available-methods.md#takewhile) ·
[`TakeUntil`](available-methods.md#takeuntil) ·
[`SkipWhile`](available-methods.md#skipwhile) ·
[`SkipUntil`](available-methods.md#skipuntil) ·
[`Nth`](available-methods.md#nth) ·
[`ForPage`](available-methods.md#forpage) ·
[`Chunk`](available-methods.md#chunk) ·
[`Sliding`](available-methods.md#sliding) ·
[`Partition`](available-methods.md#partition)

- **Grouping:**
[`GroupBy`](available-methods.md#groupby) ·
[`KeyBy`](available-methods.md#keyby) ·
[`CountBy`](available-methods.md#countby)

- **Retrieving and searching:**
[`First`](available-methods.md#first) ·
[`FirstWhere`](available-methods.md#firstwhere) ·
[`Last`](available-methods.md#last) ·
[`Get`](available-methods.md#get) ·
[`Has`](available-methods.md#has) ·
[`HasAny`](available-methods.md#hasany) ·
[`Contains`](available-methods.md#contains) ·
[`Every`](available-methods.md#every)

- **Aggregating:**
[`Reduce`](available-methods.md#reduce) ·
[`Len`](available-methods.md#len) ·
[`IsEmpty`](available-methods.md#isempty) ·
[`IsNotEmpty`](available-methods.md#isnotempty)

- **Map keys and values:**
[`Keys`](available-methods.md#keys) ·
[`Values`](available-methods.md#values) ·
[`LazyKeys`](available-methods.md#lazykeys) ·
[`LazyValues`](available-methods.md#lazyvalues) ·
[`Invert`](available-methods.md#invert)

- **Iterating and materializing:**
[`Items`](available-methods.md#items) ·
[`ItemsAs`](available-methods.md#itemsas) ·
[`ItemsAll`](available-methods.md#itemsall) ·
[`ItemsAsAll`](available-methods.md#itemsasall) ·
[`Iter`](available-methods.md#iter) ·
[`Each`](available-methods.md#each) ·
[`EachAll`](available-methods.md#eachall)

- **JSON:**
[`JSONSchemaType`](available-methods.md#jsonschematype) ·
[`MarshalJSON`](available-methods.md#marshaljson) ·
[`UnmarshalJSON`](available-methods.md#unmarshaljson)

## When not to use collections

Use a plain loop when the body does more than one thing, when it returns early
from the surrounding function, or when you want to handle each error where it
happens. A chain of five callbacks that each hide a decision is harder to read
than a ten-line loop. For a single operation that the standard library already
has, such as `slices.Contains` or `slices.Sort`, call it directly.

Collections also use closures for every step, so they are not the choice for a
tight inner loop over millions of numbers. And a generic collection is not a
query engine: filtering a large table in Go after loading all of it is slower
and wastes memory compared to a `WHERE` clause. Narrow the data in the query,
then use a collection to shape what comes back.

## Native Arkoren results

You can transform many Arkoren results directly without wrapping a slice first.
Choose an eager snapshot when you need to inspect data already collected; choose
a lazy source when you want to stop before reading every result:

| Result | Native collection | Scope |
| --- | --- | --- |
| Database `Select` | `Slice[T]` | All selected rows, already loaded |
| Database `Cursor` and `Each` | `TryLazySlice[T]` | Deferred row or batch traversal |
| [Pagination](../database/pagination.md) `Page.Items` and `Cursor.Items` | `Slice[T]` | Only the returned page or cursor window |
| [Storage](../file-storage/overview.md) `Walk` | `TryLazySlice[ObjectInfo]` | Object metadata across listing pages |
| [LLM](../llm/streaming.md) `Stream` and `RunStream` | `TryLazySlice[Event]` | Deferred model or agent events |
| [Router](../http-fundamentals/routing.md) `Routes` | `Slice[Route]` | Detached, registration-ordered route snapshot |
| [Memory telemetry](../observability/telemetry.md) spans, logs, and metrics | `Slice` of the corresponding record | Detached observation snapshot |
| [Scheduler](../scheduled-tasks/overview.md) `Entries` | `Slice[Entry]` | Registered entries in registration order |
| [Workflow registry](../workflows/overview.md) `Workflows` and `Steps` | `Slice` of the corresponding handler | Registered definitions sorted by name |

For example, a result from [search](../search/overview.md) keeps its total and
page position while you project the current page's documents. Given an existing
`pagination.Page[search.Hit[User]]` named `page`, import `search` from
`arkoren.dev/core/service/search` for the callback's hit type:

```go
names := page.Items.Map(func(_ int, hit search.Hit[User]) string {
	return hit.Document.Name
})
```

`names` contains only this page's names. Filtering or sorting its items does not
change the server's query, global total, or issued cursor tokens. Apply global
selection and ordering in the query before fetching the page.

Snapshots retain each producer's documented copy depth; the collection wrapper
does not create additional isolation. Lazy traversal retains the producer's
context and cleanup rules, and traversing again can issue fresh requests. In
particular, repeating an LLM stream can repeat billed work. Read
[storage listing](../file-storage/overview.md), [LLM streaming](../llm/streaming.md),
and [fallible results](fallible-collections.md#where-fallible-collections-come-from)
before consuming these sources.

HTTP streaming accepts standard iterators, so pass a fallible collection's
`Iter()` to `response.SSEJSON` or another compatible consumer. An ordinary JSON
response instead materializes a lazy collection before encoding; it does not
deliver items progressively. Queue deliveries, listener dispatch, and application
cleanup retain their own protocols rather than becoming collection traversal.

## Collections in the database service

Arkoren's database service returns collections. `Select` loads every row into
an eager `Slice`, so you can chain on the result right away:

```go
users, err := db.Select[User](ctx, query.Table("users", func(statement *query.Builder) {
	statement.Where("active", true)
}))

names := users.Map(func(_ int, u User) string {
	return u.Name
})
```

`Cursor` returns a `TryLazySlice` instead, which reads rows as you consume them
and surfaces a failing connection as an error on the item, so a very large
result never has to fit in memory:

```go
err := db.Cursor[User](ctx, query.Table("users", func(statement *query.Builder) {
	statement.OrderBy("id")
})).Each(func(_ int, u User) error {
	return sendDigest(ctx, u)
})
```

Here `sendDigest` is your application's operation for one user, not a collection
helper. `Each` stops at the first error, whether the database or your function
returned it. See [Pagination](../database/pagination.md) for
paging through results.

## Testing

Collections are values, so tests compare them with whatever you already use:
`slices.Equal(got.Items(), want)` or `maps.Equal`. For a lazy chain, test its
result by ending it with `Items()`. To check that a chain is lazy, count the
calls with `TapEach`, as shown in [Lazy Collections](lazy-collections.md), and assert both that the
count is zero before the chain is read and that it stops where expected after.

For signatures and every type, see the [`collection` API reference](https://pkg.go.dev/arkoren.dev/foundation/collection).
