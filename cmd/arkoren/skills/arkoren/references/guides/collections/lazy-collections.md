---
title: Lazy Collections
description: How lazy collections defer work until you read the result, stop early, keep indexes through a chain, and behave when iterated twice.
order: 20
---

# Lazy Collections

## Introduction

If you need only the first few matching users, an eager chain can still
transform every user before selecting the result. A lazy chain pulls users
through streaming steps as you read them, so it can stop once it has enough.
Operations that need the full input, such as sorting or grouping, are the
exception, as explained below.
The examples use the `collection` package from
`arkoren.dev/foundation/collection` and these users:

```go
type User struct {
	Name   string
	Age    int
	Active bool
}

users := collection.NewSlice([]User{
	{Name: "Ada", Age: 36, Active: true},
	{Name: "Bob", Age: 17, Active: true},
	{Name: "Cy", Age: 52, Active: false},
})
```

Start a lazy chain from the eager collection with `Lazy()`:

```go
calls := 0
first := users.Lazy().TapEach(func(int, User) {
	calls++
}).Filter(func(_ int, u User) bool {
	return u.Active
}).Map(func(_ int, u User) string {
	return u.Name
}).Take(2)
// nothing has run yet: calls == 0

names := first.Items() // [Ada Bob]; calls == 2
```

Nothing happens when the chain is built. `Items()` pulls items through the
whole chain one at a time, and `Take(2)` stops the pull as soon as it has two,
so the third user is never read. `TapEach` is a convenience for observing items
as they pass, such as for logging; it does not change them.

A lazy collection is a function, so you can also range over it directly and
break out whenever you like:

```go
for i, name := range first {
	fmt.Println(i, name)
}
```

Choose between the two kinds by what the work costs:

- Allocating eager transformations create intermediate collections; windowing
  methods such as `Take` share existing storage. For a few hundred
  items this is cheap and easy to debug, because each step's result is an
  ordinary value.
- A streaming lazy chain avoids an intermediate result slice at each step, so it suits large inputs and
  chains that end in `First`, `Take`, or `Contains`, which stop as soon as they
  have an answer.
- Some operations need every item. `Sort` and `Reverse` read the whole source
  when you iterate their results. `Partition`, `GroupBy`, `KeyBy`, and `CountBy`
  consume it when you call them. Do not use these operations on an infinite
  source, or when the full result cannot fit in memory.

## Supplying an iterator

`NewLazySlice`, `NewLazyMap`, `NewTryLazySlice`, and `NewTryLazyMap` retain the
iterator function and its captures without invoking or copying them. Supply a
non-nil function: a lazy collection's zero value, or a constructor given nil,
is a nil function and panics if traversed. To obtain an iterable empty sequence,
convert an empty eager collection instead:

```go
empty := collection.NewSlice[int](nil).Lazy()
fmt.Println(empty.Items() == nil) // true; traversal is valid.
```

Use `Try()` instead of `Lazy()` for an empty fallible sequence. Maps follow the
same pattern through `NewMap[K, V](nil)`.

A lazy collection can also be infinite, as long as the chain ends in something
that stops, such as `Take`:

```go
naturals := collection.NewLazySlice(func(yield func(int, int) bool) {
	for n := 0; ; n++ {
		if !yield(n, n) {
			return
		}
	}
})

first := naturals.Take(4).Items() // [0 1 2 3]
```

The source must return when `yield` returns false, as in this iterator. If the
source acquires a file, cursor, or other resource, it must release that resource
on every return, including an early stop. Wrapping an existing resource does
not transfer ownership or register cleanup: the owner still arranges its close.

Collection operations run synchronously and start no goroutines. They supply no
context, timeout, or cancellation signal. A blocking source must capture and
observe the caller's context itself; stopping downstream cannot interrupt work
that is blocked before the next yield. For fallible I/O, use a `TryLazySlice` or
`TryLazyMap` source to report read or cancellation errors.

Ending a lazy chain with `Items()` gives a plain Go slice, and `Eager()` a
`Slice` you can continue with. `LazyMap` works the same way, and
`collection.LazyKeys` and `collection.LazyValues` turn one into a `LazySlice`.

## Sampling and sliding windows

To sample a stream, use [`Nth`](available-methods.md#nth): it selects every
`n`th position starting at an optional zero-based offset. A missing or negative
offset starts at zero, matching eager slices. It counts positions in the
sequence, not the indices supplied by a custom iterator, and yields dense
output indices.

For complete groups that can overlap or skip a gap, use
[`Sliding`](available-methods.md#sliding). For example, stop after the first
two-item window without reading the gap before the next window:

```go
read := 0
windows := collection.NewSlice([]int{1, 2, 3, 4, 5, 6}).Lazy().TapEach(func(_ int, _ int) {
	read++
}).Sliding(2, 3)
// read == 0: construction has not consumed the source.

for _, window := range windows {
	fmt.Println(window) // [1 2]
	break
}
// read == 2: the gap and remaining windows were not consumed.
```

Each yielded window owns independent slice storage, unlike eager `Sliding`
windows; nested pointers, maps, and slices inside elements are still shared.
Both methods require positive steps and accept steps through `math.MaxInt`
without wrapping. A large step does not let a lazy iterator jump over its
source: requesting the next selection or window still reads the intervening
items. Stop iteration when you have enough.

## Indexes in lazy chains

If you use an index in a callback, it refers to that step's input, not
necessarily the original slice. Filtering renumbers its output from zero, so
the callbacks after `Filter` see dense indexes without gaps:

```go
for i, label := range users.Lazy().Filter(func(_ int, u User) bool {
	return u.Age > 20
}).Map(func(i int, u User) string {
	return fmt.Sprint(i, u.Name)
}) {
	fmt.Println(i, label)
}
// 0 0Ada
// 1 1Cy
```

## Iterating more than once

A lazy collection invokes its iterator every time you traverse it; this does
not cache the result. A slice- or map-backed source can be traversed again,
but a one-shot source such as an open file, channel, or network response might
yield nothing or only the unread remainder on a second pass. Treat such a
source as single-use. If you need the result twice, call `Eager()` or `Items()`
once and reuse the materialized value. Repeating a traversal also repeats
`TapEach` or `Map` callbacks for the items the source yields.

For every lazy collection method, see the
[`collection` API reference](https://pkg.go.dev/arkoren.dev/foundation/collection).
