---
title: Overview
description: Write and read logical objects, manage stream ownership, and traverse portable keys with explicit error handling.
order: 10
summary:
  - Choose literal portable keys without confusing syntax validation with authorization.
  - Write and read objects with explicit reader and body ownership.
  - Traverse lazy metadata pages and recognize incomplete results on failure.
  - Delete exact keys and classify missing objects, invalid input, and operational failures.
---

# Overview

A generated report needs a stable name so another request can retrieve it later.
The `storage` facade stores an **object**, a sequence of bytes, under a **key**
such as `reports/2026-q1.csv`. It validates portable names and delegates the
bytes to your chosen driver; it does not track users, filenames, or access rights.

Examples assume an existing `files *storage.Storage` and a non-nil operation
context `ctx`. [Drivers](drivers.md) owns construction and backend lifecycle.
Import `storage` from `arkoren.dev/core/service/storage`; the other packages
named below are from the standard library. Focused fragments leave routine
error results visible; handle them before using results in application code.

## Object Keys

Choose keys from application-owned identifiers, not client filenames. A valid
key is a literal UTF-8 relative path of at most 1024 bytes, with `/` between
segments. Validation checks syntax, not whether the caller owns the object.
Authorize access separately before reading, replacing, or deleting a key.

Keys cannot contain empty, `.` or `..` segments, control characters, backslashes,
or `:`, `<`, `>`, `"`, `|`, `?`, `*`. A segment cannot end in a dot or space.
Reserved Windows device names are rejected case-insensitively, including their
extension forms: `CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`, and `LPT1`–`LPT9`.
The port digits `¹`, `²`, `³` and exact names `CONIN$`, `CONOUT$` are also
reserved; `COM0` and `COM10` are not blanket-rejected.

These rules apply on every backend. `storage.ValidateKey` checks a key without
I/O; `storage.ValidatePrefix` also allows an empty prefix or one trailing `/`.
Neither function cleans, escapes, case-folds, or repairs a name. Native filesystem
path-length and case-sensitivity constraints can still differ.

Do not store both `a` and `a/b`: a filesystem cannot represent both. If only
`reports/2026/q1.csv` exists, `reports/2026` is a prefix, not an object;
`Open` and `Stat` report it missing and `Delete` leaves its children alone.

If existing native names violate these rules, inventory them through the backend
and migrate bytes and application references to collision-free valid names.
Preserve ownership checks; silently normalizing a name can address another
user's object. Check configured backend prefixes too.

## Writing Objects

To save a completed report, give `Put` its key and an `io.Reader`. This small
report uses `strings.NewReader`, which also supports seeking:

```go
contents := strings.NewReader("month,total\n2026-01,1250\n")
info, err := files.Put(ctx, "reports/2026-q1.csv", contents)
```

On success, `info` contains `Key`, byte `Size`, and `ModifiedAt`. `Put` consumes
the reader once and replaces any existing object at that key. It does not close
the source: if you supply an open file, you own its cleanup. `PutBytes` accepts
an existing `[]byte` instead; do not mutate the bytes during the call.

