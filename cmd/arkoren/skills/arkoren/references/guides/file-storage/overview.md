---
title: File Storage Overview
nav: Overview
description: Store, read, list, and delete files behind one API on memory, the local filesystem, and Amazon S3, with portable keys and testable errors.
order: 10
---

# File Storage Overview

## Introduction

Most services keep files somewhere: avatars that users upload, invoices a job
generates, exports a customer downloads later. Where those files live changes
between environments. A test wants them in memory, a single server writes them
to a disk, and a production deployment with several replicas needs a store
that every replica can reach. If your handlers talk to the disk or to the S3
client directly, moving between these means rewriting them.

The `storage` package puts one API in front of all of them. Your code depends
on `*storage.Storage`, the **facade**. A **driver** supplies the actual store:
[memory, the local filesystem, or Amazon S3 and S3-compatible services](drivers.md).
The facade checks every request before it reaches the driver, so a key that is
invalid for one driver is invalid for all of them, and a missing file is the
same error everywhere. Switching drivers is a change to the line that builds
the facade.

Storage deals in **objects**: a sequence of bytes under a **key** such as
`avatars/42.png`. There are no directories to create and no permissions to
set. An object is written whole, read as a stream, and replaced or deleted by
its key. This page covers that core. The other pages of this topic cover
[uploads and downloads over HTTP](uploads-and-downloads.md),
[signed URLs](signed-urls.md) for direct access, and
[events and telemetry](events-and-telemetry.md).

Focused examples show the ordinary success path and leave error results visible
without routine checks. Handle those errors in your application; failure
examples and tests retain the relevant checks.

## Wiring a driver

Run `go get arkoren.dev/core` from your application's Go module. To try storage
without a server or a directory, wrap the included memory driver. Subsequent
examples use your operation context `ctx`:

```go
import (
	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/memorystorage"
)

files := storage.New(memorystorage.New())
```

This process-local store is ready to use without lifecycle hooks; process exit loses its
objects. For persistent files, choose [filesystem or S3 storage](drivers.md).
The filesystem driver requires `Start` before use and `Stop` after consumers
finish; follow the [lifecycle and ownership guide](../application/lifecycle-and-ownership.md).
S3 has no lifecycle hooks.

To report events and telemetry, give the facade the application's bus and
telemetry when you create it, as described in
[Events and telemetry](events-and-telemetry.md). This configuration is optional.

### Several stores

There is no registry of named disks. If your application keeps public assets
in one bucket and private documents in another, create one facade for each
and pass the one a component needs:

```go
assets := storage.New(assetsDriver, func(store *storage.Builder) {
	store.Name("assets")
})
documents := storage.New(documentsDriver, func(store *storage.Builder) {
	store.Name("documents")
})
```

The name does not select anything. It labels the store in
[events and telemetry](events-and-telemetry.md), so you can tell the two apart
in a trace. Without a name, a store reports itself as `default`.

## Writing files

To save a generated report for later download, pass its reader to `Put`.
`Put` stores the contents of an `io.Reader` under a key and returns the
object's metadata. It reads the reader once, to the end, and replaces any
existing object with that key:

> [!WARNING]
> On S3, a reader that cannot seek is buffered fully in memory before upload.
> Bound its size or use a seekable file or [direct signed upload](signed-urls.md)
> for large objects, so an upload cannot exhaust application memory.

Here `reportFile` is your open report reader. `Put` does not close it; you retain
responsibility for closing it if it is a file:

```go
info, err := files.Put(ctx, "reports/2026-q1.csv", reportFile)
// info.Key, info.Size, info.ModifiedAt
```

The object becomes current only when the whole body has been written. If the
reader fails halfway, the previous object, if there was one, is still what
readers get, and no partial logical object is published. Native staging cleanup
is a separate backend concern. A later metadata failure is different: on S3 or
the filesystem, the upload can succeed but the metadata lookup can fail,
so a returned error does not always mean the object is unchanged. Inspect the
key with `Stat` before deciding how to recover.

For data you already hold in memory, `PutBytes` takes a byte slice:

```go
_, err := files.PutBytes(ctx, "avatars/42.png", png)
```

> [!NOTE]
> The S3 driver needs the length of the body before it uploads. It streams from
> a reader that can seek, such as an uploaded file or an `*os.File`, and
> buffers any other reader fully in memory. Pass a seekable reader when you
> store large files.

