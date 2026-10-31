# Typed search definitions

Use `arkoren.dev/core/service/search` to declare immutable indexes and queries
over your application's document types. The engine validates declarations and
encodes documents; the selected driver stores and searches them. Install it
with `go get arkoren.dev/core` in your application module.

Start with [Search Overview](../../../cmd/arkoren/skills/arkoren/references/guides/search/overview.md)
for a document type, index, synchronization, and search. The
[memory driver](memorysearch/README.md) is suitable for isolated application
tests and rebuildable single-process indexes.

## Validate before using definitions

Declare fields in `NewIndex` callbacks and use `NewQuery` or `Query.Derive` for
filters and ordering. Published getters return detached field and condition
containers. Comparison operators accept either strings or the corresponding
typed constants inside a query callback. This fragment imports
`arkoren.dev/core/service/search` and uses the overview's `Products` definition:

```go
query := search.NewQuery(Products, "red", func(query *search.QueryBuilder) {
	query.Where("price", search.OpLessOrEqual, 100)
	query.OrderBy("price")
})
err := query.Err()
```

The query retains only normalized scalar operands and owned containers.
A zero query is invalid, including after derivation; the
first declaration error survives later callbacks.

## Custom index declarations

`Indexer` requires `Schema() search.Schema` and `Err() error`. Existing custom
implementations that exposed only `Schema` must add `Err` and report their full
definition failures, including settings that cannot appear in the raw schema.
The shipped `Index[T]` already implements both methods; ordinary applications
using `NewIndex` need no adapter or compatibility wrapper.

Engine construction records an invalid registration without doing I/O. Every
operational call returns that error before touching the driver. Accessors
`Driver` and `DriverAs` remain usable and do not emit telemetry; direct calls on
the returned driver bypass facade validation, conversion, events, and telemetry.

Cancellation during facade encoding or decoding discards incomplete local
results. Once driver writes begin, cancellation does not imply rollback. Native
driver cancellation and atomicity remain provider-specific; consult the
[driver comparison](../../../cmd/arkoren/skills/arkoren/references/guides/search/drivers.md)
before depending on them.

See the [search API reference](https://pkg.go.dev/arkoren.dev/core/service/search).
