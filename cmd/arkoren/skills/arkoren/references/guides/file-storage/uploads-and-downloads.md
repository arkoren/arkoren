---
title: Uploads and Downloads
nav: Uploads & Downloads
description: Accept uploaded files with size limits and safe generated keys, and serve them back as downloads or inline content with range support.
order: 20
---

# Uploads and Downloads

## Introduction

Files cross the HTTP boundary in two directions, and both are places where a
careless handler causes harm. An upload is data from a stranger: it can be
enormous, it can lie about its name and type, and what you store it as decides
what a browser later does with it. A download is the reverse: the response has
to say what the bytes are and whether to show or save them, and a wrong answer
lets an uploaded page run on your own origin.

Use `PutFile` for a multipart upload when storage should generate the key;
use `PutFileAs` when your application chooses the key. For the return trip,
`Download` offers the object as an attachment, while `Inline` displays only
allowed content types and forces the others to download. Both serve the bytes
through your application; use [signed URLs](signed-urls.md) instead when large
S3 transfers should go directly to the backend.

This page shows the relevant parts of handlers. It assumes a `files` value
built as described in [File storage](overview.md).

The examples use `request` from `arkoren.dev/core/http/request`, `router` from
`arkoren.dev/core/http/router`, `bodylimit` from
`arkoren.dev/core/http/middleware/bodylimit`, and `problem` from
`arkoren.dev/foundation/problem`. Handler fragments have an `*http.Request`
named `r` and an `http.ResponseWriter` named `w` unless noted otherwise.

## Accepting uploads

To store an avatar submitted by a browser, parse its `multipart/form-data`
request and require the file before passing it to storage. `request.Multipart`
parses the request and takes the largest body you accept, in bytes. `PutFile` then
stores one of the uploaded files under a generated key and returns where it
went:

```go
form, err := request.Multipart(r, 5<<20) // the whole request, at most 5 MiB
if err != nil {
	return err
}
avatar := form.File("avatar")
if avatar == nil {
	return problem.UnprocessableContent().Derive(func(details *problem.Builder) {
		details.Detail("An avatar file is required.")
	})
}

info, err := files.PutFile(r.Context(), "avatars", avatar)
```

Parsing failures return before accessing the form or storing a file.
The handler returns `422` if the form has no `avatar`, rather than passing a
nil file header to storage.
On success, `info.Key` identifies the stored object; return or persist it for
later downloads.

`PutFile` and `PutFileAs` open and close the uploaded file themselves. They
return `ObjectInfo` (key, byte size, and modification time), not the original
filename or a content-type record; keep application metadata separately.

`PutFile` names the object itself: the directory you pass, then 26 random
lowercase characters, then an extension. The result is a key such as
`avatars/mfrggzdfmztwq2lknnwg23tpoi.png`. Uploads never overwrite each other,
and the client's filename never reaches the key. Keep the original name in your
database next to the key if you want to show it, available from the file
header's `Filename`.

