---
title: Indexing Documents
description: Declare typed search documents and field policies, replace indexed records, and remove documents with explicit batch and retry boundaries.
order: 30
summary:
  - Define a minimal typed document and immutable searchable, filterable, and sortable field policies.
  - Replace complete document projections and delete authorized source-record IDs.
  - Bound backfills and distinguish preflight validation from partial backend writes and safe retries.
---

# Indexing Documents

A search document is a projection of a record your application owns elsewhere.
Keep only the fields search needs, and retain a source from which you can rebuild
the index. Arkoren stores the JSON you send; it does not load records, follow
database changes, or remove private fields for you.

## Document Shape

For a tenant-scoped catalog, define the searchable representation explicitly.
Import `search` from `arkoren.dev/core/service/search` and standard-library
`time` for the timestamp:

```go
type Product struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Price       float64   `json:"price"`
	InStock     bool      `json:"in_stock"`
	Backorder   bool      `json:"backorder"`
	CreatedAt   time.Time `json:"created_at"`
}
```

Fields in a declaration refer to top-level JSON keys, not Go field names or
database `db` tags. Use UTC timestamps for text-based time comparisons and keep
one scalar kind per filterable/sortable field across documents. Searchable
fields contain strings; filterable and sortable fields contain strings, numbers,
or booleans.

The whole encoded document is stored and returned, including fields not listed
in the index policy. Excluding a field from `Searchable` does not make it private.
Do not index secrets or unnecessary personal data. Tenant fields permit
[permission filters](queries.md#permission-filters); they do not enforce them
automatically.

## Field Policies

Declare which fields each search operation can use, and select a stable document
ID from the source record:

```go
var Products = search.NewIndex("products", func(product Product) string {
	return product.ID
}, func(fields *search.IndexBuilder) {
	fields.Searchable("title", "description")
	fields.Filterable("tenant_id", "category", "price", "in_stock", "backorder", "created_at")
	fields.Sortable("price", "created_at")
})
```

`Products` is immutable. Builder methods append fields; publication copies
field lists and detaches retained drafts. Field getters and `Schema()` return
sorted, deduplicated copies. The key function and its captured state are not
cloned, so keep identity selection stable and safe for concurrent calls.

IDs must be unique within the whole index, including across tenants. A tenant
filter does not partition document identity; two records with the same ID
replace each other. Use source IDs that already satisfy that scope, or encode
both source and tenant identity into an allowed stable ID.

Index and field names are 1–64 bytes of lowercase letters, digits, and
underscores. A declaration needs a key function and at least one searchable
field. `Products.Err()` wraps `search.ErrInvalidName` for an invalid declaration.
Callbacks run synchronously once in order; supplied nil callbacks panic, while
expanded empty callback slices add no edits. Validation uses the completed draft.

Register `Products` with `Builder.Indexes` when constructing the engine in
[Search Drivers](drivers.md), then [synchronize it](deploying.md) before writes.
Duplicate names and invalid registrations fail every operational engine call.
Custom `search.Indexer` implementations must provide both `Schema()` and `Err()`;
registration checks `Err()` first. Keep custom definitions stable for the engine's
lifetime and report complete validation failures, not only schema-visible ones.

## Putting Documents

Assume `engine` registers `Products`, synchronization succeeded, and `ctx` is
your operation context. Index a product already validated and saved in your
source of truth:

```go
product := Product{
	ID:       "p-17",
	TenantID: "shop-4",
	Title:    "Red running shoes",
	Category: "shoes",
	Price:    85,
	InStock:  true,
}
err := engine.Put(ctx, Products, product)
```

`Put` encodes the value with `encoding/json/v2`, derives its ID with the key
function, and upserts it. After success it is searchable, subject to
[Postgres transaction visibility](drivers.md#matching-limits-1). A missing backend
index returns `search.ErrNotFound`; writes do not implicitly synchronize it.

To reflect a changed source record, put the complete projection again:

```go
product.Price = 79
err = engine.Put(ctx, Products, product)
```

The stored document is replaced entirely, not patched. A partially populated
`Product` replaces prior values with the JSON that value encodes. There is no
portable partial-update operation. Source writes and search writes are separate
unless you deliberately use the Postgres driver's shared database transaction.
For other backends, schedule indexing after a successful source commit and
retain enough information to retry failed updates.

## Removing Documents

When an authorized source deletion or visibility change removes products from
search, delete their stable IDs:

```go
err := engine.Delete(ctx, Products, "p-17", "p-18")
```

Missing document IDs succeed, so retries are safe. An absent index still returns
`search.ErrNotFound`. IDs are 1–255 bytes of ASCII letters, digits, hyphens, and
underscores; invalid IDs return `search.ErrInvalidID` before deletion starts.
Select deletion targets from trusted application state, not an unauthorized
request's arbitrary IDs.

`Delete` removes documents, not the index declaration. `Drop` destroys the entire
backend index and its documents; keep that action in a planned
[deployment or rebuild](deploying.md#retiring-indexes), never an ordinary document
update.

## Batch Behavior

For a backfill, submit a bounded source batch such as an existing `[]Product`
named `products`:

```go
err := engine.Put(ctx, Products, products...)
```

The facade validates every ID and encodes every document before the first driver
write. Invalid IDs, encoding failures, or values that do not encode as JSON
objects write nothing. Duplicate IDs within one call keep the last document.
An empty `Put` or `Delete` does no driver work after definition/context checks.

This preflight is not a transaction. Drivers can split large batches; a later
failure or cancellation can follow successful writes. Repeating the same upsert
batch finishes it safely, but coordinate retries with newer source updates so
an old retry does not overwrite a newer projection. Meilisearch can continue a
server task after the caller stops waiting. Consult the
[provider limits](drivers.md#compatibility-matrix) for reserved fields, field
lengths, and native task behavior.

Choose application batch sizes to bound encoding memory and retry work; there
is no portable streaming bulk writer. Cancellation is checked between key and
encoding callbacks and before passing the batch to the driver, not inside an
individual callback. Once driver work begins, reconcile outcomes rather than
assuming cancellation rolled them back.

For every function and option, see the [search API reference](https://pkg.go.dev/arkoren.dev/core/service/search).
