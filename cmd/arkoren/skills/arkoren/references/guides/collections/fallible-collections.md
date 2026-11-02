---
title: Fallible Collections
description: Preserve read and callback failures while choosing whether to stop, retain a partial result, or continue traversal.
order: 30
summary:
  - Choose first-error or continuing traversal without treating partial successes as complete input.
  - Track successful positions while preserving source and callback errors.
  - Expose reader failures and retain producer-owned cleanup, cancellation, and replay constraints.
---

# Fallible Collections

An import can fail after several records have succeeded. `TryLazySlice[T]`
yields `(item, error)` pairs so that failure remains part of the traversal.
`TryLazyMap[K, V]` yields `(MapEntry[K, V], error)` pairs, where `MapEntry`
contains `Key` and `Value`. Ignore the item or entry whenever its error is
non-nil.

These are lazy iterators, not saved results. Streaming transformations forward
source and callback errors; the consumer decides whether to stop or continue.
Ordinary terminal methods stop on the first error. Methods ending in `All`
continue while the source permits it and return joined errors. Neither policy
retries a failed operation or recovers a panic.

## Parsing Import Values

Import `arkoren.dev/foundation/collection` and the standard-library `strconv`
package. Start with typed import lines so that source identity is available
even if callback indexes change:

```go
type QuantityLine struct {
	Line  int
	Units string
}

lines := collection.NewSlice([]QuantityLine{
	{Line: 1, Units: "3"},
	{Line: 2, Units: "unknown"},
	{Line: 3, Units: "5"},
})
```

`Try()` converts an eager or infallible lazy collection to its fallible lazy
form. It does not parse anything yet. Make parsing a step that can return an
error:

```go
quantities := lines.Try().Map(func(_ int, line QuantityLine) (int, error) {
	return strconv.Atoi(line.Units)
})
```

The result is a `TryLazySlice[int]`. When consumed, successful conversions yield
an integer and nil error; the invalid line yields a conversion error instead
of a usable integer. A source error bypasses the callback entirely.

## Stopping On Failure

Choose `Items` when a failure should stop this read:

```go
units, err := quantities.Items()
```

`units` contains only `[3]`, and `err` is the conversion error for `unknown`.
The third line is not processed. Decide whether your application can use that
prefix or must reject the import; it is not a complete result. `Eager` returns
an eager collection with the same first-error policy. A slice materializer
returns nil if no values succeed; map materializers return allocated maps.
Both own new outer storage with shallow element copies.

`Each` also stops on the first source or callback error. Work completed by an
earlier callback is not rolled back. If callbacks write application data, use
your application's transaction or idempotency policy rather than assuming
collection traversal is atomic.

## Continuing Explicitly

If independent import lines may succeed despite other invalid lines, choose
`ItemsAll` instead. This is an alternative traversal of the replayable
in-memory source above, not a second pass over an already consumed reader:

```go
units, err := quantities.ItemsAll()
```

`units` contains `[3, 5]`. `err` joins the encountered errors with `errors.Join`;
it is still non-nil even though later values succeeded. `EagerAll` collects
into an eager collection, and `EachAll` continues after both source and callback
errors. Use `errors.Is` or `errors.As` to inspect a joined cause when needed.

For a per-error decision, range over the fallible source. This loop deliberately
reports each conversion failure and skips its accompanying value; import `fmt`
for the output:

```go
for units, err := range quantities {
	if err != nil {
		fmt.Printf("Rejected quantity: %v\n", err)
		continue
	}
	fmt.Println(units)
}
```

The loop prints 3, reports the invalid quantity, then prints 5. Replacing
`continue` with `break` stops the source at that error. Continuation moves to
the next source result; it does not retry the failed line. A terminal read error
can end a source even when the consumer wants to continue.

## Limits And Indexes

