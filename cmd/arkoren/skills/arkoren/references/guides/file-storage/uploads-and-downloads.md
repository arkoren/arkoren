---
title: Uploads and Downloads
nav: Uploads & Downloads
description: Store bounded uploads under safe keys and serve authorized objects with attachment, inline, and range behavior.
order: 30
summary:
  - Bound multipart requests and validate individual files before storage.
  - Generate upload keys or deliberately replace an authorized application key.
  - Serve authorized attachments and restricted inline content with backend-specific range behavior.
  - Preserve transfer and cleanup errors without replacing a committed response.
---

# Uploads and Downloads

An upload brings untrusted bytes into your store; a download exposes those bytes
to another client. Storage's HTTP helpers generate upload keys and control how
objects are presented, but your handler still decides who may write or read
each object and which content your application accepts.

Examples assume a configured `files *storage.Storage`, `r *http.Request`, and
`w http.ResponseWriter` in an error-returning router handler. Import `request`
from `arkoren.dev/core/http/request` and `validate` from
`arkoren.dev/foundation/validate`. Backend construction belongs on
[Drivers](drivers.md).

## Accepting Uploads

For an avatar upload, authorize the current user's profile before storing a
file. Parse the whole multipart request with a limit that includes fields and
multipart overhead, not only file bytes:

```go
form, err := request.Multipart(r, 5<<20)
if err != nil {
	return err
}
```

