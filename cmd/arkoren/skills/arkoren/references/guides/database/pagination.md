---
title: Pagination
description: Page through query results by page number or cursor, parse page parameters from a request, return JSON, and process large sets in batches.
order: 60
---

# Pagination

## Introduction

A list endpoint cannot return a million rows, and a client rarely wants more
than a screenful. **Pagination** splits a result into pages that a client
requests one at a time. The database facade supports two styles, and the
choice between them shapes both your query and your API.

**Offset pagination** numbers the pages. The client asks for page 3, and the
response says how many rows and pages exist in total, which is what a "page 3
of 12" navigation needs. **Cursor pagination** gives the client an opaque
token that marks where the last page ended, and the client sends it back to
get the next page. It has no page numbers and no total, but avoids scanning
past all the rows in earlier pages when the ordered columns are indexed.

Both work on the [query builder](query-builder.md), return a typed result you
can send straight to the client, and read their parameters from the request
with the `request` package. The result types, parameters, and cursor codecs
live in the shared `arkoren.dev/core/pagination` package, which both `database`
and `request` use; this guide covers running the queries.

Examples assume the configured `db` from the [database overview](overview.md).
Import `arkoren.dev/foundation/query` for statements and
`arkoren.dev/core/pagination` for parameters. HTTP fragments additionally use
`arkoren.dev/core/http/request` and `arkoren.dev/core/http/response`; `time`
comes from the standard library.

## Choosing between them

Use offset pagination when the client needs page numbers or a total, as in an
admin table, and deep pages are rare. It runs a count query on every request.
Inserts and deletes between requests can shift rows between pages.

Use cursor pagination for feeds, infinite scroll, and programs that walk large
result sets. It seeks from a known row instead of skipping earlier rows. The
trade-off is no arbitrary page jumps or total, and an order with non-null
columns ending in a unique key. Index those columns in the query's order.

Neither style is a snapshot across requests. Keep filters and ordering the
same while paging. With cursors, inserts and deletes do not shift the
position as offsets do, but changes to a row's ordered values can still make
it repeat or disappear.

## Offset pagination

`Paginate` counts the rows a query selects and fetches one page of them. The
examples use a `Post` struct and a function that builds the base query:

```go
type Post struct {
	ID        int64     `db:"id"         json:"id"`
	Title     string    `db:"title"      json:"title"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

