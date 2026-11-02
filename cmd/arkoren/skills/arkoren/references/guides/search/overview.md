---
title: Search Overview
nav: Overview
description: Query a configured search engine and read typed document hits without coupling application code to a backend.
order: 10
summary:
  - Separate typed index and query definitions from engine validation and backend storage.
  - Search a configured catalog and interpret document hits, scores, and bounded pages.
  - Account for registration failures, cancellation, shared drivers, and test scope.
---

# Search Overview

A catalog search needs matching words, not only exact database-field equality.
Arkoren combines full-text matching, field filters, and ordering behind a typed
search engine. Your application chooses the documents to index; the engine
returns those documents as typed hits.

## Search Boundaries

An **index** declares a document type, its identity, and the fields available
for searching, filtering, and sorting. A **query** describes which documents
to match. The **engine**, `*search.Engine`, validates these definitions, converts
documents to and from JSON, and delegates storage and matching to a **driver**.

Choose and construct a backend in [Search Drivers](drivers.md). Declare your
document shape in [Indexing Documents](indexing-documents.md), then
[synchronize its index](deploying.md) before putting documents or searching.
Registration alone does not create a backend index.

Keep authoritative records in your database or another rebuildable source.
Search does not automatically follow source changes, enforce application
permissions, or replace your database. Backend ranking and capacity differ;
facets, highlighting, and typo tolerance are outside the portable contract.

## Finding Products

Assume `engine` is configured with the populated `Products` index from
[Indexing Documents](indexing-documents.md#document-shape), and `ctx` is your
operation context. Import `search` from `arkoren.dev/core/service/search` and
`pagination` from `arkoren.dev/core/pagination`. Focused examples leave error
results visible but omit routine propagation; handle errors in your application.

For a tenant-scoped catalog search box, take `tenantID` from trusted,
authorized application state and combine it with the text and price filter:

```go
query := search.NewQuery(Products, "red shoes", func(selection *search.QueryBuilder) {
	selection.Where("tenant_id", tenantID)
	selection.Where("price", "<=", 100)
})
```

This publishes an immutable query without performing I/O. It requires both
words, allows the last word to match a prefix, and limits matches to products
in that tenant costing at most 100. Run it to fetch one window:

```go
page, err := engine.Search(ctx, query, pagination.PageParams{Page: 1, PerPage: 20})
```

`page` is a `pagination.Page[search.Hit[Product]]`. Its items contain each hit's
stored `ID`, decoded `Document`, and `Score`; its metadata describes the bounded
result set. After checking `err`, use `hit.Document.Title` to display a product.
No matches produces an empty items collection, not `search.ErrNotFound`.

## Reading Results

Without explicit sort keys, positive text matches rank by descending relevance,
then ascending document ID. Scores are comparable only within one result, not
between queries or drivers, and are not percentages. Empty or exclusion-only
text has score zero.

Search reaches only the first 1,000 hits. [Search Queries](queries.md) explains
text syntax, permission filters, explicit ordering, and this result-window bound.
The [pagination guide](../queries/pagination.md) explains the shared page types;
its SQL examples are not search-query syntax.

## Operation Boundaries

The engine rejects an invalid query before driver I/O. An unregistered index,
or a registered name used with a different field definition, returns
`search.ErrNotFound`. Invalid or duplicate registrations are retained at
construction and fail every operational call; `Sync` or `Check` exposes them
before serving requests.

Pass a context with a deadline when work must stop with its caller. Cancellation
is cooperative: individual key callbacks and JSON encoders or decoders finish
before the next facade check. A decode failure or cancellation during result
decoding returns an error, not a successful partial page. Cancellation after
writes begin does not imply rollback; see [batch behavior](indexing-documents.md#batch-behavior).

The engine is safe for concurrent use when its driver is. `Driver()` and
`DriverAs` return shared drivers, not copies. Calling them directly bypasses
facade validation, JSON conversion, telemetry, and notifications. Configure
[Monitoring](monitoring.md) on the engine to observe ordinary operations.

For application tests, use an independent [Memory driver](drivers.md#memory),
synchronize it, and put only the documents each test needs. Assert matching IDs
and explicit orders rather than backend-specific relevance scores. A custom
driver can use `searchtest.Run` from `arkoren.dev/core/service/search/searchtest`;
the [conformance guide](../testing/conformance-suites.md) explains what a suite
does and does not establish. Memory tests do not prove native backend behavior.

For every function and option, see the [search API reference](https://pkg.go.dev/arkoren.dev/core/service/search).
