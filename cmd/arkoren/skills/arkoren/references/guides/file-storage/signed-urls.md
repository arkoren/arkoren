---
title: Signed URLs
description: Authorize direct S3 transfers, bind upload headers and exact size, and distinguish provider URLs from application-signed routes.
order: 40
summary:
  - Grant short-lived direct downloads after authorizing the stored object.
  - Bind direct uploads to an application-owned key, content type, and exact byte length.
  - Confirm uploaded content and handle unsupported signing without weakening restrictions.
  - Distinguish provider capabilities from signed application routes and their transfer costs.
---

# Signed URLs

A large transfer need not pass through your application server. A **signed URL**
grants a client GET or PUT access to one object directly at the provider. Your
application authorizes the recipient and signs the request; the provider serves
the bytes without that client presenting your storage credentials.

The URL is itself a bearer credential. Anyone holding it can use the granted
operation while the provider accepts it, so return it only to an authorized
recipient and keep it out of logs, analytics, and shared diagnostics. Facade
[telemetry](events-and-telemetry.md#privacy-boundaries) excludes signed URLs.

Examples assume `files *storage.Storage` backed by the
[S3 driver](drivers.md#amazon-s3) and a non-nil operation context `ctx`. Import
`storage` from `arkoren.dev/core/service/storage`, plus standard `time`,
`net/http`, and `errors` where used. Memory and filesystem drivers do not sign
provider URLs.

## Signed Downloads

After loading and authorizing a private invoice record, use its stored key to
issue a short-lived download. Here `invoice.Key` is an application-owned string:

```go
address, err := files.Sign(ctx, invoice.Key, storage.SignOptions{
	TTL: 15 * time.Minute,
})
```

An omitted method means GET. Return the string only after checking `err`;
`SignURL` provides a parsed `*url.URL` instead. Do not edit the signed path or
query. The configured S3 prefix applies, so this addresses the same logical
object as `Open` or `Put`.

Signing does not open the object or check existence. A signed GET for a missing
key fails when fetched; use `Stat` first if your issuance flow needs that check.
Provider requests do not pass through your app's authorization middleware or
`Download`/`Inline` presentation safeguards. Review bucket permissions, stored
content metadata, and the origin that serves untrusted content separately.

TTL must be positive. S3 accepts at most seven days; larger values wrap
`storage.ErrInvalidTTL`. Temporary AWS credentials can expire sooner, making the
URL unusable before its requested TTL. Prefer short grants and issue new ones
when needed. This API does not supply one-time consumption or per-link
revocation; do not infer either from the TTL.

## Direct Uploads

A signed PUT permits replacing one key and can be reused while valid. Before
issuing it, authorize the upload target and allocate an application-owned key
that does not expose another user's object. Do not accept an arbitrary key or
filename from the client.

For an avatar, accept a decoded request with a claimed exact size. The format
policy is PNG, selected by your application rather than the client's MIME claim:

```go
type AvatarUpload struct {
	Size int64 `json:"size"`
}
```

With `input AvatarUpload`, check its positive size against the application limit
before signing. Import `problem` from `arkoren.dev/foundation/problem` for this
handler fragment:

```go
if input.Size <= 0 || input.Size > 2<<20 {
	return problem.UnprocessableContent().Derive(func(details *problem.Builder) {
		details.Detail("The avatar must contain between 1 byte and 2 MiB.")
	})
}
```

Here `key` is the upload key you allocated for the authorized user. Bind both
the expected type and exact byte length:

```go
destination, err := files.Sign(ctx, key, storage.SignOptions{
	Method:        http.MethodPut,
	TTL:           10 * time.Minute,
	ContentType:   "image/png",
	ContentLength: input.Size,
})
if err != nil {
	return err
}
```

Only expose `destination` after success. The uploader must send PUT,
`Content-Type: image/png`, and exactly `input.Size` bytes with the matching
`Content-Length`. Signing does not send these headers for the client. Binding
the MIME header does not establish that the bytes are a valid or safe image.

For browser uploads, configure bucket CORS to allow your application origin,
PUT, and the `Content-Type` request header; follow the
[S3 CORS instructions](https://docs.aws.amazon.com/AmazonS3/latest/userguide/enabling-cors-examples.html).
CORS is not authorization. Allow your real origin rather than every origin.
Given the selected browser `File` named `file`, whose size matches the signed
request, upload with:

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

The browser manages `Content-Length`; do not try to set that forbidden request
header manually.

## Upload Bindings

An empty `ContentType` leaves the type unbound. Zero `ContentLength` leaves
size unbound, not restricted to an empty upload. A positive length is an exact
match, not a maximum. S3 presigned PUT cannot express a size range; presigned
POST policies are a different mechanism that Arkoren does not offer.

Content type can be bound without length, but use both for user uploads.
Otherwise the URL holder selects any unbound type or size. A negative length,
or type/length bindings on GET, wraps `storage.ErrInvalidSignOptions`.

With a client supplied through [Native Access](drivers.md#native-access), the
presigner must report the requested type as the sole signed `Content-Type`
value in its
[`v4.PresignedHTTPRequest.SignedHeader`](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws/signer/v4#PresignedHTTPRequest).
A missing or changed type produces `storage.ErrUnsupported` instead of a weaker
URL. Arkoren does not modify borrowed middleware or close the supplied client.

For an [S3-compatible service](drivers.md#s3-compatible), verify the provider
actually enforces signatures and bound headers. Signing success alone does not
test server enforcement. Never recover from unsupported bindings by silently
retrying without them.

## Confirming Uploads

A direct client write bypasses facade upload validation, write events, and
transfer telemetry. Keep the allocated key unconfirmed until your application
has verified the upload. When the client reports completion, authorize that
confirmation against the same owner's pending upload and inspect it:

```go
info, err := files.Stat(ctx, key)
```

After checking `err`, compare the size with the pending record and validate
contents before publishing the object's application record. `Stat` establishes
metadata, not content safety. A holder can reuse the PUT URL while valid, so a
completion callback alone does not make the object immutable. If your workflow
requires a validated immutable asset, design a separate trusted promotion step
rather than serving a still-writable pending key.

## Unsupported Signing

Memory and filesystem stores return an error wrapping `storage.ErrUnsupported`;
an S3 client without presigning, or unable to retain the requested type binding,
does too. Call the operation and classify its result:

```go
destination, err := files.Sign(ctx, key, options)
if errors.Is(err, storage.ErrUnsupported) {
	// Offer an application-served transfer with the same authorization policy.
}
```

Here `options` is your validated `storage.SignOptions`. Do not return
`destination` on error. A fallback can accept a
[bounded upload](uploads-and-downloads.md#accepting-uploads), not a less
restricted signed request. Test the fallback with memory storage; exercise
provider signing separately against the deployment backend.

Cancellation precedes input validation. Otherwise validation checks key, TTL
and backend cap, method, bindings, then signing capability. Only GET and PUT are
supported; unsupported methods also wrap `ErrUnsupported`. An invalid request
does not become valid merely because the driver cannot sign.

## App-Served Links

If the backend has no direct URL, or your application must check policy on each
request, sign an application route with
[Secure Links](../http-capabilities/secure-links.md) and serve its authorized
record using [Download](uploads-and-downloads.md#serving-files).
This is a separate mechanism: your route verifies the link and your server
transfers the bytes. Storage signing does not install route authentication,
create a receiver, or add download-count enforcement.

For signing options and error identities, see the
[`storage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage).