func published() query.Query {
	return query.Table("posts", func(statement *query.Builder) {
		statement.Select("id", "title", "created_at")
		statement.Where("published", true)
	})
}
```

To fetch the third page, order the query and pass the page parameters:

```go
params := pagination.PageParams{Page: 3, PerPage: 3}
page, err := db.Paginate[Post](ctx, published().Derive(func(statement *query.Builder) {
	statement.OrderByDesc("created_at")
	statement.OrderBy("id")
}), params)
```

`Paginate` runs two queries: one `COUNT(*)` for the total, and one for the
page itself. For page 3 with three rows per page, PostgreSQL receives:

```sql
SELECT COUNT(*) FROM "posts" WHERE "published" = $1
SELECT "id", "title", "created_at" FROM "posts" WHERE "published" = $1 ORDER BY "created_at" DESC, "id" ASC LIMIT 3 OFFSET 6
```

The query needs an `ORDER BY`. Without one the database may return rows in a
different order for every request, so rows would repeat or disappear between
pages, and `Paginate` fails with `database.ErrNoOrder`. End the order with a
unique column such as `id`, which breaks ties between rows that share the
other values. The facade checks that an order exists, not that its final column
is actually unique. The builder must not set its own `Limit` or `Offset`, because
`Paginate` sets them; it fails with `database.ErrPaged` instead of overwriting
them.

### Reading the page from a request

In an existing handler, `request.Page` reads the `page` and `per_page` query
parameters from `req`. Pass them to `Paginate`, then return the page with
[`response.JSON`](../http-fundamentals/responses.md):

```go
params, err := request.Page(req)
page, err := db.Paginate[Post](req.Context(), published().Derive(func(statement *query.Builder) {
	statement.OrderByDesc("created_at")
	statement.OrderBy("id")
}), params)
```

After checking the pagination error, send `page` with
`response.JSON(w, http.StatusOK, page)`, where `w` is the response writer and
`http` is `net/http`. Given seven published posts ordered as above,
`/posts?page=2&per_page=2` receives:

```json
{
  "items": [
    {
      "id": 5,
      "title": "Post 5",
      "created_at": "2026-03-01T14:00:00Z"
    },
    {
      "id": 4,
      "title": "Post 4",
      "created_at": "2026-03-01T13:00:00Z"
    }
  ],
  "total": 7,
  "per_page": 2,
  "current_page": 2,
  "last_page": 4
}
```

`items` is always an array, an empty one when nothing matches, so clients do
not need to check for `null`. The response has no envelope, links, or
`Link` header: it is the page value as is. If your API needs a `links` or
`meta` object, wrap the page in your own struct before you send it. See
[Responses](../http-fundamentals/responses.md) for the response helpers.

### Limits and bad input

A client controls `page` and `per_page`, so `request.Page` applies limits.
A missing parameter or integer zero uses its default, which is page 1 with 25
rows per page. Negative values are raised to one, and `per_page` is capped at 100 rows. A
value that is not a whole number is rejected with a `400 Bad Request` problem
document that names the parameter, which the router renders for you:

```json
{
  "type": "tag:arkoren.dev,2026-09:core:http/request:invalid-query-parameter",
  "title": "Invalid query parameter",
  "detail": "The query parameter must be a valid integer.",
  "status": 400,
  "instance": "/posts",
  "parameter": "per_page"
}
```

Pass options to change the limits for one endpoint:

```go
params, err := request.Page(req,
	pagination.WithDefaultPerPage(10),
	pagination.WithMaxPerPage(50),
)
```

A page beyond a ceiling, 10,000 by default, is rejected with a `400` as well,
because the database would have to read and discard every row before it.
`WithMaxPage` changes the request parser's ceiling, but `db.Paginate` normalizes
again with its own fixed default limits: at most page 10,000 and 100 rows per
page. Raising parser limits does not raise these facade limits; lower parser
limits still apply. Deep offsets are where cursor pagination is the better tool.
A page past the last one that is within the
ceiling is not an error: it returns no items with `current_page` set to the
requested page, so the client can see it is past `last_page`.

Custom `PageParams.Normalize` limits also reject an offset that cannot fit an
`int`: the boundary is `(Page-1)*PerPage <= math.MaxInt`, after size normalization.
`Offset` itself is unchecked arithmetic; normalize parameters before calling it
outside the facade. The count and page query are separate statements, not an
automatic shared snapshot, so concurrent changes can make their observations differ.

### Pagination outside a request

A background job has no request, so build the parameters yourself. They are
normalized for you, with the same defaults and limits:

```go
page, err := db.Paginate[Post](ctx,
	published().Derive(func(statement *query.Builder) {
		statement.OrderBy("id")
	}),
	pagination.PageParams{Page: 2, PerPage: 50})