To store a file from a multipart upload, use `PutFile`, which chooses a safe
key for you; see [Uploads and downloads](uploads-and-downloads.md).

## Reading files

`Open` returns the object's metadata and its contents as an `io.ReadCloser`.
You must close it, so open and defer the close together:

```go
object, err := files.Open(ctx, "avatars/42.png")
if err != nil {
	return err
}
defer object.Contents.Close()

_, err = io.Copy(w, object.Contents)
```

You can consume the body incrementally. Filesystem and S3 reads do not load
the whole object into application memory; the memory driver already holds it
in memory and returns a copied snapshot. The memory and filesystem drivers return a body that also
implements `io.Seeker`; the S3 driver does not. If you need to seek, check for
it with a type assertion instead of assuming it.

When you only need to know about an object, `Stat` returns the same
`ObjectInfo` without opening the contents. That is also how you test whether
an object exists, since there is no separate method for it:

```go
_, err := files.Stat(ctx, "avatars/42.png")
switch {
case errors.Is(err, storage.ErrNotFound):
	// there is no such object
case err != nil:
	return err
}
```

## Listing files

To inspect stored avatars for cleanup, traverse their shared key prefix with
`Walk`. It returns a fallible lazy [collection](../collections/overview.md):
no listing or validation happens until you consume it. With `files` and `ctx`
from the setup above:

```go
for info, err := range files.Walk(ctx, storage.ListOptions{Prefix: "avatars/", Limit: 100}) {
	fmt.Println(info.Key, info.Size)
}
```

`Walk` preserves the driver's lexical source order and follows opaque
`NextCursor` values, even after empty or short pages. A full final page with an
empty cursor ends traversal without another request. `Limit` caps each page,
not the total number of objects; zero or less keeps the driver's default.
To consume at most ten objects, use `Take`:

```go
objects, err := files.Walk(ctx, storage.ListOptions{Prefix: "avatars/", Limit: 100}).Take(10).Items()
if err != nil {
	return err // objects contains only the successful prefix, not a complete listing.
}
```

Breaking iteration or reaching `Take`'s item limit stops immediately, without
requesting another page. This bounds future facade page requests, not the cost
of preparing the current page: memory and filesystem listings can scan and sort
matching keys, and S3 can read ahead through physical pages to validate logical
continuation. Neither `Walk` nor `Take` makes that work object-by-object.

Cancellation is checked before requests and between buffered objects. A page
error or cancellation is yielded once and ends traversal without retry; `Items`
returns the objects collected before that error. Reusing the same walk starts
fresh I/O from its original cursor with its captured context. It does not refresh
an expired context or cache results. Create a new walk for a new operation
context. Concurrent changes provide no snapshot across pages or traversals.

If you need to return a page and its cursor to a caller, keep using `List`.
It returns one page of objects in lexicographic key order:

```go
page, err := files.List(ctx, storage.ListOptions{Prefix: "avatars/", Limit: 100})
```

After checking the error, return `page.Objects` and `page.NextCursor` to your
caller. To request another page, pass that cursor as `ListOptions.Cursor` with
the same prefix. Continue until the cursor is empty, even if an intermediate
page is empty or shorter than its limit; use `Walk` above for automatic traversal.

Treat cursors as opaque: one is valid only for the same driver and prefix, and the pages give
no snapshot guarantee, so an object written while you page may or may not
appear. Listing is meant for housekeeping, such as cleaning up or exporting a
prefix. Native S3 or filesystem entries outside the portable key syntax are
omitted; S3 can read ahead through physical pages to validate logical continuation.
That adds requests without changing the native cursor representation. Keep the
keys your application needs to find in your database, which
can answer by owner or date far faster than a listing.

## Deleting files

`Delete` removes an object and succeeds when the object does not exist, so you
can call it again after a failure without checking first:

```go
err := files.Delete(ctx, "avatars/42.png")
```

## Keys

