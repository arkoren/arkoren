# Arkoren Search Memory Driver

`arkoren.dev/core/service/search/memorysearch` is an in-process implementation of
`search.Driver` using only the standard library.

It is suitable for tests, local development, and single-process applications.
Use it in application tests instead of a fake: sync your indexes, put the
documents a test needs, and search them through `search.New`. Nothing is
persisted, and a search scans every document of the index. Benchmark your data
and query load before choosing it for production.

## Using the driver

Run `go get arkoren.dev/core` in your application's Go module. With the
`Products` definition from [Search Overview](../../../../cmd/arkoren/skills/arkoren/references/guides/search/overview.md),
register the index on the engine:

```go
import (
	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
)

engine := search.New(memorysearch.New(), func(engine *search.Builder) {
	engine.Indexes(Products)
})
```

Call `engine.Sync(ctx)` and check its error before putting documents or
searching. The linked guide shows both tasks and their results. The driver has
no lifecycle hooks; a new driver starts without indexes or documents.

Documents are decoded from their JSON source when they are put. Text is
lowercased and split on every character that is not a letter or digit, with
no stemming and no typo tolerance. A word term matches when each of its words
is a word of any searchable field; a phrase when its words are consecutive in
one field; the last plain word may match as a prefix; excluded terms must not
match. Every hit therefore matches every required term, and the score of a hit
is 1 when the query has a positive term and 0 for empty or exclusion-only text.
Results are ordered by the query's orders, then by score, then by ascending ID.

Filters compare strings case-insensitively and numbers numerically. A document
without a field, or with another type in it, matches no comparison on that
field. Missing sort values come last in both directions.

`Sync` records each schema and keeps the documents of existing indexes, so a
changed definition applies at once. `Check` fails with `search.ErrNotFound` for
an index that was never synced and with `search.ErrSchemaChanged` for one synced
with another definition. `Drop` removes an index with its documents.

Schema field lists and stored document bytes are copied, so edits to your input
after a call returns do not change stored state. Returned document bytes are
independent copies too.

Cancellation is cooperative: the driver checks it after acquiring a lock and
between schema, document, and result iterations. Lock acquisition, individual
JSON decodes, and sorting are not interruptible. A canceled write can leave
partial changes once mutation begins; retry or reconcile the operation rather
than assuming rollback. These are this driver's implementation details, not a
new cancellation or atomicity guarantee for every search provider.

See the [memory driver reference](https://pkg.go.dev/arkoren.dev/core/service/search/memorysearch).