```

## Cursor pagination

Offset pagination has two weaknesses on large or busy tables. The database
must walk past every skipped row, so page 5,000 costs far more than page 1.
And when rows are inserted or deleted while a client pages, rows shift between
pages, so the client sees one twice or misses one.

`CursorPaginate` avoids skipping earlier rows. It remembers the position of the last row, and
the next query starts after it with an ordinary `WHERE` condition on the
ordered columns, which a matching index can seek to efficiently. As with page
numbers, read the existing handler's `req` with `request.Cursor`, then paginate:

```go
params, err := request.Cursor(req)
feed, err := db.CursorPaginate[Post](req.Context(), published().Derive(func(statement *query.Builder) {
	statement.OrderByDesc("created_at")
	statement.OrderByDesc("id")
}), params)
```

The first request, `/feed?per_page=2`, has no cursor. The response carries
the rows and a `next_cursor` for the page after them:

```json
{
  "items": [
    {
      "id": 7,
      "title": "Post 7",
      "created_at": "2026-03-01T16:00:00Z"
    },
    {
      "id": 6,
      "title": "Post 6",
      "created_at": "2026-03-01T15:00:00Z"
    }
  ],
  "per_page": 2,
  "next_cursor": "eyJ2IjpbIjIwMjYtMDMtMDFUMTU6MDA6MDBaIiw2XSwiayI6IjA5OGFmODQyNTRhMTliZGEifQ"
}
```

The client passes the cursor back unchanged, as in `/feed?per_page=2&cursor=eyJ2...`.
A page in the middle has both cursors, `next_cursor` to continue and
`prev_cursor` to go back:

```json
{
  "items": [
    {
      "id": 5,
      "title": "Post 5",
      "created_at": "2026-03-01T14:00:00Z"
    },
    {
      "id": 4,
      "title": "Post 4",
      "created_at": "2026-03-01T13:00:00Z"
    }
  ],
  "per_page": 2,
  "next_cursor": "eyJ2IjpbIjIwMjYtMDMtMDFUMTM6MDA6MDBaIiw0XSwiayI6IjA5OGFmODQyNTRhMTliZGEifQ",
  "prev_cursor": "eyJ2IjpbIjIwMjYtMDMtMDFUMTQ6MDA6MDBaIiw1XSwiayI6IjA5OGFmODQyNTRhMTliZGEiLCJiIjp0cnVlfQ"
}
```

A navigation cursor that does not apply is left out; an empty result has neither
cursor, even when requested with a token. The first page has no
`prev_cursor` and the last has no `next_cursor`, so a client knows it has
reached the end when the key is missing. To find out whether another page
exists, `CursorPaginate` asks the database for one row more than a page and
checks whether it arrived, which costs no extra query.

For PostgreSQL, the query behind the second request looks like this. The
cursor stands in for the last row seen, here created at 15:00 with ID 6:

```sql
SELECT "id", "title", "created_at" FROM "posts" WHERE "published" = $1 AND ("created_at" < $2 OR ("created_at" = $3 AND "id" < $4)) ORDER BY "created_at" DESC, "id" DESC LIMIT 3
```

### What a cursor query needs

A cursor is made of the ordered column values of a row, so the query must
meet three requirements:

- **An order that ends in a unique column.** Two rows with the same
  `created_at` need `id` to decide which comes first. Without a unique last
  column, rows that share the other values can repeat or vanish between pages.
  The facade does not verify that uniqueness in the database.
- **Result fields for every ordered column.** The cursor reads the values from
  the row struct, matched by the same column rules as scanning and
  [`query.Values`](query-builder.md#writing-structs), so `Post` needs
  `created_at` and `id` fields. A qualified column such as `posts.id` matches `id`. Otherwise
  the call fails with `database.ErrCursorColumn`.
- **No `NULL` in the ordered dataset.** Every fetched row is checked, including
  lookahead rows; an observed NULL key fails with `database.ErrCursorValue`.
  SQL seek predicates can exclude a NULL partition before rows are returned,
  so the facade cannot detect all unseen NULLs. Require non-nullable ordering
  columns, or explicitly filter NULLs from every ordered column.

SQL nullable wrappers are checked through `driver.Valuer`. Non-NULL pointer
Valuers keep their method sets for SQL binding; the cursor's JSON representation
continues to use the dereferenced field value. A Valuer error retains its cause
under `database.ErrCursorValue`; an encoded client key that fails validation
instead matches `pagination.ErrCursorDecode`.

The order is part of the cursor. A cursor from one order cannot be used with
another. A different order, malformed token, or invalid key value
fails with `pagination.ErrCursorDecode`, a `400` problem document that the
router renders:

```json
{
  "type": "tag:arkoren.dev,2026-09:core:pagination:cursor-decode",
  "title": "Invalid cursor",
  "detail": "The cursor could not be decoded.",
  "status": 400,
  "instance": "/feed"
}
```

The token does not bind the table, filters, user, or tenant, and it has no
built-in expiry. Apply authorization filters on every request, and have the
client discard its cursor when filters change. Unsigned tokens can be edited
to choose another valid position; signing below prevents that, but does not
add query scoping or hide the encoded values.

Unsigned and signed tokens are limited to **4096 bytes for the complete encoded
token**, including a signed token's separator and signature. Encoding a larger
token fails with `pagination.ErrCursorEncode`; oversized input fails decoding
with `pagination.ErrCursorDecode`. Normalizing request parameters alone does not
decode or authenticate a cursor.

### Signing cursors

By default a cursor is base64-encoded JSON, so a client can read the key
values in it and build one by hand. Forging a cursor only moves the position
in a query you already wrote, and your own filters still apply, but you may
prefer tokens a client cannot invent. Give the database a codec, which signs
every cursor with an HMAC and rejects any it did not sign. Generate independent
material with `arkoren key generate shared --out cursor-key.key`, then import
`arkoren.dev/foundation/key` in startup code:

```go
secret, err := key.LoadShared("cursor-key.key")
if err != nil {
	return err
}
codec, err := pagination.NewCursorCodec(secret)
if err != nil {
	return err
}

