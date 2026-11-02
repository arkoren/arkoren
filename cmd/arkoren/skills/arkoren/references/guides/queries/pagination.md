---
title: Pagination
description: Return result windows with offset or cursor pagination, request parameters, and cursor safety.
summary:
  - Choose numbered pages with totals or cursor windows without a count.
  - Validate request limits and provide a stable non-null ordering.
  - Sign cursor positions while keeping authorization and filter checks separate.
order: 30
---

# Pagination

Use numbered pages when users need totals or direct page selection. Use cursor
windows when they move forward or backward through a large ordered result and
do not need a total. For background processing of every row, use
[Batching and Streaming](../database/batching-and-streaming.md) instead.

Assume a configured `db` and request or job context `ctx`. Import
`arkoren.dev/foundation/query` and `arkoren.dev/core/pagination`. The examples
read `users` with a non-null unique `id` and a non-null `email`:

```go
type User struct {
	ID    int64  `db:"id"`
	Email string `db:"email"`
}
```

## Stable Ordering

Both helpers require a SELECT with an explicit order and no existing `Limit`
or `Offset`. Define the common query once:

```go
users := query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "email")
	statement.OrderBy("id")
})
```

End any compound order with a unique tiebreaker, such as `created_at` followed
by `id`. The helper requires an order but does not verify uniqueness. Reapply
authorization and tenant filters on every request, including requests with cursors.

## Numbered Pages

To fetch the third page of twenty users:

```go
page, err := db.Paginate[User](ctx, users, pagination.PageParams{
	Page:    3,
	PerPage: 20,
})
```

`Paginate` runs a count and a separate SELECT with offset 40 and limit 20.
The result exposes `Items`, `Total`, `PerPage`, `CurrentPage`, and `LastPage`.
No matches produces an empty collection and a last page of at least one. A
request past the end keeps its requested page number instead of inventing rows.

The count uses `Query.Count`, including grouped or raw-derived result sets.
Count and items are not automatically one snapshot: concurrent changes can
make them disagree unless the database isolation policy preserves a snapshot
across both statements. Deep offsets also require the server to skip preceding
rows; use cursor pagination when that cost is inappropriate.

## Cursor Windows

To read the first window without counting the full result:

```go
page, err := db.CursorPaginate[User](ctx, users, pagination.CursorParams{
	PerPage: 20,
})
```

The helper fetches up to one extra row to detect another window. `Items` contains
at most twenty users; `NextCursor` and `PrevCursor` are opaque continuation tokens.
Pass either token unchanged as `Cursor` on the next request. An empty token means
no token was emitted; an empty result has neither token. There is no total count.
Backward queries are reversed back into the declared display order.

Every ordered column must be present in the result type and selected into it.
All ordering keys in the selected dataset must be non-NULL. Fetched NULL values,
including lookahead rows, fail with `database.ErrCursorValue`; SQL seeks can
exclude NULL rows before this check can see them. Choose non-null keys or
explicitly exclude NULLs rather than relying on the check to validate the dataset.

Tokens encode boundary values and ordering, not a snapshot. Inserts, deletes,
and ordering-key updates between requests can change traversal. Keep tokens
with the same filters and authorization context. The built-in ordering fingerprint
checks columns and directions, not filters or tenant identity.

## Request Parameters

For an existing HTTP request `r`, import `arkoren.dev/core/http/request` and
parse the query parameters before execution:

```go
params, err := request.Page(r, pagination.WithMaxPerPage(50))
```

This reads `page` and `per_page`; `request.Cursor(r)` reads `cursor` and
`per_page`. Handle parsing errors before passing parameters to the facade.
The facade normalizes again with its built-in limits, so request options can
impose stricter limits but do not raise the facade's ceilings.

For directly constructed parameters, zero page selects page 1 and zero size
selects 25. HTTP parsing distinguishes absence from explicit zero: missing values
use defaults, while supplied zero or negative values become one. Size is capped
at 100, and numbered pages above 10,000 or with
overflowing offsets fail with `pagination.ErrPageOverflow`. Cursor normalization
preserves the token unchanged; validation happens when the database helper
decodes it.

Missing order and preset limits return `database.ErrNoOrder` and
`database.ErrPaged`. Cursor fields missing from the result type return
`database.ErrCursorColumn`. Malformed, incompatible-order, or invalidly signed
tokens match `pagination.ErrCursorDecode`. Do not replace these failures with
an apparently successful first page. Encoding can fail too; never return a
partial cursor result as success.

## Signed Cursors

By default, positions are unsigned base64 JSON and clients can forge them.
To reject tampering, load private application material with
`arkoren.dev/foundation/key` and construct a codec:

```go
material, err := key.LoadShared("cursor.key")
codec, err := pagination.NewCursorCodec(material)
```

The key must contain exactly 32 bytes; follow [key provisioning](../keys/overview.md)
instead of committing it. After handling those construction errors, configure
the facade using `arkoren.dev/core/service/database`:

```go
db := database.New(driver, func(connection *database.Builder) {
	connection.CursorCodec(codec)
})
```

The codec rejects unsigned, altered, and wrongly signed tokens. Enabling it
invalidates previously issued unsigned tokens. Rotate by constructing the new
codec with the current key first and previous keys afterward, then retire old
keys according to your cursor-retention policy.

Signing does not encrypt boundary values, enforce expiration, bind filters, or
authorize access. The codec provides no expiry check. Reapply access filters and
implement any additional token policy in the application. Both signed and
unsigned token formats have a 4096-byte limit.

`Items` is an eager collection for this window only. Local filtering or mapping
does not fetch rows, recompute totals, or change cursor boundaries.

See the [`database` API](https://pkg.go.dev/arkoren.dev/core/service/database) and
[`pagination` API](https://pkg.go.dev/arkoren.dev/core/pagination) for the full contracts.
