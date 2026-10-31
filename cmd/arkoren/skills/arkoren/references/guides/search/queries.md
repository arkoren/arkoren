---
title: Search Queries
nav: Queries
description: Write search queries with text syntax for words, phrases, exclusions, and prefixes, add filters, order results, and page within 1,000 hits.
order: 20
---

# Search Queries

## Introduction

To turn a search-box value into a product list, combine text matching with
field filters and an order. Start with the populated `Products` index and
`engine` from [Search Overview](overview.md).
Import `search` from `arkoren.dev/core/service/search` and `pagination` from
`arkoren.dev/core/pagination`. Here `ctx` is your operation context and `since`
is your application's `time.Time` cutoff. Focused snippets omit routine error
checks but leave their error results visible.

A query combines up to three things: text that the matching documents must
contain, filters on fields, and an order. All three are optional. A query with
no text and no filters matches every document in the index, which is how you
list or page a whole index.
This means a query constructed with `search.NewQuery(Products, "")`, not the
zero value of `search.Query[Product]`, which has no valid index.

You publish an immutable query with `search.NewQuery(index, text, configure...)`.
Each `func(query *search.QueryBuilder)` callback edits the same fresh draft.
Settings append and return that draft. The first problem is kept and reported
by `query.Err()`. `engine.Search` returns it, wrapping `search.ErrInvalidQuery`,
before calling the driver.

```go
query := search.NewQuery(Products, `red "running shoes" -boots`, func(query *search.QueryBuilder) {
	query.Where("category", "shoes")
	query.Where("price", "<=", 100)
	query.OrderBy("price")
})
```

