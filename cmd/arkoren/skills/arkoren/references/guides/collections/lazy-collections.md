---
title: Lazy Collections
description: Defer collection work until traversal and choose safe stopping, buffering, and replay behavior for your source.
order: 20
summary:
  - Consume only the needed prefix and recognize operations that read or buffer the full source.
  - Supply iterators with explicit stop, cleanup, cancellation, and ownership rules.
  - Interpret callback indexes and windows without assuming cached or replayable results.
---

# Lazy Collections

If a packing station needs only the next two ready parcels, transforming every
parcel first does unnecessary work. A `LazySlice` is an iterator: streaming
steps such as `Filter` and `Map` run as you consume their result, not when you
build the chain. `LazyMap` applies the same model to keyed entries.

Use eager collections when you want an immediately available result. Use lazy
collections when deferring callbacks or stopping before the end matters. A lazy
chain does not cache values, add concurrency, or make a source replayable.

## Reading A Prefix

Import `arkoren.dev/foundation/collection`. These parcels are already in memory;
`Lazy()` changes when later operations run, without copying the source:

```go
type Parcel struct {
	ID    string
	Ready bool
}

parcels := collection.NewSlice([]Parcel{
	{ID: "pkg-41", Ready: false},
	{ID: "pkg-42", Ready: true},
	{ID: "pkg-43", Ready: true},
	{ID: "pkg-44", Ready: true},
})
```

Observe the source before filtering to distinguish records read from records
selected:

```go
read := 0
next := parcels.Lazy().TapEach(func(_ int, _ Parcel) {
	read++
}).Filter(func(_ int, parcel Parcel) bool {
	return parcel.Ready
}).Map(func(_ int, parcel Parcel) string {
	return parcel.ID
}).Take(2)
```

`read` is still zero. `TapEach` observes each item without changing it; on a
lazy collection it runs only during traversal. Materialize the selected IDs
when the station is ready to use them:

```go
ids := next.Items()
```

`ids` is `[]string{"pkg-42", "pkg-43"}` and `read` is 3. `Take(2)` stops after
two matches; it does not read `pkg-44`. `Eager()` instead returns an eager
`collection.Slice[string]` with methods. Both materializers collect new outer
storage and copy elements shallowly. A slice with no yielded values is nil;
an empty materialized map is allocated.

You can also range over a lazy collection directly, or pass `Iter()` to a
standard-library iterator consumer. Breaking the loop stops traversal, provided
the source honors the iterator's stop signal.

## Full Input Operations

Not every operation can produce its first result from a prefix:

| Operation | Source Consumption |
| --- | --- |
| `Filter`, `Map`, `Take`, `Skip` | During traversal, as downstream asks for values. |
| `First`, `FirstWhere`, `Contains`, `IsEmpty` | At the call, stopping when an answer is known. |
| `Items`, `Eager`, `Len`, `Last`, `Reduce`, `Each` | At the call, through the end unless the operation can stop. |
| `Sort`, `Reverse` | The whole source before yielding, on every traversal. |
| `GroupBy`, `KeyBy`, `CountBy`, `Partition` | At the method call, buffering their results. |

Keep full-input operations off infinite sources. Sorting, reversing, grouping,
and partitioning also retain data in memory; laziness does not remove that
cost. In particular, `source.Sort(compare).Take(2)` still reads and sorts every
item before it yields two. `Unique` streams but retains a set of seen keys,
so its memory use can grow with the number of distinct keys.

## Supplying An Iterator

`NewLazySlice` accepts an iterator yielding `(index, item)` pairs. For example,
an application can expose an existing ordered set of bin labels without first
building a collection result:

```go
bins := collection.NewLazySlice(func(yield func(int, string) bool) {
	for index, label := range []string{"A-01", "A-02", "B-01"} {
		if !yield(index, label) {
			return
		}
	}
})
```