> [!WARNING]
> S3 buffers a non-seekable source fully in memory. Bound its size or supply a
> seekable file; use [signed uploads](signed-urls.md#direct-uploads) when large
> transfers should bypass your application.

A source read failure leaves the previous object intact and publishes no new
logical object. This does not mean every returned error proves that nothing
changed: filesystem replacement and S3 upload can succeed before the subsequent
metadata lookup fails. Inspect the key with `Stat` before recovery; do not
blindly repeat a replacement. Backend staging cleanup and crash durability are
separate concerns described on [Drivers](drivers.md).

For a multipart upload, use the [HTTP upload helpers](uploads-and-downloads.md)
rather than deriving a key from the submitted filename.

## Reading Objects

Keep the stored key in your application's record, alongside the display name and
owner. After loading and authorizing that record, open its contents. Here
`report` is a `StoredReport` and `destination` is your existing `io.Writer`:

```go
type StoredReport struct {
	Key          string
	OriginalName string
	OwnerID      string
}
```

Open and close the body in the same operation. Import `io` for copying and
`errors` for joining transfer and cleanup errors:

```go
object, err := files.Open(ctx, report.Key)
if err != nil {
	return err
}
_, err = io.Copy(destination, object.Contents)
err = errors.Join(err, object.Contents.Close())
```

The caller owns every returned body, including cleanup after a failed copy.
Metadata describes the opened object; acquiring it does not mean its bytes have
been consumed. Keep the operation context alive while reading a backend stream.
Seeking is optional: check for `io.Seeker` when you need it, rather than assuming
all drivers provide it.

If you only need metadata or an existence check, use `Stat` without opening a
body:

```go
info, err := files.Stat(ctx, report.Key)
if errors.Is(err, storage.ErrNotFound) {
	// The authorized report record has no stored object.
}
```

Treat other errors as failures, not absence. Storage preserves the driver's
not-found identity; it does not reinterpret a permission failure as a missing
object. For HTTP delivery with automatic body cleanup, use
[Downloads](uploads-and-downloads.md#serving-files).

## Listing Objects

For housekeeping across reports, `Walk` flattens pages into a fallible lazy
[collection](../collections/fallible-collections.md). Constructing it performs
neither validation nor listing. To consume at most ten metadata records:

```go
objects, err := files.Walk(ctx, storage.ListOptions{
	Prefix: "reports/",
	Limit:  100,
}).Take(10).Items()
if err != nil {
	return err // objects is an incomplete successful prefix.
}
```

`Limit` caps each page, not the whole traversal; nonpositive values use the
driver default. `Take` and breaking iteration stop future facade page requests,
not work already performed for the current page. Memory and filesystem drivers
can scan and sort matching keys; S3 can read ahead to find valid continuation.

Traversal preserves lexical source order and follows opaque cursors even after
empty or short pages. Cancellation is checked before requests and between
buffered objects. A page error or cancellation is yielded once, then traversal
ends without retry. `Items` returns the successful prefix alongside the error.
Reusing a walk starts fresh I/O with its original options and captured context;
create a new walk for a new operation context.

If your API returns individual pages, call `List` instead:

```go
page, err := files.List(ctx, storage.ListOptions{
	Prefix: "reports/",
	Limit:  100,
})
```

After checking `err`, use `page.Objects` and return `page.NextCursor` to the
caller. Continue with that cursor and the same driver and prefix until it is
empty, even after a short or empty page. Do not parse or rewrite cursors.
Prefixes are literal strings: `reports` also matches `reports-old/a.csv`;
`reports/` selects the slash-delimited namespace.

Pages and repeated walks provide no snapshot under concurrent changes. Native
entries outside portable key syntax are omitted. Keep owner/date lookup data in
your database rather than using storage listings as an authorization index.

## Deleting Objects

After authorizing removal, delete the record's exact key:

```go
err := files.Delete(ctx, report.Key)
```

Deleting an absent object succeeds, so absence does not require a preliminary
`Stat`. This removes one object, not everything below a matching prefix.

## Handling Errors

Use `errors.Is` with `storage.ErrNotFound` for missing reads and
`storage.ErrInvalidKey` for invalid keys or prefixes. The signing page explains
`ErrUnsupported`, `ErrInvalidTTL`, and `ErrInvalidSignOptions`. Other errors can
report operational failures such as denied access, a full disk, or unavailable
storage; retain their wrapped causes when handling them.

An already-canceled context takes precedence over invalid input and prevents
backend work. Signing then checks key, TTL (including the backend maximum),
method, bindings, and capability in that order. Later cancellation is cooperative:
it cannot force an arbitrary blocked reader or native operation to return.

For every operation and option, see the
[`storage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage).