db := database.New(driver, func(connection *database.Builder) {
	connection.CursorCodec(codec)
})
```

The typed `key.Shared` must be valid and hold exactly 32 bytes; raw byte slices are not accepted.
`NewCursorCodec` accepts older `key.Shared` values as
further arguments, which verify cursors but never sign them, so you can rotate
a secret without invalidating the cursors clients hold. Turning signing on
for the first time does invalidate them, since the existing tokens are
unsigned. [Secrets](../secrets/overview.md) covers where to keep the key.

A nil codec on the database leaves cursors unsigned. A non-nil zero
`pagination.CursorCodec` is not a usable replacement: its `Encode` fails with
both `ErrCursorEncode` and `ErrInvalidSecret`, and its `Decode` rejects every
token with `ErrCursorDecode`. Always construct it with `NewCursorCodec`.

## Paginating raw SQL

Pagination needs a builder, because it reads the order from it and adds the
limits itself. A raw query becomes a builder with `query.FromRaw`, which wraps
the SQL as a subquery:

```go
type AuthorPosts struct {
	AuthorID int64 `db:"author_id" json:"author_id"`
	Posts    int64 `db:"posts"     json:"posts"`
}

report, err := db.Paginate[AuthorPosts](ctx,
	query.FromRaw(query.Raw(`SELECT author_id, COUNT(*) AS posts
FROM posts WHERE created_at >= ? GROUP BY author_id`, since), func(statement *query.Builder) {
		statement.As("r")
		statement.OrderByDesc("posts")
		statement.OrderBy("author_id")
	}),
	params)