The directory argument may be empty. Otherwise it must satisfy the
[key rules](overview.md#keys), with no trailing `/`; invalid directories return
`storage.ErrInvalidKey` before the file is read.

The extension comes from the content, not the name. `PutFile` reads the first
512 bytes of the file and detects its type with `http.DetectContentType`, so an
`evil.html` that really contains HTML is stored as `.html` however it was
named, and a file whose content is unknown is stored with no extension. If you
accept only certain kinds of file, check that before you store it; see
[Checking uploads](#checking-uploads).

When the key should come from your own data, `PutFileAs` stores the file at
exactly the key you give, replacing what is there:

```go
_, err = files.PutFileAs(r.Context(), "avatars/"+userID+".png", avatar)
```

Because the key is yours, so is the extension. Choose it from the type you
decided to accept, not from `Filename`.

> [!NOTE]
> `request.Multipart` returns `nil` from `File` when the form has no file under
> that name. `PutFile` and `PutFileAs` require a non-nil file header. Check it
> before either call, as above, or mark the field required in validation.

### Size limits

An upload limit is enforced in layers, and each layer catches something the
others cannot.

The first is the argument to `request.Multipart`. It caps the whole request,
files and fields together, and a larger request fails with a `413` problem
before the handler stores anything. Up to 8 MiB of file content is held in
memory while parsing and anything larger spills to temporary files, which the
router removes after the response.

The second is the [body limit middleware](../http-fundamentals/body-limit.md),
which sets a ceiling for a router or group before your handler runs. It rejects
a request that declares a too-large `Content-Length` immediately, and it caps
chunked uploads that declare nothing. Give upload routes their own, larger
limit and keep the rest of the API small. In this router-configuration
fragment, `r` is your existing `*router.Router`, not a request, and
`uploadAvatar` is your application's upload handler:

```go
r.Group("/uploads", func(uploads *router.Router) {
	uploads.Use(bodylimit.Middleware(func(limit *bodylimit.Builder) {
		limit.Limit(50 << 20)
	}))
	uploads.Post("/avatar", uploadAvatar)
})
```

A negative size makes `request.Multipart` use the limit from that middleware
instead of a number of its own, so the limit is written in one place:

```go
form, err := request.Multipart(r, -1)
```

The third is the size of a single file, which belongs in
[validation](../validation/advanced.md#validating-files). A route may allow
50 MiB in total and still want each avatar under 2 MiB.

### Checking uploads

The content type a client claims is only a claim. `validate.File` checks the
size and extension of an uploaded file and the type detected from its actual
first bytes, and reports failures as field errors. Run it before `PutFile`, so
a rejected file is never stored. [Validation](../validation/advanced.md#validating-files)
shows how to put it in a schema.

### Streaming a raw body

Not every upload is a form. An API client may `PUT` the file itself as the
request body. The body is already a stream, so pass it to `Put` without parsing
a form. Put this route behind the [body limit middleware](../http-fundamentals/body-limit.md).
The S3 driver buffers a non-seekable body fully in memory; use a bounded small
body here, or choose a [direct signed upload](signed-urls.md) for large files.
Authorize the document ID before deriving its key:

```go
key := "documents/" + r.PathValue("id") + ".pdf"
_, err := files.Put(r.Context(), key, r.Body)
```

Reading past the body limit makes the reader fail. `Put` then returns the error
and leaves the previous object untouched. Unlike the multipart helpers, `Put`
does not open or close its input: the HTTP server owns `r.Body` and closes it
after the handler returns. Check the `Content-Type` header yourself if it
matters, since nothing is detected for you.

> [!WARNING]
> The S3 driver must know the length before it uploads, so it buffers a body
> that cannot seek, such as `r.Body`, fully in memory. For large files on S3,
> let the client upload [straight to the bucket with a signed URL](signed-urls.md),
> or parse a multipart form, whose files are seekable.

## Serving files

After loading a document from your own records and checking that the current
user may read it, pass its stored key and original filename to `Download`:

```go
return files.Download(w, r, doc.Key, doc.OriginalName)
```

This writes a response that the browser saves. `Download` and `Inline` open
the object and always close its body; you do not need a separate `Open` call.
Returning the helper's error lets the router render a missing object as `404`.

The last argument to `Download` is the filename offered to the user. Pass an
empty string to use the last segment of the key. The helper sets
`Content-Disposition: attachment` with an ASCII fallback name and a UTF-8 name,
so names with accents or spaces arrive intact.

Look the key up from your own data before this call, and decide there
whether the current user may have the file. Storage does not know about users:
anyone who can make your handler pass a key can read that object.

### Headers and ranges

The content type comes from the extension of the filename you pass, and from
the first 512 bytes of the object when the extension tells nothing. Every
response carries `X-Content-Type-Options: nosniff`, so the browser uses that
type and does not guess a different one.

What else you get depends on the driver. The memory and filesystem drivers open
objects that can seek, and the helper then serves them with
`http.ServeContent`: `Range` requests are answered with `206 Partial Content`,
so video players can seek and downloads can resume, `HEAD` works, and
`Last-Modified` with `If-Modified-Since` produces `304 Not Modified`. The S3
driver returns a body that cannot seek, so there the helper streams the whole
object with its `Content-Length`. `HEAD` gets headers only, and ranges and
conditional requests are not supported. To let clients fetch large S3 objects
with ranges, send them to a [signed URL](signed-urls.md) instead and let S3
serve it.

### Showing a file in the page

`Inline` is `Download` for content that a page displays, such as an avatar in
an `<img>` tag or a PDF in a frame. Authorize this avatar ID before the call;
the helper does not check who owns it:

```go
return files.Inline(w, r, "avatars/"+r.PathValue("id")+".png")
```

Showing user content inline is how stored XSS happens: an uploaded HTML or SVG
file served from your origin runs with your cookies. `Inline` therefore adds
`Content-Security-Policy: sandbox` to every response and displays only an
allow-list of types: PNG, JPEG, GIF, WebP, AVIF, BMP, and ICO images, PDF,
audio, video, and plain text. Every other type, including HTML, SVG, XML,
JavaScript, CSS, WebAssembly, and JSON, is sent as an attachment, so the
browser saves it instead of running it. Serve user uploads this way, or from
a separate domain that holds no cookies.

### Errors

A missing object returns `storage.ErrObjectNotFound`, a `404` problem that the
router renders, and the error still wraps the driver's error, so
`errors.Is(err, storage.ErrNotFound)` stays true if you want to log it
differently. Nothing has been written to the response when either helper
returns an error before it starts. An error while copying the body arrives after
the response has begun, so it can only be logged: the client sees a truncated
download.

## Testing

Test handlers with the memory driver and `httptest`. In a test with
`t *testing.T` and a `files` facade backed by memory, store ten bytes and request
the middle three to verify range support:

```go
if _, err := files.PutBytes(t.Context(), "docs/a.txt", []byte("0123456789")); err != nil {
	t.Fatal(err)
}

req := httptest.NewRequest(http.MethodGet, "/files/a", nil)
req.Header.Set("Range", "bytes=2-4")
rec := httptest.NewRecorder()

if err := files.Download(rec, req, "docs/a.txt", "a.txt"); err != nil {
	t.Fatal(err)
}
if rec.Code != http.StatusPartialContent || rec.Body.String() != "234" {
	t.Fatalf("got %d %q", rec.Code, rec.Body.String())
}
```

[HTTP tests](../testing/http-tests.md) shows how to exercise a whole route
including its middleware.

For every function and option, see [the `storage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage)
and [the `request` API reference](https://pkg.go.dev/arkoren.dev/core/http/request).
