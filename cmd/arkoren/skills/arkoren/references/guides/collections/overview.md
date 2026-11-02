---
title: Overview
description: Shape in-memory application records with eager slice and map collections while preserving clear ordering and ownership.
order: 10
summary:
  - Filter and project typed records, then group or aggregate the selected values.
  - Choose slices for ordered items and maps for keyed values and explicit overrides.
  - Distinguish allocated results from shared windows and preserve native result boundaries.
---

# Overview

A dispatch report often needs a subset of orders, their identifiers, and a total
for each warehouse. The `collection` package connects those operations as typed
Go calls. An eager `Slice[T]` holds ordered items; an eager `Map[K, V]` holds
values addressed by comparable keys. Their operations run when you call them.

Use collections to reshape values your application already has. They do not
translate callbacks into SQL or fetch more data. Apply database selection and
ordering before loading records, then shape the returned values in Go. For one
operation, a loop or the standard-library `slices` and `maps` packages may be
clearer than a chain.

## Creating Collections

The standalone module requires Go 1.27 and works without the rest of Arkoren.
Install it from your application's module directory:

```sh
go get arkoren.dev/foundation/collection
```

Import `arkoren.dev/foundation/collection`. For a dispatch report, wrap the
orders already loaded by your application:

```go
type Order struct {
	ID        string
	Warehouse string
	Units     int
	Ready     bool
}

orders := collection.NewSlice([]Order{
	{ID: "ord-104", Warehouse: "north", Units: 3, Ready: true},
	{ID: "ord-105", Warehouse: "south", Units: 2, Ready: false},
	{ID: "ord-106", Warehouse: "north", Units: 5, Ready: true},
})
```

`NewSlice` wraps without copying. `Slice[T]` is a defined `[]T`, so you can
index it and range over it directly. `Items()` returns the underlying `[]T`
for standard-library calls. Both the wrapper and that slice share storage.
`NewSliceAs` accepts a named slice type; `ItemsAs` converts back to one.

## Selecting Orders

Keep the ready orders before projecting their IDs. Slice callbacks receive the
input index and item; `_` makes an unused index explicit:

```go
ready := orders.Filter(func(_ int, order Order) bool {
	return order.Ready
})
ids := ready.Map(func(_ int, order Order) string {
	return order.ID
})
```

`ids` is a `collection.Slice[string]` containing `ord-104` and `ord-106`, in
that order. `Filter` and `Map` allocate result storage and leave `orders`
unchanged. Methods can introduce a new type parameter, so projection changes
the collection's element type without losing the chain.

If you need one order rather than another collection, use a lookup:

```go
order, found := orders.FirstWhere(func(_ int, order Order) bool {
	return order.Warehouse == "south"
})
```

Here `found` is true and `order.ID` is `ord-105`. If nothing matches, the result
is the zero `Order` and false; checking `found` distinguishes absence from a
stored zero value. `First` and `Last` use the same value-plus-boolean convention.

## Summarizing Dispatch

Group ready orders when each warehouse needs its own dispatch list:

```go
warehouses := ready.GroupBy(func(_ int, order Order) string {
	return order.Warehouse
})
```

`warehouses["north"]` contains the two ready orders in source order. The outer
map has no iteration-order guarantee. Use `CountBy` for the number of orders per
warehouse, or `KeyBy` for one order per identifier; repeated `KeyBy` keys keep
the last item.

To count units rather than orders, fold the selected records with `Reduce`:

```go
units := ready.Reduce(func(total, _ int, order Order) int {
	return total + order.Units
}, 0)
```

`units` is 8. The initial value is also the result for empty input, so an empty
dispatch has zero units without a separate branch.

## Working With Maps

Choose a map when the key, rather than a position, identifies each value. Map
callbacks receive `(key, value)`. For example, select warehouses with available
packing capacity:

```go
capacity := collection.NewMap(map[string]int{"north": 12, "south": 0})
available := capacity.Filter(func(_ string, slots int) bool {
	return slots > 0
})
```

