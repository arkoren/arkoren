---
title: Fallible Collections
description: Process sources that can fail halfway, including database cursors, storage listings, model streams, and files, with TryLazySlice and TryLazyMap.
order: 30
---

# Fallible Collections

## Introduction

Reading from a database or a file can fail after you have already processed
many items. A `TryLazySlice` carries an error alongside each item, and its
methods take functions that can return one. Build one with `NewTryLazySlice`
or start from a slice with `Try()`. Import `arkoren.dev/foundation/collection`
alongside the standard-library `bufio`, `fmt`, and `strconv` packages. The example
below uses an existing `io.Reader` named `r`; `bufio.Scanner` reports read failures, and
`strconv.Atoi` can reject individual lines:

```go
lines := collection.NewTryLazySlice(func(yield func(string, error) bool) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if !yield(scanner.Text(), nil) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		yield("", err)
	}
})

numbers := lines.Map(func(_ int, line string) (int, error) {
	return strconv.Atoi(line)
})
```

The reader remains owned by your application: wrapping it neither closes it nor
cancels a blocked read. [Supplying an iterator](lazy-collections.md#supplying-an-iterator)
explains source cleanup, cancellation, and the non-nil iterator prerequisite.

## Stopping at the first error

`Items()` collects until the first error, and returns what it had together
with that error:

```go
values, err := numbers.Items()
```

Use `err` to decide whether to keep the successfully collected prefix in
`values` or reject the whole input. Do not treat that prefix as a complete read.
Slice materializers return nil if no successful values were collected, including
an error before the first success; map materializers return allocated maps.

Methods that end the chain, such as `Each` and `Reduce`, return an error as
well. They stop on the first source or callback error. If you range over a
`TryLazySlice` directly, you receive `(item, err)` pairs and choose when to
stop; ignore any item yielded with a non-nil error.

## Continuing after errors

When one bad item should not stop the rest, use `ItemsAll` instead of `Items`
for that traversal. `ItemsAll` and `EagerAll` read to
the end, keep every item that succeeded, and join the errors with
`errors.Join`. `EachAll` is the variant of `Each` that keeps going.

The `All` variants continue only if the source can continue. A database cursor
ends after its terminal connection or scan error; `ItemsAll` cannot recover the
connection or fetch the unread rows. This reader-backed example is single-use:
materialize it once if you need to process the same lines twice.

## Limiting successful results

Use [`Take`](available-methods.md#take) to limit successful values or entries,
and [`Skip`](available-methods.md#skip) to discard the first successful ones.
Errors are forwarded in source order but consume neither a limit nor a skipped
position. Errors within a skipped prefix are still observable. `Take(0)` or a
negative limit consumes nothing, including no errors; a positive `Take` stops
immediately after its last requested success.

On a lazy map, these methods count entries, not distinct keys. Two successful
entries for the same key consume two positions, even though materialization
keeps only the last value for that key. Continuing after an error preserves the
remaining limit or skipped count; stopping iteration stops the source. On slices,
`Nth`, `Chunk`, and `Sliding` also count successful values rather than errors.

## Callback indexes after errors

`TryLazySlice` streaming callbacks start at index zero. A source error bypasses
the callback and does not advance its index. A callback error also leaves the
next callback at the same index if you continue iteration:

```go
var indices []int
parsed := collection.NewSlice([]string{"bad", "10", "20"}).Try().Map(func(index int, text string) (int, error) {
	indices = append(indices, index)
	return strconv.Atoi(text)
})
values, err := parsed.ItemsAll()
fmt.Println(values, indices, err != nil) // [10 20] [0 0 1] true
```

Continuation moves to the next source result; it does not retry the failed
item. Successful callbacks advance once even when `Filter` rejects the value
or `FlatMap` returns no values. Each step has its own indices, so carry an
identifier in the value when you need to identify the original input.

`EachAll` deliberately differs: its index counts every successful source value,
including one whose callback returns an error. Given the same three source
strings, its callback sees indices zero, one, and two even if it fails on
`"bad"`. Source errors still bypass the callback and do not advance that index.

## Sampling and windowing successful items

Choose sampling before you consume the reader-backed pipeline. For a single
traversal of the `numbers` from the introduction, apply
[`Nth`](available-methods.md#nth). Its offset is zero-based and defaults to zero;
a negative offset also starts at zero, just as on eager and infallible lazy slices:

```go
for value, err := range numbers.Nth(2, -1) {
	if err != nil {
		fmt.Println("skipped:", err)
		continue
	}
	fmt.Println(value)
}
```

This selects successful positions zero, two, four, and so on. A conversion
error is forwarded but does not consume an offset or step position. The loop
ignores the accompanying value and continues if the source has more results;
it does not retry a failed read or parse.

[`Sliding`](available-methods.md#sliding) likewise counts only successful items:
`Sliding(2, 3)` yields two-item windows whose starts are three successful items
apart. Errors leave a partial window and the gap unchanged, and are forwarded
even within a skipped gap. A window yielded with an error must be ignored;
successful windows have independent, shallow-copied slice storage.

Both operations consume nothing until iteration. Breaking on a selected value,
window, or forwarded error stops the source immediately. Continuing iteration
preserves the offset, step, and partial-window progress, but cannot make a
terminal source error recoverable. Their steps must be positive; steps through
`math.MaxInt` are valid without wrapping.

## Where fallible collections come from

Arkoren returns fallible collections from several data producers:

- [Database cursors and batch traversal](../database/pagination.md) read rows
  as you consume them.
- [Storage `Walk`](../file-storage/overview.md) follows listing cursors across pages
  of object metadata. Its page size is not a limit on the total items yielded.
- [LLM `Stream` and `RunStream`](../llm/streaming.md) yield model or agent events.
  Stopping early interrupts execution rather than completing a model turn.

These sources end on their terminal read or execution error. `ItemsAll` can
continue past a transformation error when the source permits it, but cannot
retry a failed page fetch or resume a failed model request. A successful prefix
returned with an error is incomplete; decide explicitly whether your task can
use that prefix.

The producer owns cancellation and cleanup. Traversing a storage listing or
model stream again performs fresh work, not a replay of a saved snapshot; model
requests can incur additional charges. Materialize once if you need to inspect
the same successful values repeatedly. [Native Arkoren results](overview.md#native-arkoren-results)
compares the available sources, and [Lazy Collections](lazy-collections.md)
explains deferred evaluation and source ownership.

For every fallible collection method, see the
[`collection` API reference](https://pkg.go.dev/arkoren.dev/foundation/collection).