The constructor retains the function and its captures without invoking them.
`bins.Take(1).Items()` returns only `A-01`. The source must return when `yield`
returns false; continuing to yield violates the Go iterator protocol.
`NewLazyMap` accepts an `iter.Seq2[K, V]`, such as `maps.All(existingMap)` from
the standard-library `maps` package. A Go-map source retains unspecified order.

Supply a non-nil function. Lazy zero values and constructors given nil are nil
functions, not iterable empty collections; traversing them panics. For an empty
source, use `collection.NewSlice[Parcel](nil).Lazy()` or
`collection.NewMap[string, int](nil).Lazy()`.

## Source Ownership

Wrapping an iterator does not acquire or close its resources. If your source
opens a file or cursor inside its iterator, release it on every return,
including early stop and failure. If the source captures an already open
resource, its existing owner still arranges cleanup. For I/O that can fail,
use [Fallible Collections](fallible-collections.md) rather than hiding read
errors in an infallible iterator.

Collection operations run synchronously in the consuming goroutine. They start
no goroutines and supply no context, timeout, or cancellation signal. A blocking
source must capture and observe the caller's context itself. Stopping downstream
cannot interrupt work blocked before the next yield.

The source and callbacks also determine concurrency safety. Do not mutate a
captured slice or map while another goroutine traverses it without
synchronization. A lazy wrapper is not a snapshot of its captured data.

## Callback Indexes

A callback on `LazySlice` receives the index yielded by its immediate input.
Custom sources can supply arbitrary indexes. Transformations yield dense output
indexes starting at zero, so an index is not a stable record identifier:

```go
labels := parcels.Lazy().Filter(func(_ int, parcel Parcel) bool {
	return parcel.Ready
}).Map(func(index int, parcel Parcel) string {
	return fmt.Sprintf("%d: %s", index, parcel.ID)
}).Take(2).Items()
```

With the standard-library `fmt` package imported, `labels` contains
`0: pkg-42` and `1: pkg-43`. The rejected parcel no longer occupies a position
in `Map`'s input. Carry the parcel ID in the value when later steps need its
identity. Map callbacks receive keys instead of indexes.

## Sampling And Windows

Use `Nth` to sample positions, or `Sliding` to inspect complete neighboring
groups. With the parcels above:

```go
sample := parcels.Lazy().Nth(2, 1).Items()
windows := parcels.Lazy().Sliding(2, 3)
```

`sample` contains `pkg-42` and `pkg-44`: offset 1 is zero-based. `Nth` defaults
to offset zero and clamps negative offsets to zero. Selection counts sequence
positions, not the indexes a custom source supplies.

`windows` is an `iter.Seq2[int, []Parcel]`. It yields only the first two-parcel
window here; the next start has no complete window. `Sliding` defaults its step
to one and omits incomplete final windows. Use `Chunk` for nonoverlapping
groups that include a final short group. Lazy chunks and windows own independent
outer slice storage, unlike eager windows; nested references remain shared.

Validate sizes and steps before calling these methods: `Chunk` and `Sliding`
panic on nonpositive sizes, and `Nth` and `Sliding` panic on nonpositive steps.
Positive steps through `math.MaxInt` are valid without wrapping. A lazy source
still reads intervening items to reach the next selection; a large step does
not skip I/O. Stopping after a yielded selection or window avoids reading the
following gap.

## Repeating Traversal

Every traversal invokes the retained iterator again. Reusing `next.Items()`
from the prefix example repeats its callbacks and increments `read` again.
A slice-backed source can be traversed again, but sees shared element data,
not an isolated snapshot.

An already open file, channel, or network response may yield only its unread
remainder on a second pass. A source that opens a fresh request can repeat I/O,
including billed model work. Treat a source as single-use unless its contract
says otherwise. If you need to inspect the same result twice, materialize it
once and reuse that eager value rather than traversing again.

For exact declarations, see the [`collection` API reference](https://pkg.go.dev/arkoren.dev/foundation/collection).