`available` contains only `north: 12`. `Get` reports both a value and whether
its key exists; a stored zero is different from an absent key. `MapValues`
changes values while retaining keys, and `MapKeys` changes keys while retaining
values. Colliding transformed keys overwrite earlier entries. With a Go-map
source, the winner is unspecified because traversal order is unspecified.

For explicit overrides, `Merge` gives the argument precedence regardless of
iteration order:

```go
adjusted := capacity.Merge(collection.NewMap(map[string]int{"south": 4}))
```

`adjusted["south"]` is 4; `capacity["south"]` remains 0. The result has independent
outer map storage. Keep the returned map: `Merge` does not modify its receiver.

To display keys in a repeatable order, import `strings` and sort them:

```go
names := collection.Keys(capacity).Sort(strings.Compare)
```

`names` contains `north`, then `south`. `Keys` and `Values` are package
functions, not eager-map methods. Map traversal remains unordered; sorting
the extracted keys does not reorder the map.

## Copies And Windows

Wrapping and extracting do not copy. Assigning `orders[0]` changes the slice
passed to `NewSlice`, and writes through `orders.Items()` change the collection.
Allocated transformations such as `Filter`, `Map`, `Sort`, `Reverse`, and
`Merge` own their outer result storage. Copies remain shallow: pointers, maps,
and slices inside elements can still refer to shared data.

Slice windows behave differently. `Take`, `Skip`, `SkipWhile`, `SkipUntil`,
and `ForPage` share the source array, as do eager `Chunk` and `Sliding` windows.
A write through a window can change the source; retaining a small window can
also keep a large array alive. To keep an independent dispatch prefix, import
the standard-library `slices` package and clone it:

```go
batch := collection.NewSlice(slices.Clone(ready.Take(1).Items()))
batch[0].Units = 1
```

`ready[0].Units` is still 3. `Sort` already makes a shallow copy, but it is not
stable: equal items need not retain their relative order. Supply a comparison
that includes a tie-breaker if that distinction matters.

Nil eager collections support empty reads. `First` reports false, `Len` is
zero, and a filter with no matches returns nil slice storage. Constructors and
`Items` preserve nil versus allocated-empty input; windows preserve their
source representation. A nil map still panics on indexed writes: allocate it
first, or keep a populated `Merge` result. Concurrent access follows ordinary
Go slice and map rules; synchronize writes and shared mutable elements.

## Choosing Evaluation

Eager operations suit data already held in memory. Use [Lazy Collections](lazy-collections.md)
when callbacks should run only as results are consumed, especially when you
need only a prefix. Use [Fallible Collections](fallible-collections.md) when a
source or callback can fail during traversal. Neither conversion copies the
underlying application data or supplies cancellation automatically.

For selection, windowing, and serialization details, use
[Collection Methods](available-methods.md). Validate untrusted window sizes:
`Chunk` and `Sliding` require positive sizes, and `Nth` and `Sliding` require
positive steps. Invalid values panic. `Take` and `Skip` clamp counts instead;
`ForPage` does not validate page inputs or arithmetic overflow.

## Native Results

Arkoren data results can already be collections. Database `Select` returns an
eager slice; [cursors and batch traversal](../database/batching-and-streaming.md)
return fallible lazy slices. [Paginated results](../queries/pagination.md)
contain only the returned window: filtering its `Items` does not change the
server's total, query ordering, or navigation tokens.

Inspection results, including router routes, scheduler entries, telemetry
records, and [workflow definitions](../workflows/running-workflows.md#inspecting-definitions),
retain their producer's ordering and copy depth. The collection wrapper adds
no extra isolation. [Storage listings](../file-storage/overview.md) and
[model streams](../llm/streaming.md) retain their producer's cleanup and replay
rules; traversing a model source again can repeat billed work.

Eager collections use their native container's JSON shape through
`encoding/json/v2`. Default nil non-byte slices encode as `[]`, maps as `{}`,
and byte slices as base64 strings. Lazy JSON encoding materializes the source;
it is not network streaming. For schemas, eager collections declare their
native shape to [output description](../validation/json-schema.md#describe-output).
Attach logical rules through the [named-container adapter](../validation/collections.md#named-containers).

For exact declarations, see the [`collection` API reference](https://pkg.go.dev/arkoren.dev/foundation/collection).