```

The database may have to compute the whole inner query before it applies the
limits, so a raw query with `GROUP BY` or `DISTINCT` can make deep pages and
the count slow. [Raw SQL and locks](raw-sql-and-locks.md#raw-sources-and-fragments)
shows more about raw sources.

The source can also be `query.RawNamed(sql, values)`. `FromRaw` preserves its
named-binding mode and captured map, and source arguments bind before outer
conditions. Order by columns exposed by the subquery; raw SQL does not gain
automatic uniqueness checks or a snapshot across page requests.

## Transforming the current window

To prepare fetched rows for display, call collection operations directly on
`page.Items`. Both offset and cursor results expose an eager
`collection.Slice[Post]`, not a stream. After checking the fetch error, map the
current window to titles:

```go
titles := page.Items.Map(func(_ int, post Post) string {
	return post.Title
})
```

`titles` contains only this window's titles, in the original order. `Filter` and
`GroupBy` are also available directly; see [Collections](../collections/overview.md).
These operations do not query the database or visit another page. They do not
recompute `Total`, `LastPage`, `PerPage`, or navigation tokens. If you assign a
filtered collection back to `page.Items`, cursor tokens still identify the
original window's boundaries, even when you removed its first or last row.
Apply filters to the SQL query when they must affect the total or navigation.

Constructed results own their outer item storage, but referenced objects inside
items remain shared. `page.Items.Items()` returns a plain `[]Post` view of that
same page storage when another API requires it. The JSON representation remains
an `items` array with the existing page metadata.

## Constructing result values yourself

If you already have an in-memory item window, `pagination.Paginate` builds the
offset result, and `pagination.CursorPaginate` or `CursorPaginateFunc` builds
the cursor result. These helpers copy the outer item slice; referenced objects
remain shared rather than recursively copied. Nil items become an empty array.
They do not fetch rows or trim an oversized window. Cursor helpers retain the
supplied per-page metadata without normalization.

For a nonempty cursor window, requested encoders run in order: the last item
for `next_cursor`, then the first item for `prev_cursor`. Empty windows invoke
neither encoder. Supply a non-nil callback for each requested token; callback
panics propagate. An encoder failure wraps `pagination.ErrCursorEncode` and
preserves its cause. The returned partial result can still contain copied items
and a previously encoded next token: check the error before sending it as success.
These low-level encoders define your own token protocol; they do not automatically
produce the order fingerprint expected by `database.CursorPaginate`.

## Processing every row

Sometimes the consumer is code, not a client: an export, a backfill, a
nightly job. Neither page style fits, and loading the whole table into a slice
would exhaust memory. Two methods read large results in a stream.

### Reading in batches

`Each` reads the rows in batches. It runs one keyset query per batch of the
size you give, so memory stays bounded. Like cursor
pagination, it needs an order that ends in a unique column and result fields
for the ordered columns. Leave `Limit` and `Offset` unset: `Each` owns the batch
window and yields `database.ErrPaged` if either is preset:

```go
for post, err := range db.Each[Post](ctx, published().Derive(func(statement *query.Builder) {
	statement.OrderBy("id")
}), 500).Iter() {
	err = writer.Write(post)
}
```

The batches are separate queries, not one snapshot, so rows written while the
loop runs may or may not appear. If you need a consistent view, use a
transaction with a snapshot-preserving isolation level; a default PostgreSQL
read-committed transaction still takes a new snapshot per statement. See
[Transaction isolation](transactions.md#isolation). A batch
size below one yields `database.ErrInvalidBatchSize`.

`Each` checks cancellation between buffered rows as well as between queries,
and validates every emitted key, including a short final batch. It has the same
non-NULL dataset and caller-owned uniqueness requirements as cursor pagination.
If a later row, batch, or cancellation fails, earlier rows may already have been
processed; iteration does not undo their side effects. Stopping the consumer
prevents further batch fetches.

### Reading a single query as a stream

`Cursor` instead runs a single query and reads its rows as the database
sends them. It needs no order and no unique column, and suits one pass over a
result:

```go
err := db.Cursor[Post](ctx, published()).Each(func(_ int, post Post) error {
	return writer.Write(post)
})
```

The iteration holds a database connection until it ends, so process each row
quickly, and stop early by returning an error. The facade closes the underlying
rows when iteration ends, including an early stop. Both methods return a lazy
collection, which [Collections](../collections/overview.md) describes, so
you can also filter and transform the rows as they stream by.

On exhaustion, scanning failure, or cancellation, `Cursor` closes its rows before
yielding a final error and joins close failures with the original cause. After
the consumer stops, the iterator cannot yield another error: a close failure is
recorded only in telemetry. When your caller must receive an early-stop close
error, use `db.Driver().Query` and explicitly handle `Rows.Close` instead. That
native/root path bypasses facade session and transaction selection and telemetry;
do not use it for SQL that must join a callback scope.

For every function and option, see
[the `pagination` API reference](https://pkg.go.dev/arkoren.dev/core/pagination) and
[the `database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
