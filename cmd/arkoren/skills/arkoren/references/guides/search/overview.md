---
title: Search Overview
nav: Overview
description: Define an index for a document type, register it with a search engine, put documents in, and search them with typed hits and pages.
order: 10
---

# Search Overview

## Introduction

When customers search a product catalog, they need matching products rather
than exact database-field equality. Full-text search finds records by words
and, unless you request another order, returns the most relevant first.
Arkoren lets you index documents of one type, narrow matches with filters,
sort them, and page through them. Facets, highlighting, and typo tolerance
are outside the portable contract because backends handle them differently.

The contract has three parts. An **index** is a definition you write once, in
code, for one document type: its name, how a document names itself, and which
fields take part in searching, filtering, and sorting. The **engine**,
`*search.Engine`, is what your code uses: it registers your indexes, encodes
documents as JSON, validates input, pages results, and records
[telemetry](../observability/instrumented-components.md#search). A **driver**
stores the documents and runs the queries: [memory](drivers.md#memory) for
tests and local development, and [Postgres](drivers.md#postgres),
[Meilisearch](drivers.md#meilisearch), and [OpenSearch](drivers.md#opensearch)
for production. Your typed index, query, and engine calls stay the same when
you switch drivers; connection setup and deployment requirements change.

The definition is not the stored index: register it with the engine, run
`Sync` to prepare the backend, then `Put` documents before searching them.
Keep the original records in your database or another source you can rebuild
from. Search does not synchronize that source automatically.

Because the engine, not the driver, validates names, document IDs, queries,
and page limits, every driver behaves the same on that invalid input. Ranking
and backend limitations still differ; review [Search Drivers](drivers.md)
before choosing a production backend.

## Defining an index

Install `arkoren.dev/core` with `go get` in your application's Go module.
Import `search` from `arkoren.dev/core/service/search`, `pagination` from
`arkoren.dev/core/pagination`, and standard library `time`, `fmt`, and `testing`
where used below. Examples use your operation context `ctx`.

To let customers find products by their title or description, first define the
document you want to search. This catalog also includes fields used by the
[query examples](queries.md):

```go
type Product struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Price       float64   `json:"price"`
	InStock     bool      `json:"in_stock"`
	Backorder   bool      `json:"backorder"`
	CreatedAt   time.Time `json:"created_at"`
}
```

With that document shape defined, write a function that returns its ID:

```go
func productID(product Product) string {
	return product.ID
}
```

Declare the index using `arkoren.dev/core/service/search`. An index is an
immutable value you usually keep in a package-level variable. Pass the ID
function and configure the fields:

```go
var Products = search.NewIndex("products", productID, func(index *search.IndexBuilder) {
	index.Searchable("title", "description")
	index.Filterable("category", "price", "in_stock", "backorder", "created_at")
	index.Sortable("price", "created_at")
})
```

Field names are the top-level keys of the JSON a document encodes to, so the
struct needs `json` tags for every name the index uses. Each list says what
the field may be used for:

- **Searchable** fields hold strings. Query text matches their words. An index
  needs at least one.
- **Filterable** fields can appear in conditions such as `price <= 100`.
- **Sortable** fields can order results.

A field may appear in several lists, as `price` could be both filterable and
sortable. Declare only the fields you need: searchable fields affect indexing
work, and filterable or sortable fields can add backend indexing and storage costs.

Index and field names are 1 to 64 bytes of lowercase letters, digits, and
underscores. Configure fields in `func(index *search.IndexBuilder)` callbacks;
settings append and return the same draft. Callbacks run directly once, in
order on one draft. Supplied nil callbacks panic; expanded nil/empty slices
omit edits. `Schema()` and field getters return sorted, deduplicated copies;
changing a returned list cannot change the index.

Publication copies input field lists and detaches retained drafts, so later
edits cannot change the index. Validation checks the completed definition,
not an intermediate draft missing its searchable fields. An invalid name, a
missing searchable field list, or a nil key function is reported by
`Products.Err()`, wrapping `search.ErrInvalidName`. Operational engine calls
reject invalid definitions before driver I/O.

> [!NOTE]
> Text processing is language-neutral. Every driver lowercases text and splits
> it on anything that is not a letter or digit, without stemming or typo
> tolerance, so a search for `shoe` does not find `shoes` unless you type it as
> a prefix (see [Query Text](queries.md#query-text)). There is no configurable
> language-aware analyzer in the portable contract.

## Creating the engine

Build a driver and pass it to `search.New` with the indexes the application
uses:

```go
import (
	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch"
)

driver := memorysearch.New()
engine := search.New(driver, func(engine *search.Builder) {
	engine.Indexes(Products)
})
```

Each memory driver keeps independent process-local state and loses its indexed
documents on process exit.

The core facade's `search.New` does no I/O and returns an engine even if a
registration is invalid. Registration checks each `Indexer.Err()` before
capturing its schema, then validates and copies the field lists. `Sync` and
`Check` receive separate copies, so a driver cannot rewrite the
engine's registrations by editing those containers. This copies known containers,
not custom index implementations, key-function captures, or shared services.

Native drivers still have their own validation and startup requirements:
[`opensearchsearch.New`](drivers.md#connecting-2) now publishes configuration
without constructing its SDK client, and `Start` creates that client and reports
native URL/certificate errors.

Registering an index does not create it in the backend; that is the separate
deploy step [`Sync`](deploying.md). Before putting documents into this local
memory engine, create its registered indexes:

```go
err := engine.Sync(ctx)
```

Here `ctx` is your operation's `context.Context`. A successful sync lets the
following `Put` and `Search` calls use the index. Memory needs no lifecycle hooks;
the [deployment guide](deploying.md) covers synchronization during application startup.

Every call that names an index not passed to `Builder.Indexes`, or the same name
with a different definition, fails with `search.ErrNotFound` before reaching
the driver. Two indexes with one name, or an invalid index, make `New` keep
the error and return it from every operational call, so `Sync` or `Check` exposes
the mistake at startup. `Driver` and `DriverAs` remain unvalidated accessors.
Custom implementations need the [required validation method](#migrating-custom-index-declarations).

## Putting documents in

The examples use existing `Product` values named `shoes`, `boots`, and `socks`.
Focused snippets keep error results visible but omit routine error checks;
handle those errors in your application.

`Put` encodes documents as JSON, takes each ID from the index's key function,
and stores them:

```go
err := engine.Put(ctx, Products, shoes, boots, socks)
```

After a successful `Put`, the documents are searchable, subject to
[database transaction visibility](drivers.md#indexing-and-searching-1).
A document whose ID is already stored replaces it entirely, so `Put` is an
upsert and safe to repeat after a failure. If one call holds the same ID twice,
the last one wins.

IDs are 1 to 255 bytes of letters, digits, hyphens, and underscores, the
alphabet every backend accepts. `Put` checks every ID and encodes every
document before it writes anything, so an invalid one fails with
`search.ErrInvalidID` and nothing is stored. Beyond that check, a large call
can be split by the driver into several writes and is not atomic across them;
because writes are upserts, calling `Put` again with the same documents
finishes the job.

`Delete` removes documents by ID and succeeds for IDs that are not stored:

```go
err := engine.Delete(ctx, Products, "p-17", "p-18")
```

`Drop` permanently removes a whole index with its documents, making search
unavailable until you synchronize and repopulate it. Keep rebuildable source
data and use a planned maintenance window for a destructive rebuild. It succeeds when the index
does not exist. It is never part of `Sync`, so removing an index from your
code leaves its data in the backend until you drop it.

Keeping the index in step with your database is your job: call `Put` and
`Delete` where you write the record, or from a job you dispatch after the
write. The search index is a copy you can rebuild with `Put`, never the only
place a record lives.

## Searching

After putting the products, find those matching `red shoes` and costing at most
100. `Search` takes that query and pagination parameters and returns a page of hits:

```go
query := search.NewQuery(Products, "red shoes", func(query *search.QueryBuilder) {
	query.Where("price", "<=", 100)
})

page, err := engine.Search(ctx, query, pagination.PageParams{Page: 1, PerPage: 20})

for _, hit := range page.Items {
	fmt.Println(hit.ID, hit.Score, hit.Document.Title)
}
```

The result is a `pagination.Page[search.Hit[Product]]`, the same page type
[database pagination](../database/pagination.md) returns, so a handler can
return it as JSON unchanged. Each `Hit` carries the document ID, the document
decoded into your type, and a relevance score. A higher score is more
relevant, scores are comparable only inside one result, and the score is 0
when the query has no positive terms, including exclusion-only text. Do not
compare scores from different queries or drivers, and do not show them to users
as percentages.

Writing queries (text syntax, filters, ordering, and the limits on paging) is
covered in [Search Queries](queries.md). A query with an error, such as a
field that is not declared, fails `Search` with `search.ErrInvalidQuery`
before any I/O.

## Cancellation

Pass a context with a deadline for work that must stop when its caller leaves.
The facade checks cancellation between document encodes, after key callbacks,
during ID validation, and between result decodes. Cancellation observed before
the driver receives an encoded batch prevents that write; cancellation observed
while decoding results returns an error rather than a successful partial page.
An individual key callback, encoder, or decoder is not interrupted.

Once driver work begins, cancellation does not imply rollback or atomicity.
Writes or synchronization may already have changed part of the backend; retry
idempotent writes or reconcile the result. The memory driver's additional
[cancellation checks](drivers.md#behavior-and-limits) do not establish the same
timing guarantees for native providers.

## Events and telemetry

To select telemetry explicitly, pass `Builder.Telemetry` in the trailing
`search.New` callback; otherwise operations follow the process default.
`Builder.Name` labels telemetry when a process has several engines without
renaming their indexes.

To observe deployment operations, opt in with `Builder.Events(bus)` at engine
construction. The [administrative notifications](deploying.md#sync) report
observed `Sync` and `Drop` outcomes, not query or document operations. Nil
disables notifications. They contain no index names or document/query data and
establish neither an atomic commit nor index consistency: synchronization can
succeed without changing anything, and failure may follow partial changes.
These best-effort in-process observations are not a durable audit log.

With telemetry enabled, `Sync`, `Check`, `Put`, `Delete`, `Drop`, and `Search`
emit one client span named `search {operation}` and the
`arkoren.search.operations` counter and `arkoren.search.operation.duration`
histogram. They record the operation, engine name, and index name, and a
search records its hit count on the span. They never record query text or
documents, because both can hold personal data. The names and labels are
listed under [instrumented components](../observability/instrumented-components.md#search).
`Driver` and `DriverAs` emit no telemetry. Calling the returned driver directly
bypasses facade validation, JSON conversion, telemetry, and notifications.

## Testing

Use the memory driver in application tests instead of a fake. Sync your
indexes, put the documents a test needs, and search through the same engine
your code uses:

```go
func TestProductSearch(t *testing.T) {
	shoes := Product{ID: "p1", Title: "Red running shoes"}
	boots := Product{ID: "p2", Title: "Leather boots"}
	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
		engine.Indexes(Products)
	})
	if err := engine.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Put(t.Context(), Products, shoes, boots); err != nil {
		t.Fatal(err)
	}

	page, err := engine.Search(t.Context(), search.NewQuery(Products, "boots"), pagination.PageParams{})
	if err != nil || page.Total != 1 {
		t.Fatalf("Search = %+v, %v", page, err)
	}
}
```

Behavior that depends on a backend's ranking, such as which of two matching
documents scores higher, is not part of the contract, so assert on which
documents match and on explicit orders, not on score order. The
`searchtest.Run` conformance suite checks a driver against the contract; see
[Search Drivers](drivers.md).

## Migrating custom index declarations

If your application implements `search.Indexer` instead of using `Index[T]`,
it must now supply both `Schema() search.Schema` and `Err() error`. The shipped
`Index[T]` already implements both; applications using `NewIndex` need no adapter.

Add `Err` to your custom implementation and report its complete definition
failures, including settings such as a nil document-key function that a schema
cannot represent. If your implementation wraps an `Index[T]`, delegate validation
to that definition's `Err`. Return nil only for a valid complete definition; do
not hide validation inside `Schema` or add an optional capability interface.
Keep the definition stable while an engine uses it. Registration calls `Err`
first and preserves its cause under `search.ErrInvalidName`; invalid registration
never reaches the driver through an operational engine call.

For every function and option, see [the `search` API
reference](https://pkg.go.dev/arkoren.dev/core/service/search).
