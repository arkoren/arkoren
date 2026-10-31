# Arkoren Pagination

Large result sets need bounded pages rather than a single response containing
every row. `arkoren.dev/core/pagination` provides the offset and cursor pagination result types,
parameters, and opaque cursor codecs shared by `arkoren.dev/core/service/database` and
`arkoren.dev/core/http/request`.

Install with `go get arkoren.dev/core`, then import `arkoren.dev/core/pagination`.

Read request parameters with `request.Page` and `request.Cursor` from
`arkoren.dev/core/http/request`; this package does not import `net/http`. They return
`PageParams` and `CursorParams`, which can also be built without HTTP and
passed to their `Normalize` methods. `PageParams.Offset` returns the SQL
offset. Zero configuration fields and zero parameter values use the package
defaults: page 1, 25 rows, at most 100 rows and page 10,000. Negative page and
per-page values normalize to one. A negative `MaxPerPage` or `MaxPage` returns
`ErrInvalidConfig`. A page whose offset would overflow returns
`ErrPageOverflow` (400, a `problem.Details`). `request.Page` and `request.Cursor` return
`request.ErrInvalidQueryParameter` (a `problem.Details` with HTTP 400) for
non-integer or explicitly empty values. Parameter parsing does not fetch rows;
the `Paginate` helper does not trim supplied items or empty an out-of-range page.
Page and cursor result helpers copy the outer item slice; objects referenced by
items remain shared. Cursor helpers preserve `perPage` as supplied metadata,
without normalization or trimming. On nonempty items they call the requested
next encoder before the requested previous encoder; empty items call neither.
An encoding error stops further encoders, wraps `ErrCursorEncode`, and returns
the copied items plus any cursor already encoded. Do not use that partial result
as a successful page.

`Page[T].Items` and `Cursor[T].Items` are eager `collection.Slice[T]` values.
Use `page.Items.Map`, `Filter`, or `GroupBy` directly to prepare the current
window for display. These operations do not fetch more rows or recompute totals,
page sizes, or cursor tokens. Tokens still identify the original window's
boundaries, even if you filter out its first or last item. Use `Items.Items()`
when a caller specifically needs a plain `[]T` view; that view shares the page's
backing array. JSON still uses the `items` array and existing metadata fields.

`CursorEncode` and `CursorDecode` are unsigned, so a client can forge a cursor. `NewCursorCodec(secret,
previous...)` returns a `*CursorCodec` whose generic `Encode` and `Decode` methods sign cursors with
HMAC-SHA256 (valid `key.Shared` values holding exactly 32 bytes; `previous` secrets only verify, for rotation) and
reject unsigned or altered ones with `ErrCursorDecode`. Pass it to the database with
`database.Builder.CursorCodec` in a construction policy.
Both encoders return `ErrCursorEncode` if the complete token would exceed the
decoders' 4096-byte limit. For signed tokens, that limit includes the signature
and separator, not only the JSON payload.
The zero `CursorCodec` is invalid: its `Encode` returns errors matching
`ErrCursorEncode` and `ErrInvalidSecret`, and its `Decode` rejects every token.
Always construct the codec with `NewCursorCodec` and check its error.

Signing protects integrity, not secrecy or authorization. It does not bind a
cursor to a user, tenant, filter, or expiration time; apply authorization filters
on every request. Enabling signing invalidates unsigned cursors already held by clients.

Database `CursorPaginate` and `Each` require non-NULL ordering keys in every
selected row. Use non-nullable keys or explicitly filter out NULL rows. They
reject observed NULL keys, including pointer and SQL nullable representations;
cursor pagination checks lookahead too, and `Each` checks short final batches.
These checks cannot detect NULL rows that a SQL seek already excludes, so they
do not make nullable ordering complete. `Each` also observes cancellation
between buffered rows.
Non-NULL `driver.Valuer` keys retain their SQL conversion methods, including
pointer-only methods, without changing the existing cursor JSON representation.

See the [database pagination examples](../service/database/README.md#pagination-and-batching)
to apply these parameters to queries.