A key is a relative path of UTF-8 segments separated by `/`, at most 1024 bytes
in total. Every driver
accepts the same keys, because the facade rejects the others before the driver
sees them. A key must not be empty, start with `/`, or contain an empty,
`.`, or `..` segment, a backslash, a NUL or other control character, or a
colon. Windows filename characters `<`, `>`, `"`, `|`, `?`, and `*` are also
forbidden on every backend. A segment may not end with a dot or a space.
Reserved Windows device names are rejected case-insensitively in every segment:
`CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`, and `LPT1`–`LPT9`, including extension
forms such as `NUL.txt` and superscript port digits `¹`, `²`, and `³`. The exact
console names `CONIN$` and `CONOUT$` are also rejected. Names such as `COM0` and
`COM10` are not blanket-rejected. An invalid key returns an
error that wraps `storage.ErrInvalidKey`. Prefixes follow the same rules and
may be empty or end in a single `/`.

These rules are strict so that a key means the same thing on a Linux disk, in
S3, and on a developer's laptop. Keep three habits in mind:

- Never build a key from a client's filename. Generate it, or derive it from
  your own data such as `avatars/<user id>.png`. A filename from a user can
  contain anything, and it decides the extension a browser will trust.
- Do not store both `a` and `a/b`. A filesystem cannot hold a file and a
  directory with the same name, so this works on S3 and fails elsewhere.
- Do not rely on a key that is only the leading path of others. If only
  `reports/2026/q1.csv` exists, `reports/2026` is not an object: `Open` and
  `Stat` report it missing.

To check a key you got from outside, call `storage.ValidateKey` before you use
it.

### Migrating existing names

Validation does not escape, rename, or repair existing objects. Before upgrading
an application with affected names, inventory them through the native backend,
choose collision-free valid replacements, and migrate their bytes and application
references deliberately. Check configured S3 prefixes as well as logical keys:
`ValidatePrefix` applies the same restrictions, so an old prefix can now fail
construction. Preserve authorization and ownership while migrating; do not
silently normalize a name into another user's key or discard inaccessible data.

## Errors

Errors from the facade wrap sentinel values, so test for them with
`errors.Is`:

| Error | Meaning |
| --- | --- |
| `storage.ErrNotFound` | `Open` or `Stat` found no object for the key. |
| `storage.ErrInvalidKey` | The key or prefix breaks the rules above. |
| `storage.ErrUnsupported` | The driver cannot do this, such as [signing a URL](signed-urls.md) on the filesystem driver. |
| `storage.ErrInvalidTTL` | A signing TTL is not positive or is longer than the driver allows. |
| `storage.ErrInvalidSignOptions` | A signing option does not apply to the method. |

A missing object is the one error most handlers want to turn into a response.
`Download` and `Inline` do that for you with `storage.ErrObjectNotFound`, a
404 problem response, as described in
[Uploads and downloads](uploads-and-downloads.md). After ruling out the input
and unsupported-operation errors above, other errors can report backend
failures, such as a lost network connection or a full disk. The driver's own
error is wrapped inside them. The identity of
`ErrNotFound` comes from the driver, so an S3 permission error is reported as
itself and not as a missing object.

With valid input, an already canceled context returns its error before the
driver changes backend state. Invalid keys and signing options can be rejected
by the facade before the driver's context check.
Raw drivers check an already-canceled context before their input validation or
I/O. For signing, key validation precedes TTL, then supported method, then binding
validation, and finally backend capability rejection. Thus an unsupported method
keeps its own error before invalid binding fields; unsupported signing does not
hide an invalid TTL. Cancellation after I/O begins remains cooperative, not a
guarantee of interrupting a blocked reader or native operation.

## Testing

Import `arkoren.dev/core/service/storage/memorystorage` for this test fragment.

Use the memory driver in tests. It needs no setup, is safe for concurrent use,
and behaves like the other drivers, down to the errors. In a test with
`t *testing.T`, call your application's `saveAvatar` with the image bytes `png`,
then check that its expected key exists:

```go
files := storage.New(memorystorage.New())

if err := saveAvatar(t.Context(), files, "42", png); err != nil {
	t.Fatal(err)
}

if _, err := files.Stat(t.Context(), "avatars/42.png"); err != nil {
	t.Fatal(err)
}
```

If you write a driver of your own, `storagetest.Run` checks it against the
whole contract; see [Conformance suites](../testing/conformance-suites.md).
Code that signs URLs needs a driver that supports it: the memory driver does
not, so test that path against the S3 driver or inject an error. See
[Testing](../testing/overview.md) for the general approach.

For every function and option, see [the `storage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage).