`Take` and `Skip` count successful values or map entries, not errors. Errors
are forwarded in source order, including inside a skipped prefix. A positive
`Take` stops immediately after its last requested success. `Take(0)` and
negative limits consume nothing, so they cannot reveal any source error.
Duplicate lazy-map keys count as separate entries, even though materialization
keeps only the last successful value for a key.

`Nth`, `Chunk`, and `Sliding` likewise count successful slice values. An error
does not consume an offset, a step position, or space in a partial window.
Continuing after the error resumes that progress if the source has more
results; ignoring the error would conceal an incomplete or invalid input.
Sizes and steps must be positive where required, as described in
[Collection Methods](available-methods.md#nth).

Streaming slice callbacks start at index zero. Source errors do not advance
the index, and callback errors leave the next callback at the same index.
For the three import lines, this records that difference:

```go
var indexes []int
parsed := lines.Try().Map(func(index int, line QuantityLine) (int, error) {
	indexes = append(indexes, index)
	return strconv.Atoi(line.Units)
})
units, err := parsed.ItemsAll()
```

`indexes` contains `[0, 1, 1]`, `units` contains `[3, 5]`, and `err` is non-nil.
A successful predicate still advances its input index when it rejects a value;
a successful `FlatMap` advances once even when it returns no items. Later steps
have their own input indexes. Carry `QuantityLine.Line` into application errors
or output records when you need the original line number.

`EachAll` deliberately differs: its index counts every successful source value,
including a value whose callback fails. Calling it directly on `lines.Try()`
therefore gives callback indexes zero, one, and two. Source errors still bypass
its callback and do not advance that index.

## Supplying Read Errors

For an existing application-owned `io.Reader` named `reader`, a scanner-backed
iterator must expose `Scanner.Err`, not silently treat a failed read as the end
of input. Import `bufio` along with `collection`:

```go
text := collection.NewTryLazySlice(func(yield func(string, error) bool) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		if !yield(scanner.Text(), nil) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		yield("", err)
	}
})
```

`text.Items()` returns scanned lines and any read or scanner-limit error.
The source returns immediately when `yield` is false. Its last error yield
needs no continuation check because the iterator returns immediately afterward.

The reader remains application-owned: wrapping it does not close it or cancel
a blocked read. If your iterator opens a resource, arrange its release inside
the iterator on every return, including early stop. If it captures an already
open resource, its existing owner arranges cleanup. Blocking sources must
observe their own context; the collection supplies no cancellation or timeout.
Use a non-nil iterator function, as explained under
[Supplying An Iterator](lazy-collections.md#supplying-an-iterator).

This reader-backed source is single-use unless the reader itself supports
replay. Materialize once when you need to inspect the same values repeatedly.
Do not call `ItemsAll` after `Items` expecting it to resume with every original
line: each traversal invokes a new scanner over the reader's current position,
and buffering may already have read ahead.

## Native Producers

[Database cursors and batch traversal](../database/batching-and-streaming.md),
[storage listings](../file-storage/overview.md), and
[model streams](../llm/streaming.md) return fallible lazy data. Their producers
own cleanup, cancellation, and repeat-traversal behavior. A storage listing's
page size is not a total-item limit; repeated traversal can issue fresh requests.
Repeating a model stream can repeat billed work, and stopping early interrupts
execution rather than completing a model turn.

These producers stop after terminal read or execution errors. `ItemsAll` can
continue after your transformation fails, but cannot reconnect a cursor, retry
a failed page fetch, or resume a failed model request. Check the returned error
before treating a collected prefix as complete.

Full-input transformations have their own failure shapes: `Sort` and `Reverse`
discard a failed materialization's prefix and yield only the error. `Partition`
consumes at the call and makes both returned iterators yield the error without
partial partitions. `GroupBy`, `KeyBy`, and `CountBy` return partial eager maps
with the first error. JSON encoding returns the first source error instead of
encoding a partial collection; materialization is not network streaming.

For exact declarations, see the [`collection` API reference](https://pkg.go.dev/arkoren.dev/foundation/collection).
