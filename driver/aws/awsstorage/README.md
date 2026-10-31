# S3 object storage

Use `arkoren.dev/driver/aws/awsstorage` to store portable objects in an existing
S3 bucket. Install with `go get arkoren.dev/driver/aws` and import `awsstorage`
from `arkoren.dev/driver/aws/awsstorage`. Use your startup context and bucket:

```go
driver, err := awsstorage.New(ctx, "uploads", func(store *awsstorage.Builder) {
	store.Prefix("tenant-a")
})
```

Pass the driver to `storage.New` from `arkoren.dev/core/service/storage`.
Construction loads AWS configuration through its default credential chain; it
does not provision a bucket or test permissions. Callbacks run once in order on
one fresh draft. Owned settings are detached; native credential providers stay
shared. NewFrom borrows its client and applies only Prefix, leaving region,
endpoint, credentials, middleware, and transport cleanup caller-owned.

## Signing an upload

Bind both MIME type and exact length when granting an upload URL:

```go
link, err := driver.SignURL(ctx, "avatars/ada.png", storage.SignOptions{
	Method:        http.MethodPut,
	TTL:           10 * time.Minute,
	ContentType:   "image/png",
	ContentLength: 2048,
})
```

Give the uploader `link` and both bound header values. Zero length leaves size
unbound; S3 PUT cannot express a maximum-size range. If borrowed middleware drops
or changes the requested signed type, SignURL fails with `storage.ErrUnsupported`
instead of returning a weaker URL. The driver does not modify that middleware.
Temporary credentials can expire before the requested TTL; the maximum TTL is
seven days. A signed GET does not check object existence during signing.

## Reading and listing

Callers must close bodies returned by Open. Nonseekable Put readers are buffered
in memory; use the native S3 upload manager for very large streams. An already
canceled call consumes nothing, but cancellation cannot force an arbitrary
blocking reader or native dependency to return.

Listings omit nonportable native keys. To determine whether another logical
object exists, listing can read ahead through physical S3 pages; deployed native
cursor strings remain unchanged. Use a cursor only with the same driver and
logical prefix. Concurrent bucket changes can affect subsequent pages.

Config formatting records credential-provider types, never provider contents.
Endpoint and Prefix are not redacted; keep secrets out of diagnostic strings.

See the [storage driver guide](../../../cmd/arkoren/skills/arkoren/references/guides/file-storage/drivers.md)
and the [`awsstorage` API reference](https://pkg.go.dev/arkoren.dev/driver/aws/awsstorage).