This accepts at most 5 MiB in the whole body; exceeding the limit returns a
413 problem. [Parse Uploads](../http-fundamentals/requests.md#parse-uploads)
owns general multipart parsing, repeated-call behavior, and temporary-file
cleanup. Through Arkoren's router, cleanup runs after the response. Outside
that adapter, the caller must arrange `form.RemoveAll`; consume and close file
handles before cleanup, and do not queue file headers for later use.

Set a route ceiling with [Body Limits](../http-fundamentals/body-limit.md) as
well. A negative `request.Multipart` limit uses the contextual middleware limit,
or the default when none is set; it does not mean unlimited. The parser keeps
up to 8 MiB of file content in memory before spilling larger parts to disk.

The whole-body limit does not express an avatar policy. Define a separate
policy requiring a PNG of at most 2 MiB:

```go
avatar := validate.File(func(upload *validate.FileBuilder) {
	upload.Required().Max(2 << 20).Types("image/png")
})
```

Validate the actual `*multipart.FileHeader` before storage. A missing field
returns nil from `form.File`; `Required` rejects it before the storage helper
can dereference it:

```go
header := form.File("avatar")
if err := avatar.Validate(r.Context(), header); err != nil {
	return request.ValidationProblem(err)
}
info, err := files.PutFile(r.Context(), "avatars", header)
```

On success, persist `info.Key` with the owner and any display metadata. The
validation adapter maps rule violations to an HTTP problem while preserving
operational errors; see [Validation Errors](../validation/errors.md).
[File Upload Rules](../validation/available-methods.md#file-uploads) explain
per-file size, claimed extension, and detected-type checks. Detection examines
leading bytes, not the client's `Content-Type` header; it is not malware scanning
or proof that an image decoder will accept the whole file.

## Choosing Keys

`PutFile` combines your directory with 26 random lowercase base32 characters
and an extension inferred from the first 512 bytes. The client filename never
becomes the key. Collisions are extremely unlikely, not impossible: the
underlying `Put` still replaces a colliding key.

The directory may be empty; otherwise it must satisfy the
[object key rules](overview.md#object-keys) without a trailing slash. An invalid
directory fails before reading the upload. Unknown or octet-stream content gets
no extension. HTML disguised as `photo.png` can get `.html`, so sniffing alone
does not make unrestricted static serving safe.

If your application needs a stable replacement key, use `PutFileAs`. Here
`userID` is the authorized, application-issued portable profile identifier, and
the preceding PNG policy has succeeded:

```go
info, err := files.PutFileAs(r.Context(), "avatars/"+userID+".png", header)
```

This replaces exactly that key. Choose the extension from your accepted format,
never `header.Filename`. Both helpers open and close their read-only multipart
source themselves, passing its seekable file to the driver. Source close is
best-effort and cannot undo or change a successful write's result; do not repeat
the write to compensate for source cleanup.

Returned `ObjectInfo` has a key, size, and modification time, not a stored MIME
type or original filename. Keep `header.Filename` separately if your application
needs a display name. Serve untrusted objects through `Download`, `Inline`, or
an isolated origin rather than an unrestricted static route on your app origin.

## Raw Uploads

For a client that sends bytes directly, `Put` accepts `r.Body`. Before this
fragment, enforce a [body limit](../http-fundamentals/body-limit.md), authorize
the target, and validate content according to your application's format policy.
Here `key` is that record's application-owned key, not an unchecked path value:

```go
info, err := files.Put(r.Context(), key, r.Body)
```

`Put` does not detect content or close the reader; the HTTP server closes the
request body after the handler returns. Reading beyond an enforced body limit
fails the reader, so no partial logical object replaces the prior one. Other
failures can occur after replacement, as explained in
[Writing Objects](overview.md#writing-objects).

> [!WARNING]
> S3 buffers a non-seekable body fully in memory. Keep this path bounded and
> small, use a seekable multipart source, or choose a
> [direct signed upload](signed-urls.md#direct-uploads) for large objects.

Cancellation is cooperative after work begins; it cannot force an arbitrary
blocked reader to return. Keep the request context attached to the operation.

## Serving Files

Load a document by your application's identifier and verify that the current
user may read it. Do not expose an arbitrary requested storage key. With an
authorized `document` of this shape:

```go
type StoredDocument struct {
	Key          string
	OriginalName string
	OwnerID      string
}
```

Return the download helper's result from your handler:

```go
return files.Download(w, r, document.Key, document.OriginalName)
```

The helper opens and always closes the object body. It sets attachment
disposition so the browser offers a download. An empty display name uses the
key's last segment; supplied names get an ASCII fallback and UTF-8 presentation
where needed. The display name changes presentation, not the stored key.

## Headers and Ranges

The response MIME type comes from the display filename's extension, falling back
to the first 512 bytes when the extension gives no type. A recognized extension
therefore takes precedence over sniffing. Choose trustworthy display metadata
and validate accepted formats; the helper adds `X-Content-Type-Options: nosniff`
but does not validate an entire payload.

Memory and filesystem bodies can seek, so the helper uses `http.ServeContent`:
range requests can return 206, `HEAD` omits bytes, and `Last-Modified` with
`If-Modified-Since` can produce 304. Native handling also answers invalid ranges
and failed size probes with its own HTTP error responses.

S3 bodies cannot seek. The helper streams the whole object with its known
`Content-Length`; `HEAD` receives headers only. It does not process ranges or
conditional requests for that body. Use a [signed GET](signed-urls.md#signed-downloads)
when clients need direct S3 range downloads.

## Inline Content

For an authorized avatar, `Inline` requests browser display instead of saving:

```go
return files.Inline(w, r, document.Key)
```

Every inline object response includes `Content-Security-Policy: sandbox`.
Only PNG, JPEG, GIF, WebP, AVIF, BMP, ICO, PDF, audio, video, and plain text are
allowed inline. Other types, including HTML, SVG, XML, JavaScript, CSS,
WebAssembly, JSON, and unknown content, are forced to attachment disposition.
The offered name comes from the key's final segment.

These restrictions reduce active-content risk on your origin; they are not
authorization or content inspection. Use an isolated origin without application
cookies when your threat model requires stronger separation.

## Transfer Errors

A missing object returns `storage.ErrObjectNotFound`, a 404 problem that also
wraps `storage.ErrNotFound`. Open failures and initial sniff/rewind failures
return before response commitment, so the router can still render an error.

Source, seek, write, short-write, and body-close failures remain discoverable
through `errors.Is`; cleanup errors join transfer errors. Premature EOF during a
seekable transfer matches `io.ErrUnexpectedEOF`. Native `ServeContent` error
responses are retained, not replaced by a problem body.

An error after commitment does not always mean truncation: a copy failure can
truncate the body, while a close failure can arrive after every byte was sent.
Return errors for logging, but never append another response or automatically
retry a committed transfer. See
[Committed Failures](../http-fundamentals/streaming-responses.md#committed-failures)
for that HTTP boundary.

For all helper options, see the
[`storage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage).
