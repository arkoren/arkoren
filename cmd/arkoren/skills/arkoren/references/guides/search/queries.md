---
title: Search Queries
nav: Queries
description: Combine search text, typed filters, permission boundaries, and ordering within a bounded result window.
order: 40
summary:
  - Match words, phrases, prefixes, and exclusions without native backend syntax.
  - Build typed catalog filters and group alternatives within trusted permission boundaries.
  - Order hits and fetch pages within the first 1,000 results.
  - Validate immutable queries and distinguish local item transformations from server-side filtering.
---

# Search Queries

A search query combines text, field conditions, and result ordering. Start with
the populated [`Products` index](indexing-documents.md#document-shape) and a
configured `engine`. Import `search` from `arkoren.dev/core/service/search` and
`pagination` from `arkoren.dev/core/pagination`; `ctx` is your operation context.
These queries search indexed JSON documents, not SQL tables.

## Query Text

For a search box, pass the text to `NewQuery` without translating it to native
backend syntax:

```go
selection := search.NewQuery(Products, `red "running shoes" -boots`)
```

This requires a word starting with `red` and the consecutive phrase `running
shoes` in one field, while excluding `boots`. Only the last plain, nonexcluded
word is a prefix, even when a phrase or exclusion follows it.

| Text | Meaning |
| --- | --- |
| `red shoes` | Require both words; the last may match a prefix. |
| `"running shoes"` | Require consecutive words in one field. |
| `-boots` | Exclude a matching word. |
| `-"wool socks"` | Exclude a matching phrase. |
| `wool soc` | Require `wool` and a word beginning with `soc`. |

Matching lowercases words and splits punctuation without stemming. Nonphrase
words can match different searchable fields, including words split from one
punctuated term. `soc wool` requires the complete word `soc`; `wool soc` can
match `wool socks`. A quoted word is not a prefix.

Parsing never fails: an unclosed quote runs to the end, a minus not directly
followed by a word or quote is ordinary text, and terms without letters or
digits are dropped. Empty text, or text reduced to no terms, matches every
document unless filters restrict it. Use `NewQuery(Products, "")` for this;
the zero value of `Query[Product]` is invalid.

Meilisearch additionally folds diacritics, limits positive words and phrases to
ten, and rejects multiword nonphrase exclusions such as `-t-shirt` with
`search.ErrUnsupported`. Review [provider differences](drivers.md#matching-limits-2)
before relying on identical matches across backends.

## Catalog Filters

Keep field names under application control. For a validated catalog request,
use a typed input rather than accepting arbitrary predicates:

```go
type CatalogSearch struct {
	Text     string  `json:"text"`
	Category string  `json:"category"`
	MaxPrice float64 `json:"max_price"`
}
```

Assume `input` has already been decoded and validated. Restrict matches using
the index's declared filterable fields:

```go
selection := search.NewQuery(Products, input.Text, func(criteria *search.QueryBuilder) {
	criteria.Where("category", input.Category)
	criteria.Where("price", "<=", input.MaxPrice)
})
```

The two-argument `Where` is equality; the three-argument form accepts `=`, `!=`,
`<`, `<=`, `>`, or `>=`, either as strings or their `search.Operator` constants
such as `search.OpLessOrEqual`. Conditions append with AND. Use membership
methods, not `OpIn` passed to `Where`, for sets:

```go
selection := search.NewQuery(Products, "", func(criteria *search.QueryBuilder) {
	criteria.WhereIn("category", "boots", "socks")
	criteria.WhereNotIn("price", 15, 25)
})
```

Membership lists must be nonempty. Operands accept named or unnamed strings,
booleans, integers, floats, and exact `time.Time` values. Numbers normalize to
float64; integers above 2^53 can lose precision. Times normalize to RFC 3339 UTC
strings, so store comparable document timestamps in UTC. Booleans permit only
equality and inequality. Nil, unsupported types, NaN, and infinities invalidate
the query.

Strings compare case-insensitively. The typed contract does not equate numeric
15 with string `"15"`. Missing, null, array, and object values match no scalar
condition, including `!=` and `NOT IN`. `IN` needs one same-kind equal operand;
`NOT IN` needs every operand to have the stored kind and differ. Consequently,
a mixed-kind `NOT IN` list matches no scalar. Prefer homogeneous lists matching
the stored field. Meilisearch's native coercion remains a
[portability limit](drivers.md#compatibility-matrix), not a weaker contract.

## Permission Filters

Search does not authorize results. Derive the tenant from authenticated,
authorized application state, not from a client-supplied filter. For a trusted
`tenantID`, make the permission boundary part of every search:

```go
selection := search.NewQuery(Products, input.Text, func(criteria *search.QueryBuilder) {
	criteria.Where("tenant_id", tenantID)
	criteria.WhereGroup(func(availability *search.PredicateBuilder) {
		availability.Where("in_stock", true)
		availability.OrWhere("backorder", true)
	})
})
```

The expression is `tenant_id = tenantID AND (in_stock OR backorder)`. Without
the group, `tenant AND in_stock OR backorder` means `(tenant AND in_stock) OR
backorder` and can expose another tenant's documents. AND binds tighter than
OR. Never append a root `OrWhere` alternative that escapes a permission filter.

Group callbacks receive `*search.PredicateBuilder`, which supports predicates
and nested groups but no ordering. Callbacks share one draft; their final
combined group must be nonempty.

For a cheaper view, derive a query without changing this permission-scoped base:

```go
discounts := selection.Derive(func(criteria *search.QueryBuilder) {
	criteria.Where("price", "<", 50)
})
```

`Derive` preserves text, existing conditions, orders, and the first error; it
adds settings rather than replacing existing restrictions. This is a convenience,
not an authorization object: later application code must still preserve the
logical boundary. If permissions can change before reindexing, recheck them
against authoritative state before disclosing sensitive documents. Filtering the
fetched page afterward does not repair leaked counts or a wrongly scoped search.

## Result Ordering

To show cheaper products first and newer ones first at the same price, publish
an ordered variant of your scoped query:

```go
ordered := selection.Derive(func(criteria *search.QueryBuilder) {
	criteria.OrderBy("price")
	criteria.OrderByDesc("created_at")
})
```

Only declared sortable fields are accepted. Orders apply in declaration order,
then descending relevance, then ascending document ID by bytes. Strings sort by
lowercase bytes, numbers numerically, and missing values last in either
direction. Keep one scalar kind per field; mixed-kind ordering is undefined.

With no explicit orders, positive text ranks by relevance then ID. Empty or
exclusion-only text has score zero and falls back to ID. Scores are comparable
only within one result, not across queries or drivers. Meilisearch's string/ID
length limits and OpenSearch's ID-fielddata requirement can affect this ordering;
see [Search Drivers](drivers.md#compatibility-matrix).

## Result Windows

Run the query with page-number parameters to fetch a bounded window:

```go
page, err := engine.Search(ctx, ordered, pagination.PageParams{Page: 3, PerPage: 20})
```

Zero values normalize to page 1 and 25 items; the default per-page maximum is
100. The [pagination guide](../queries/pagination.md) explains these shared page
types and normalization, not search text or backend execution.

`search.MaxHits` is 1,000. A normalized page times per-page above that bound
returns `search.ErrInvalidQuery` before driver I/O. `Total` is exact up to
1,000 and never reports more. `LastPage` uses that capped total, but does not
override the window check: at 30 items per page and a total of 1,000, its value
is 34 and requesting page 34 fails because its window ends at 1,020. Use a page
size that divides 1,000, such as 20 or the default 25, to navigate the full cap.
Ask readers to narrow large searches instead of offering arbitrarily deep pages.
A window past the last match is empty, not an error, when it stays within the
bound.

Pages are not a snapshot or cursor traversal. Concurrent document changes can
alter contents between requests. The bound limits reachable results, not the
backend's indexing work or scan cost.

After checking the search error, extract documents from the current page:

```go
products := page.Items.Map(func(_ int, hit search.Hit[Product]) Product {
	return hit.Document
})
```

`Map`, `Filter`, and `GroupBy` are eager, local collection operations. They visit
only the fetched window, make no search requests, and do not recompute page
metadata. Put conditions in the search query when they must change server-side
counts or ordering. JSON serializes items as an array with the page metadata.

## Query Validation

`NewQuery` publishes an immutable value without I/O. Its first declaration
error is retained by `Err()` and returned by `Search` before reaching the driver:

```go
if err := selection.Err(); err != nil {
	return err
}
```

Undeclared filter/sort fields, unsupported values/operators, empty membership
lists, and empty groups wrap `search.ErrInvalidQuery`. A zero query also wraps
the missing index's `search.ErrInvalidName`; deriving it does not repair that
failure. Registration and schema matching are checked separately by the engine.
Native provider limits can still return `search.ErrUnsupported` for a valid
query. Never retry by silently dropping a filter, especially a permission filter.

Callbacks run synchronously once in order even for invalid bases. Supplied nil
callbacks panic; expanded empty callback slices omit edits. Empty groups remain
invalid. Publication, derivation, and `Inspect()` detach owned text, condition,
group, membership, and order containers; editing a retained draft or inspection
cannot change a published query.

When user input makes a query invalid, classify its error at your
[HTTP boundary](../http-fundamentals/error-handling.md). Prefer fixed fields and
validated values instead of exposing the complete predicate builder to clients.

For every function and option, see the [search API reference](https://pkg.go.dev/arkoren.dev/core/service/search).