This describes products containing a word starting with `red` and the phrase
`running shoes`, but not `boots`, in the `shoes` category and costing at most
100. Price sorts ascending; relevance and ID break ties. Constructing the query does not run
it: pass it to `engine.Search(ctx, query, pagination.PageParams{})` to get the
first page. [Pagination](#pagination) explains its defaults and 1,000-hit bound.

If several screens share a query but one needs a lower price limit, use
`Derive` to add local settings without changing the published base:

```go
cheap := query.Derive(func(query *search.QueryBuilder) {
	query.Where("price", "<", 50)
})
```

The added price condition is ANDed with the inherited conditions; it does not
replace the limit of 100. The base text, conditions, orders, and first error
are preserved. No callbacks means no edits. Callbacks run directly once,
synchronously in order, even for
an invalid query. Supplied nil functions panic; expanded nil/empty slices omit
edits. Publication and derivation detach owned containers from retained drafts;
`Inspect()` also copies text, conditions, orders, nested groups, and membership
lists. Editing an inspected container cannot change the base or a derived query.
Deriving a zero query does not repair it: callbacks still run, but the inherited
missing-index error remains and execution fails before driver I/O.

## Query text

The text is what a person typed into a search box, so it has a small syntax
and never fails to parse:

| You type | Meaning |
| --- | --- |
| `red shoes` | Every word is required. |
| `"running shoes"` | A phrase: the words must be next to each other, in this order, in one field. |
| `-boots` | Exclude documents that match the word. |
| `-"wool socks"` | Exclude documents that match the phrase. |
| `soc` (last plain word) | The last word that is neither a phrase nor an exclusion may match as a prefix, so `soc` finds `socks`. |

Words are compared without regard to case. A word that contains punctuation
splits into several words that must all appear, so `red-shoes` needs `red` and
`shoes`. A quote that is never closed runs to the end of the text, a minus sign
that is not directly followed by a word or quote is an ordinary character, and
any term without a letter or digit is dropped, so `!!!` and `-` cannot break a
search. Text that ends up with no terms matches every document.

Only the last plain word is a prefix. In `soc wool`, `soc` must be a complete
word, and nothing matches it; in `wool soc`, the second word is the prefix and
the query finds `wool socks`. A prefix lets a search-as-you-type box work
without extra code, but it also means the last word of every query is partly
guessed.

> [!NOTE]
> Nonphrase words can match different searchable fields, including words split
> from one punctuated term. Meilisearch rejects a nonphrase exclusion that splits
> into multiple words, such as `-t-shirt`, with `search.ErrUnsupported`; it does
> not silently turn it into a phrase exclusion. See its
> [matching limits](drivers.md#behavior-and-limits-3).

## Filters

`Where` adds a condition on a filterable field. With two arguments the
condition is equality; with three, the middle one is the operator:

```go
filtered := query.Derive(func(query *search.QueryBuilder) {
	query.Where("category", "shoes") // category = "shoes"
	query.Where("price", search.OpLessOrEqual, 100) // same as "<="
	query.Where("in_stock", true)
	query.Where("created_at", ">=", since) // since is a time.Time
})
```

The operator may be a string (`=`, `!=`, `<`, `<=`, `>`, or `>=`) or its typed
`search.Operator` constant: `OpEqual`, `OpNotEqual`, `OpLess`, `OpLessOrEqual`,
`OpGreater`, or `OpGreaterOrEqual`, respectively. `OrWhere` and grouped predicates
accept the same forms. Use the separate membership methods below rather than
passing `OpIn` or `OpNotIn` to `Where`.

A value is a string, a boolean, any integer or float type, or a `time.Time`.
Numbers are compared as floating-point numbers, so integers above 2^53 lose
precision. A `time.Time`
is converted to an RFC 3339 string in UTC, so a time in any zone means the same
instant, and the field in your documents should hold a UTC timestamp for the
comparison to work as expected. Booleans support only `=` and `!=`. Anything
else, such as `nil` or a struct, makes the query invalid. NaN and infinities
are also invalid.

The contract compares strings without regard to case: `Where("category", "SHOES")`
matches `shoes` or `Shoes`. Comparisons are type-sensitive: a number is not a
string containing digits. Missing, null, array, and object values match no scalar
condition, `!=` included. Keep one scalar kind per field across documents;
ordering mixed field kinds is undefined.

Memory, Postgres, and OpenSearch perform typed comparisons. Meilisearch's native
filter coercion has [portability limits](drivers.md#compatibility-matrix), even
when the facade accepts your query. Keep operand types aligned with stored
fields; native coercion is not a relaxation of the contract.

For a membership condition, pass a nonempty list to `WhereIn` or `WhereNotIn`:

```go
filtered := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
	query.WhereIn("category", "boots", "socks")
	query.WhereNotIn("price", 15, 25)
})
```

`WhereIn` needs an equal operand of the stored scalar's kind. `WhereNotIn` needs
every operand to have that kind and differ from the stored value. A numeric field
therefore does not match `WhereNotIn("price", "15")`. A mixed-kind list such as
`WhereNotIn("price", []any{15, "15"}...)` is a valid declaration but matches no
scalar under the typed contract, regardless of operand order. Missing, null,
array, and object fields match neither membership operator. OpenSearch also
compiles mixed-kind `NOT IN` to match nothing and keeps homogeneous comparisons
in separate typed containers. Meilisearch's native coercion limits still apply;
prefer homogeneous lists matching the field kind.

### Combining conditions

The following alternatives start a fresh query so the introductory query's
category and price filters do not change their meaning.

Conditions are joined with AND. `OrWhere` joins a condition to the previous
one with OR, and AND binds tighter than OR, as in SQL:

```go
query := search.NewQuery(Products, "", func(query *search.QueryBuilder) {
	query.Where("category", "socks")
	query.Where("in_stock", true)
	query.OrWhere("category", "boots")
	// (category = socks AND in_stock) OR category = boots
})
```

To control precedence, put conditions in a group, which is a function that
receives a restricted `*search.PredicateBuilder` on the same index:

```go
query := search.NewQuery(Products, "shoes", func(query *search.QueryBuilder) {
	query.Where("category", "shoes")
	query.WhereGroup(func(predicate *search.PredicateBuilder) {
		predicate.Where("in_stock", true)
		predicate.OrWhere("backorder", true)
	})
})
// category = shoes AND (in_stock OR backorder)
```

A predicate draft has no ordering methods. Group callbacks share one draft,
run directly once in order, and validate the final combined expression. An
earlier empty callback is valid if a later one adds a condition; a final empty
group is invalid. Zero callbacks (also expanded nil/empty slices) make an empty
group, while a supplied nil callback panics.

## Ordering

To show cheaper products first and newer products first within each price,
use `OrderBy` and `OrderByDesc`. They sort by a sortable field, and several calls sort
by each key in turn:

```go
filtered := query.Derive(func(query *search.QueryBuilder) {
	query.OrderBy("price").OrderByDesc("created_at")
})
```

For an unchanged index, a result has a deterministic order within one driver:

1. The query's orders, in the order you added them. Strings sort by their
   lowercase bytes, numbers numerically, and documents that lack the field come
   last in both directions.
2. Relevance, highest score first, when the query has text.
3. The document ID, ascending, by bytes.

With text and no `OrderBy`, results are therefore ranked by relevance and
equal scores fall back to the ID. With no text and no `OrderBy`, results come
back in ID order. An order that has ties, such as sorting by `price` when two
products cost the same, falls back to relevance and then ID. Relevance differs
between drivers and can change when documents change, so neither cross-driver
ordering nor a snapshot across page requests is guaranteed.

## Pagination

`Search` takes `pagination.PageParams`. Zero values use the usual defaults (page
1, 25 per page, at most 100 per page), and the page is normalized the same way
as for [database pagination](../database/pagination.md):

```go
page, err := engine.Search(ctx, query, pagination.PageParams{Page: 3, PerPage: 20})
// page.Items, page.Total, page.CurrentPage, page.LastPage
```

Search backends cannot page arbitrarily deep, so a search reaches only the
first 1,000 hits (`search.MaxHits`). A page whose last item would be beyond
that, that is, when page times per-page is above 1,000, fails with
`search.ErrInvalidQuery`. `Total` is exact up to 1,000 and never reports more,
so `LastPage` never points past what you can request. When a result is larger,
ask the user to narrow the query instead of offering page 500.

A page past the last one is not an error if its requested window stays within
the 1,000-hit bound; it returns no items. Search uses page numbers, not cursor
pagination, and changing documents between requests can change page contents.

To prepare the fetched hits for display, use the eager collection in
`page.Items` directly. After checking the search error, extract the current
window's typed documents:

```go
products := page.Items.Map(func(_ int, hit search.Hit[Product]) Product {
	return hit.Document
})
```

`products` preserves the window's order. `page.Items.Filter` and `GroupBy` also
operate locally; hit values retain their document ID and relevance score unless
your callback replaces them. These are in-memory operations, not streaming or
another search. They visit only the fetched page and do not recompute `Total`,
`LastPage`, or `PerPage`, even if you replace `page.Items` with filtered hits.
Put filters in the search query when they must change server counts or ordering;
there is no collection operation that traverses all search results for you.
JSON still emits `items` as an array with the same metadata fields.

## Checking a query before running it

`query.Err()` returns the first problem without running anything, which is useful
when the query is built from user input in a handler:

```go
if err := query.Err(); err != nil {
	return err // wraps search.ErrInvalidQuery
}
```

An undeclared field, a field used for the wrong purpose (a condition on a
field that is not filterable), an unsupported value type, an unknown operator,
and an empty membership list are all invalid queries. A zero query's `Err()`
wraps both `search.ErrInvalidQuery` and the missing index's `search.ErrInvalidName`;
`Derive` preserves that failure, and `Search` rejects it before calling the driver.
Map `search.ErrInvalidQuery` to a 400 response with the
[`errormap` middleware](../http-fundamentals/error-handling.md)
when users can influence field names. Prefer fixed, allowed fields when users
only need to supply text and values; still validate the values, since unsupported
types, NaN, and infinities also make a query invalid.

For every function and option, see [the `search` API
reference](https://pkg.go.dev/arkoren.dev/core/service/search).
