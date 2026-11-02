---
title: Drivers
description: Choose and configure memory, filesystem, or S3 storage with explicit lifecycle, ownership, and backend limits.
order: 20
summary:
  - Compare process-local memory, persistent filesystem roots, and shared S3 storage.
  - Configure each driver and retain ownership of lifecycle and native dependencies.
  - Account for filesystem containment, permissions, replacement, and staging cleanup.
  - Use S3 prefixes, compatible endpoints, native access, and a disposable local fixture.
---

# Drivers

Choose storage by where your application runs and which processes must share
its files. Memory suits isolated tests; filesystem storage suits one host with
a persistent volume; S3 suits replicas that need the same objects and clients
that transfer directly through signed URLs.

All three use the same [object operations](overview.md) and portable key rules.
The facade borrows its driver: `storage.New` neither starts nor stops it.
Examples import `storage` from `arkoren.dev/core/service/storage` and use a
non-nil operation context `ctx`. Run installation commands in your application's
Go module, and check constructor errors before wrapping a driver.

Use memory for isolated tests, then run
[integration tests](../testing/integration-tests.md) against the driver you
deploy; passing memory tests does not establish provider permissions or limits.

## Compatibility Matrix

This compares behavior exposed by Arkoren, not every native provider feature.
Download ranges refer to the application-served `Download` and `Inline` helpers.

