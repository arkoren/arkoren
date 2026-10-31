---
title: Signed URLs
description: Hand clients a temporary URL to download or upload one object directly against the storage backend, binding an upload's type and size.
order: 30
---

# Signed URLs

## Introduction

Serving a file through your application means every byte passes through your
servers: they open the object, copy it to the response, and hold a connection
for as long as the client is slow. For a large video or a heavy upload that is
wasteful, and on a platform that charges for bandwidth it is costly. A
**signed URL** removes your service from the data path. Your code asks the
storage backend for a URL that grants one operation on one key for a short
time, hands it to the client, and the client talks to the backend directly.

The URL carries its own authorization, a signature computed with your
credentials, so the client needs none. That makes it a credential in its own
right: anyone who has the URL can use it until it expires. Arkoren therefore
never records signed URLs in [telemetry](events-and-telemetry.md), and you
should not log them either.

Only some drivers can do this. Amazon S3 and S3-compatible stores sign URLs
natively. The memory and filesystem drivers cannot, because an object on a disk
or in a map has no address outside your process, and they report that with an
error. [Secure links from your own app](#serving-secure-links-yourself) cover
that case.

## Choosing between them

Choose how the bytes should reach the client before wiring a download or upload:

| You want | Use |
| --- | --- |
| Large downloads off your servers, on S3 | A signed storage URL |
| Browsers uploading large files straight to S3 | A signed `PUT` URL with type and length |
| Temporary links on the filesystem or memory driver | A [secure link](../http-capabilities/secure-links.md) and `Download` |
| To count or log each download | A secure link served by your app |

The next sections start with direct storage access. If the application must
serve the bytes, skip to [serving secure links yourself](#serving-secure-links-yourself).

## Signing a URL

Examples use `storage` from `arkoren.dev/core/service/storage`, standard library
`time`, `net/http`, and `errors`, and your operation context `ctx`. Focused
fragments keep error results visible but omit routine checks; do not send a URL
to a client unless signing succeeds.

After checking that a customer may read a private invoice, give them a
URL that grants access to that one file for a limited time. `Sign` creates the
URL from the object's key; `SignOptions.TTL` sets how long it works. Without an
explicit method, it grants a `GET`. Here `files` is the
[storage facade](overview.md#wiring-a-driver) backed by S3:

```go
address, err := files.Sign(ctx, "invoices/2026-03.pdf", storage.SignOptions{
	TTL: time.Hour,
})
```

After signing succeeds, send the string to the client, for example as a redirect
target or in a JSON response. If you need a parsed `*url.URL`, `SignURL` returns
that instead; it does not transfer the object. Do not
edit its signed path or query parameters: changing signed values invalidates
the signature. The TTL must be positive and no longer than the driver allows; S3
caps it at seven days. A TTL outside that range returns an error wrapping
`storage.ErrInvalidTTL`.

With temporary S3 credentials, the URL also stops working when the credential
session expires, even if the requested TTL is longer. Choose a short TTL and
issue a new URL when needed rather than extending the lifetime of credentials.

Signing does not fetch the object or check that it exists.
A URL for a missing key is perfectly valid and fails when the client uses it, so
`Stat` the object first if you need to give a better error.

The key is subject to the usual [key rules](overview.md#keys), and a configured
key prefix on the S3 driver applies, so the URL addresses exactly the object
that `Put` would write.

## Letting clients upload directly

For an upload, ask for a `PUT` URL. The client sends the file to it, and the
object is stored under the key exactly as if you had called `Put`, replacing
anything that was there:

> [!WARNING]
> A signed PUT authorizes replacement of this key and is reusable until expiry.
> Authorize the caller, choose an application-owned key, validate the requested
> size against your limit, and bind both type and positive exact byte length.
> Otherwise a URL holder can choose any unbound type or size and consume bucket
> space. Binding a type does not verify the file's contents.

Here `size` is that validated positive `int64` byte length:

```go
destination, err := files.Sign(ctx, "avatars/42.png", storage.SignOptions{
	Method:        http.MethodPut,
	TTL:           10 * time.Minute,
	ContentType:   "image/png",
	ContentLength: size,
})
if err != nil {
	return err
}
```

Only return `destination` to the client after signing succeeds. The upload client
must use `PUT`, send `Content-Type: image/png`, and send exactly `size` bytes with
the matching `Content-Length`. Signing does not send these headers for the client.

> [!NOTE]
> A browser can only `PUT` to another origin if the bucket has a CORS rule that
> allows your application's origin, the `PUT` method, and the `Content-Type`
> request header. Configure that on the bucket; Arkoren cannot do it for you.
> Follow the [S3 CORS configuration instructions](https://docs.aws.amazon.com/AmazonS3/latest/userguide/enabling-cors-examples.html)
> and allow your actual application origin rather than every origin.

The browser then uploads with a plain request. Here `file` is the selected
browser `File` whose byte length you validated; the browser manages its
`Content-Length`. `destination` is the URL returned by your API:

```js
const uploaded = await fetch(destination, {
  method: "PUT",
  headers: { "Content-Type": "image/png" },
  body: file,
});
if (!uploaded.ok) {
  throw new Error(`Upload failed: ${uploaded.status}`);
}
```

On S3, these header values are part of the signature. Without a binding, whoever
holds the URL chooses that header's value: for example, an unbound type permits
`text/html` under an avatar key. A nonempty type or positive length on a `GET`
URL returns `storage.ErrInvalidSignOptions`. After method validation, a negative
length returns that error too.

The default S3 presigner can bind `ContentType` without `ContentLength`. Zero
leaves the length unbound; it does not restrict the upload to an empty file.
A positive size is an exact match, not a maximum. S3 can bind a length but not
a range in a presigned `PUT`; size ranges need a presigned `POST` policy, which
Arkoren does not offer. Ask the client for the size before you sign, check it
against your own limit, and sign that number.

If you supply a client through [`awsstorage.NewFrom`](drivers.md#configuration-2),
its presigner must report the exact requested `ContentType` as the sole
`Content-Type` value in the SDK's
[`v4.PresignedHTTPRequest`](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws/signer/v4#PresignedHTTPRequest)
record's `SignedHeader`. If middleware or the presigner drops or replaces that
value, `Sign` and `SignURL` return an error wrapping `storage.ErrUnsupported`,
not an upload URL with a weaker binding.
The driver does not rewrite the supplied client's middleware or close the client;
you retain ownership.

A successful signing call does not test an object server's enforcement. For an
[S3-compatible service](drivers.md#s3-compatible-services), verify that it honors
the signature and bound headers. Do not omit the client-required headers above,
and do not treat a bound content type as proof that the bytes are an image.

A direct upload bypasses the storage facade: its upload validation, write
events, and transfer telemetry do not run. Treat the key as
unconfirmed until you know the upload happened: have the client call your API
when it finishes, then `Stat` the object and record it. Remember that what was
stored is whatever the client sent; if the file matters, such as an image you
will process, verify the content after the upload.

## Drivers that cannot sign

Signing returns an error wrapping `storage.ErrUnsupported` if the driver has no
presigned access or the supplied S3 presigner cannot preserve the requested type
binding. There is no separate capability check to perform ahead of time. Call
`Sign` and test the error rather than retrying without the bindings.

In this handler fragment, `key` and `opts` are your authorized key and validated
signing options, and `w` is the response writer. Import `response` from
`arkoren.dev/core/http/response` to send a successful URL:

```go
destination, err := files.Sign(ctx, key, opts)
switch {
case errors.Is(err, storage.ErrUnsupported):
	// This upload cannot be signed as requested: accept it through your app.
case err != nil:
	return err
default:
	return response.JSON(w, http.StatusOK, map[string]string{"url": destination})
}
```

With a compatible S3 presigner, the client gets a signed URL. On the filesystem
driver, or when the S3 presigner cannot preserve the requested type, your handler
can instead receive the file itself, as described in
[Uploads and downloads](uploads-and-downloads.md). Keep the same authorization,
type, and size checks on that fallback path. Every request is validated
before the driver is consulted, so an invalid key, TTL, or option reports its
own error even on a driver that cannot sign.

The memory driver cannot sign either, which is what you want in unit tests of
the fallback path. To test the signing path, use the S3 driver against a local
S3-compatible server as described in [Drivers](drivers.md#running-locally-1).

## Serving secure links yourself

Import `link` from `arkoren.dev/core/http/link`.

When the backend cannot sign, you can still give people temporary, unforgeable
links to your files. The [secure links](../http-capabilities/secure-links.md) of the
`link` package sign a URL of your own application. The route behind it verifies
the signature and then serves the object with `Download`. The bytes still
travel through your service, but the link works without a login and stops
working on schedule.

Create the link where you decide who may have the file. Here `secret` is the
validated `key.Shared` prepared as described in the [secure links guide](../http-capabilities/secure-links.md):

```go
signer, err := link.New(secret, func(signer *link.Builder) {
	signer.Origin("https://app.example.com")
})

shared, err := signer.Temporary("/shared/"+doc.ID, 24*time.Hour)
```

Then protect your application's download handler with signature verification on
your existing router `r`. Here `downloadDocument` is your handler: it looks up
the document by `{id}` and returns `files.Download(w, r, doc.Key, doc.OriginalName)`, as shown in
[Serving files](uploads-and-downloads.md#serving-files):

```go
r.With(link.Middleware(signer)).Get("/shared/{id}", downloadDocument)
```

An expired or tampered link gets a `403` before your handler runs. Because the
handler is your code, you can do what a signed storage URL cannot: log each
download, count it, limit it per link, or look the document up in your
database. The trade-off is load. If you run on S3 and the files are large,
prefer a signed storage URL and keep the app-served link for stores that cannot
sign.

For every option and error, see [the `storage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage)
and [the `link` API reference](https://pkg.go.dev/arkoren.dev/core/http/link).
