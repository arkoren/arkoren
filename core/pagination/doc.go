// Package pagination provides the offset and cursor pagination types and opaque
// cursor codecs shared by the database and request packages. It holds only data
// types and small helpers, and depends on neither net/http nor a database.
//
// request.Page and request.Cursor in arkoren.dev/core/http/request read query
// parameters into [PageParams] and [CursorParams]. Both types can also be
// built by hand and checked with their Normalize methods, which apply
// defaults (zero uses the default), clamp values, and validate the
// configuration.
//
// [CursorPaginate] builds a [Cursor] with one encoder for both cursors;
// [CursorPaginateFunc] takes separate next and previous encoders, for cursors
// that carry a direction. With arkoren.dev/core/service/database, the Paginate and
// CursorPaginate methods of Database run the queries and call these.
//
// [CursorEncode] and [CursorDecode] produce plain base64 JSON that a client can
// forge. A CursorCodec signs cursors with HMAC-SHA256 instead, using key.Shared
// values of exactly MinSecretLength bytes and optional previous keys for rotation.
// Database.CursorPaginate selects one through database.Builder.CursorCodec.
// Both encoders reject tokens longer than the decoders' 4096-byte limit with
// [ErrCursorEncode], including the signature overhead for signed cursors.
// A successful encode therefore does not issue an immediately oversized token.
// A zero CursorCodec is invalid and reports an encoding error instead of
// panicking; construct it with NewCursorCodec before sharing it.
// Page and Cursor construction copy their outer item slices, not referenced
// item objects. Items is an eager collection.Slice with local Map, Filter, and
// GroupBy operations. These operations inspect only the current window; they
// neither fetch more items nor recompute server totals or cursor boundaries.
// Cursor helpers retain PerPage as metadata without normalizing
// or trimming items. Their encoders run next then previous, only when requested
// on nonempty items, and can return a partial result alongside an error.
//
// Database keyset pagination requires non-NULL ordered values throughout the
// selected dataset. It rejects observed NULL keys, including nullable SQL
// representations, but cannot detect NULL rows that a SQL seek excludes.
// Use non-nullable ordering keys or explicitly filter out NULL rows; this
// package does not add NULL ordering to SQL queries.
//
// Zero configuration fields use the defaults. Page and per-page defaults may
// use any integer and are normalized to positive returned values. A negative
// maximum makes Normalize return [ErrInvalidConfig].
//
// Example:
//
//	codec, err := pagination.NewCursorCodec(secret)
//	if err != nil {
//		return err
//	}
//	db := database.New(driver, func(databaseClient *database.Builder) {
//		databaseClient.CursorCodec(codec)
//	})
//	_ = db.Driver()
package pagination
