# Collection

[![Go Reference](https://pkg.go.dev/badge/arkoren.dev/foundation/collection.svg)](https://pkg.go.dev/arkoren.dev/foundation/collection)

`collection` provides generic helpers for slices and maps, including lazy and
error-aware transformations.

## Installation

```sh
go get arkoren.dev/foundation/collection
```

## Requirements

Go 1.27 or later.

## Filtering application data

Import `arkoren.dev/foundation/collection` and keep the even values:

```go
numbers := collection.NewSlice([]int{1, 2, 3, 4})
even := numbers.Filter(func(_ int, number int) bool {
	return number%2 == 0
})
// even.Items() is []int{2, 4}; numbers is unchanged.
```

`NewSlice` and `Items` share their input storage. `Filter` allocates a new result;
windowing methods such as `Take` share the backing array. Copies remain shallow
when elements themselves contain pointers, maps, or slices.

Constructors and eager `Items` preserve nil versus allocated-empty storage.
Newly collected slice results from `Filter`, `Map`, `FlatMap`, `TakeWhile`,
`Unique`, `Partition`, and fallible materialization are nil when they collect
no values. Shared windows such as `Take` keep the source's representation.

## Merging map overrides

To combine default settings with overrides, call `Map.Merge`. Either input can
be nil and is treated as empty:

```go
defaults := collection.NewMap(map[string]string{"theme": "light"})
overrides := collection.NewMap(map[string]string{"theme": "dark"})
settings := defaults.Merge(overrides)
// settings.Items()["theme"] is "dark"; defaults still contains "light".
```

The other map's values win when keys collide. Neither input is modified, and
the result has independent outer storage. Copies are shallow: pointers, maps,
and slices within values remain shared with the inputs. Both a zero `Map` and
`NewMap(nil)` can receive entries through `Merge` without prior allocation.

## Sliding windows

`Slice.Sliding` returns complete windows that share the source's backing array;
`LazySlice.Sliding` and `TryLazySlice.Sliding` yield independent window copies.
The step counts successful source items between window starts:
`Sliding(2, 3)` over `1..6` yields `[1 2]` and `[4 5]`, skipping `3` between
windows. The fallible form forwards errors even in the skipped gap; errors do
not count toward the step. Stopping iteration stops source consumption.

The step defaults to one and must be positive, as must the window size.
Steps through `math.MaxInt` are valid. On a nonempty source,
`Sliding(1, math.MaxInt)` returns only the first window without wrapping the
next start.

## Selecting every nth item

To sample a sequence at a fixed interval, use `Nth`. All three slice forms
start at offset zero by default and clamp negative offsets to zero:

```go
numbers := collection.NewSlice([]int{1, 2, 3, 4, 5})
sample := numbers.Nth(2, -1).Items()
// sample is []int{1, 3, 5}; numbers is unchanged.
```

`Nth` accepts any positive `int` step, including `math.MaxInt`.
An offset at or beyond the source length produces no selected items. The
eager result has independent slice storage, with shallow element copies.
The lazy forms consume nothing until iteration and stop when you stop iterating.
`TryLazySlice.Nth` counts only successful items toward the offset and step;
it forwards errors in order and continues selection if you continue iteration.

## Choosing when to consume a source

Build a lazy chain when you want to stop after a useful prefix. Streaming
transformations consume nothing until iteration and stop upstream when you stop.
`Items`, `Eager`, aggregations, and `Each` consume during the method call.
`Reverse` and `Sort` instead read the whole finite source each time you iterate
their results. `Partition` reads during the call and returns buffered results
that can be traversed independently.

A traversal calls the source again; it does not cache it. Materialize a single-use
source once if you need its values twice. A zero lazy collection is a nil function
and panics when traversed. Use `NewSlice[int](nil).Lazy()` or `.Try()` for an
iterable empty sequence. Sources own cleanup and must stop when `yield` returns
false. Blocking work needs source-owned context cancellation; the collection
package does not start goroutines or supply a timeout.

## Continuing after conversion errors

Use `Items` to stop at the first error and receive the successful prefix. Use
`ItemsAll` when later successful values are useful even if an earlier conversion
fails. It consumes a finite source and joins errors without retrying failed items.
Import `strconv` alongside `collection` for the conversion below:

```go
parsed := collection.NewSlice([]string{"1", "bad", "2"}).Try().Map(func(_ int, text string) (int, error) {
	return strconv.Atoi(text)
})
items, err := parsed.ItemsAll()
// items is []int{1, 2}; err contains the failed conversion of "bad".
```

Check `err` before deciding whether to accept the successful values.
Streaming transformations forward errors and can continue when their consumer
continues; a terminal source cannot be resumed by
an `All` method. Ignore a value paired with an error during direct iteration.

Fallible slice callback indices start at zero. Source errors and streaming
callback errors do not advance them; successful callbacks do, even when a
predicate rejects a value or an expansion is empty. `EachAll` is different: its
index counts every successful source value, including one whose callback fails.
The indices describe each step's input, not stable original-source positions.

## Paging and JSON boundaries

`ForPage` composes `Skip((page-1)*perPage)` and `Take(perPage)`. Validate positive
page arguments and a representable product before passing untrusted input; it
does not validate arguments or detect overflow. In the fallible form, errors in
skipped positions are still forwarded and do not count toward page positions.

JSON methods use `encoding/json/v2`, including the compatibility methods named
`MarshalJSON` and `UnmarshalJSON`. Nil eager non-byte slices and maps encode as
`[]` and `{}` by default. Lazy encoding consumes its finite source; fallible
encoding stops before writing collection data if source materialization fails. Successful lazy
decoding replaces the source with replayable storage; decoding errors leave its
previous source intact. Eager decoding follows the underlying decoder's merging
and partial-update behavior.

`Slice.JSONSchemaType()` and `Map.JSONSchemaType()` return their identical native
slice/map `reflect.Type`. Schema tools can recognize this structural protocol
without importing `collection`; `validate.Infer` uses it to retain element and
value shapes in collection-valued DTOs. Byte slices encode as base64 strings,
not arrays, and the declaration preserves that native special case. It does not
validate values or alter encoding options. Lazy collections do not implement it.

## Development

Go 1.27 or later is required. Run the checks directly:

```sh
gofmt -w *.go
go test -race ./...
go vet ./...
govulncheck ./...
```

## License

MIT. See [LICENSE](LICENSE).