| Driver | Shared Across Hosts | Survives App Restart | Seekable Reads | Download Ranges | Signed Downloads | Signed Uploads |
| --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No | No | Yes | Yes | No | No |
| [Filesystem](#filesystem) | Partial — Requires the same mounted volume. | Partial — Requires the same persistent root. | Yes — Regular files only. | Yes | No | No |
| [Amazon S3](#amazon-s3) | Yes | Yes | No | No | Yes — Requires a presigner. | Partial — PUT only; the presigner must preserve requested bindings. |
| [S3-Compatible](#s3-compatible) | Yes | Partial — Depends on service persistence. | No | No | Partial — Requires compatible SigV4 signing. | Partial — Requires compatible signing and bound-header enforcement. |

Shared access assumes the same bucket/prefix or root and appropriate permissions.
Restart support does not promise backups, retention, or recovery from backend
failure. An ephemeral container directory is not persistent storage.
S3-compatible services use the S3 driver with a custom endpoint.

Every driver supports paged listing and the facade's [lazy traversal](overview.md#listing-objects).
Neither provides a multi-page snapshot. Stopping a walk prevents later facade
requests, not work already done preparing a page.

## Memory

The bundled memory driver stores objects only in this process. A new driver
starts empty, needs no lifecycle hooks, and is safe for concurrent use:

```go
import "arkoren.dev/core/service/storage/memorystorage"

files := storage.New(memorystorage.New())
```

Use the shared [write and read tasks](overview.md#writing-objects) with this
store. Bytes are copied into storage and into opened snapshots; later writes or
deletions do not change an already opened body. Bodies support seeking and
HTTP ranges, but objects consume application memory without a total-size quota.
Signing returns `storage.ErrUnsupported`.

If housekeeping needs a different default page size, configure the driver at
construction:

```go
driver := memorystorage.New(func(store *memorystorage.Builder) {
	store.ListLimit(500)
})
```

The default is 100; nonpositive configured values restore it. An explicit
positive `ListOptions.Limit` overrides it for that request.

See the [`memorystorage` API](https://pkg.go.dev/arkoren.dev/core/service/storage/memorystorage).

## Filesystem

Choose filesystem storage when one host owns a persistent directory. The
application's operating-system identity must be able to create and access that
directory. Keep the root application-controlled and separate from unrelated data;
portable key validation is not a substitute for filesystem permissions.

```sh
go get arkoren.dev/driver/filesystem
```

Import `filesystemstorage` from
`arkoren.dev/driver/filesystem/filesystemstorage`. Inside an existing
`compose *application.Composer` (from `arkoren.dev/core/application`), construct
the driver and register its lifecycle:

```go
driver := compose.Must(filesystemstorage.New("./data/uploads"))
compose.Cleanup(driver.Stop)
compose.Startup(driver.Start)
files := storage.New(driver)
```

`New` rejects an empty root and resolves an absolute path without disk I/O.
`Start` creates the directory if absent and opens it with `os.Root`; a
nondirectory root fails startup. Operations before `Start` or after `Stop` fail.
There are no optional construction settings. Follow
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md) for shutdown
ordering: finish active work before `Stop`, which closes the root without
joining readers or writers. Already opened bodies remain yours to close.

### Stored Files

After startup, the [shared write task](overview.md#writing-objects) stores
`reports/2026-q1.csv` at `<root>/reports/2026-q1.csv`; intermediate directories
are created as needed. New directories request mode `0750`, subject to umask,
and stored files are set to `0640` where the platform supports Unix modes.
Existing directory permissions are not repaired. Restrict root ownership and
access explicitly in your deployment.

Replacement writes a temporary file beside the destination, syncs and closes
it, then renames it into place. Atomic replacement depends on the volume's
rename semantics; it does not promise crash durability on every filesystem.
A later metadata failure can report an error after replacement succeeded.

New staging names start with `.storage-` and end in a dot, outside portable key
syntax, so listings omit unpublished bytes. A valid name such as
`.storage-report` is an ordinary object. Never remove files by prefix alone:
stop writers, inspect ownership and leftovers, and remove only confirmed staging
files. Back up the root before native migrations or cleanup.

### Path Containment

Operations through `os.Root` cannot traverse symbolic links outside the root;
such attempts fail with an operational error, not `storage.ErrNotFound`.
Internal symlinks can be followed for object operations. Listings skip symlinks
encountered during walking, including links that point inside the root; a
listing prefix that traverses an outside link fails.

`Open` and `Stat` accept regular files only. Directories are missing logical
objects; FIFOs, sockets, and other special entries report operational errors.
On Unix, FIFO opens do not wait for a writer. Regular bodies support seeking
and [download ranges](uploads-and-downloads.md#headers-and-ranges).

Listings walk the directory implied by the prefix, sort matching portable keys,
and default to 100 objects per page. Native regular files with portable names
can appear as objects; invalid names and special entries are omitted.

> [!WARNING]
> Shared network volumes can have different rename behavior and competing
> writers can overwrite one another. Prefer S3 for replicas on separate hosts;
> do not infer shared-write safety from a successful local filesystem test.

No emulator is needed locally. Write and read a test object, restart with the
same persistent root to verify persistence, then delete only that test key.
Stopping the driver does not remove stored data.

See the [`filesystemstorage` API](https://pkg.go.dev/arkoren.dev/driver/filesystem/filesystemstorage).

## Amazon S3

Use a pre-existing bucket and grant only needed actions: `s3:ListBucket` on
the bucket, and `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` on allowed
objects. `Put` also reads metadata after uploading, so it needs read permission.

```sh
go get arkoren.dev/driver/aws
```

Import `awsstorage` from `arkoren.dev/driver/aws/awsstorage`. Configure a region
and optional namespace while constructing the driver:

```go
driver, err := awsstorage.New(ctx, "private-documents", func(store *awsstorage.Builder) {
	store.Region("eu-west-1")
	store.Prefix("reports")
})
```

After checking the error, wrap it with `files := storage.New(driver)`. `New`
validates the bucket and prefix and loads AWS configuration; loading can read
local configuration or consult configured credential services. Object requests
begin with operations, not construction. No `Start` or `Stop` is required, and
construction does not verify bucket access.

Credentials and an omitted region use the AWS default configuration chain,
including environment, shared files, and workload roles. `Builder.Credentials`
accepts a shared `aws.CredentialsProvider` when that chain is unsuitable.

The prefix defaults empty; a configured `reports` becomes `reports/` before
logical keys. A logical `annual.pdf` addresses `reports/annual.pdf` in the bucket,
including for signing. Prefixes separate names, not permissions: enforce access
in application authorization and bucket policy.

### Transfer Limits

Use the shared [write and read tasks](overview.md#writing-objects). `Put` streams
from a seekable source but buffers a non-seekable reader fully in memory before
upload. Bound source sizes; choose a seekable file or
[direct signed upload](signed-urls.md#direct-uploads) for large transfers.

An upload is followed by a metadata request. If that request fails, the new
object can already exist despite the returned error; inspect with `Stat` before
deciding recovery. No facade write event is emitted for a failed call.

`Open` returns a non-seekable response body. App-served downloads stream the
whole object without ranges or conditional handling; a signed GET lets S3 serve
the client directly. Signing supports GET and PUT, at most seven days, and
temporary credentials can shorten that validity. See
[Signed URLs](signed-urls.md) for bindings and authorization.

`List` maps positive limits to `MaxKeys`; S3 returns at most 1,000 keys per page.
Nonportable native keys are omitted. Continuation checks can read ahead through
physical pages, adding requests while preserving native opaque cursor strings.
The empty cursor means no further matching portable object was found.

### S3-Compatible

To use another S3 implementation, set its endpoint. This also enables path-style
addressing. For the disposable local server below:

```go
driver, err := awsstorage.New(ctx, "dev", func(store *awsstorage.Builder) {
	store.Endpoint("http://localhost:9001")
	store.Region("us-east-1")
})
```

Supply that service's credentials through the default chain or
`Builder.Credentials`. Check its persistence, listing, SigV4, and signed-header
enforcement rather than assuming all S3 behavior. A successful emulator test
does not establish cloud IAM permissions or durability.

See the [`awsstorage` API](https://pkg.go.dev/arkoren.dev/driver/aws/awsstorage).

## Native Access

If SDK retry, HTTP timeout, or middleware settings need customization, configure
a native client first. With `awsconfig` imported from
`github.com/aws/aws-sdk-go-v2/config` and `s3` from
`github.com/aws/aws-sdk-go-v2/service/s3`:

```go
settings, err := awsconfig.LoadDefaultConfig(ctx)
if err != nil {
	return err
}
client := s3.NewFromConfig(settings)
driver, err := awsstorage.NewFrom(client, "private-documents", func(store *awsstorage.Builder) {
	store.Prefix("reports")
})
```

Only `Prefix` applies to `NewFrom`; region, endpoint, and credential settings
already belong to the client. The driver neither copies nor closes it. Arkoren
adds no timeout or retry policy over SDK settings; use operation deadlines and
configure native policies before sharing the client.

For an operation outside the facade, recover the concrete driver with
`files.DriverAs[*awsstorage.Driver]()`. Both this result and `files.Driver()`
are shared, not copied. With your existing bucket name `bucket`:

```go
if driver, ok := files.DriverAs[*awsstorage.Driver](); ok {
	if client := driver.Client(); client != nil {
		_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &bucket})
		if err != nil {
			return err
		}
	}
}
```

`Client()` is nil for non-native injected clients. A successful bucket check
does not verify every object permission. Native operations bypass facade
validation, prefix mapping, events, and telemetry. Large non-seekable streams
can use this client with the AWS upload manager instead of unbounded `Put`.

A native client supplied to `NewFrom` must provide presigning to sign URLs.
For bound uploads, its presigner must report the exact requested content type
as the sole signed `Content-Type` value; missing or changed bindings return
`storage.ErrUnsupported`, not a weaker URL. Arkoren does not rewrite borrowed
middleware. See [upload bindings](signed-urls.md#upload-bindings).

The reporting-only `awsstorage.Config` redacts credential-provider details, not
endpoint or prefix text. Native diagnostics, middleware, and provider errors
are not sanitized by facade telemetry; keep secrets out of those settings and
review native logging separately.

## Local S3

For disposable local storage, install Docker with Compose/BuildKit and the AWS
CLI. The original pinned `minio/minio` image is no longer served by Docker Hub.
Build this release from its
[official artifacts](https://github.com/minio/minio/releases/tag/RELEASE.2025-09-07T16-13-09Z),
not an unverified mirror. This fixture supports Linux amd64/arm64 containers,
not a production MinIO deployment.

Save this `Dockerfile` in a new empty development directory. Keep the pinned
base and binary checksums; a mismatch or unsupported architecture must fail:

```dockerfile
FROM alpine:3.22.7@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f
ARG TARGETARCH
RUN set -eu; \
    case "${TARGETARCH}" in \
        amd64) checksum=7c5bd8512c6e966455b1d198209358b2d191c77a83ab377c4073281065fb855f ;; \
        arm64) checksum=5c83cd2cf151717ba0243f73e1c7802ff36e272b67144bdd7f1f7d684fd6f03d ;; \
        *) printf 'Unsupported MinIO fixture architecture: %s\n' "${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    release=RELEASE.2025-09-07T16-13-09Z; \
    wget -q -O /usr/local/bin/minio \
        "https://github.com/minio/minio/releases/download/${release}/minio.linux-${TARGETARCH}.${release}"; \
    printf '%s  /usr/local/bin/minio\n' "${checksum}" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/minio
ENTRYPOINT ["minio"]
```

Beside it, save `compose.yaml`, not over an existing application configuration.
These public credentials are for disposable local data only; retain loopback
binding:

```yaml
services:
  storage:
    build: .
    command: server /data
    environment:
      MINIO_ROOT_USER: localstorage
      MINIO_ROOT_PASSWORD: localstorage-password
    ports:
      - "127.0.0.1:9001:9000"
```

Start the service and configure the local client's credentials:

```sh
docker compose up -d --build
export AWS_ACCESS_KEY_ID=localstorage
export AWS_SECRET_ACCESS_KEY=localstorage-password
export AWS_DEFAULT_REGION=us-east-1
```

After the endpoint is ready, create the bucket; reuse it if it already exists:

```sh
aws --endpoint-url http://localhost:9001 s3 mb s3://dev
```

Use the [S3-compatible constructor](#s3-compatible), write and read a small test
object through the facade, then inspect it with
`aws --endpoint-url http://localhost:9001 s3 ls s3://dev`. The signing endpoint
must also be reachable by the client using the URL.

Run `docker compose down` when finished. This fixture does not preserve objects
after its container is removed.

For facade operations, see the
[`storage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage).
