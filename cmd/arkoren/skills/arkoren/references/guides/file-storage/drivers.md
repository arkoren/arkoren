---
title: File Storage Drivers
nav: Drivers
description: Choose between the memory, local filesystem, and Amazon S3 storage drivers, connect and configure each, and use S3-compatible services.
order: 50
---

# File Storage Drivers

## Introduction

Files that survive a local restart may still disappear when a container moves
to another host. Choose storage by where your application runs and whether
every replica must read the same files.

Arkoren ships three storage drivers: memory, the local filesystem, and Amazon
S3. All of them implement the same contract, so you use the same `storage`
facade, [key rules](overview.md#keys), and portable error identities. Backend
failures still depend on the driver. What differs
is where the bytes live, and a few places where that shows through: whether an
object can seek, whether a URL can be signed, and how large a page of results
is. This page covers how to connect each driver and what to know before you
rely on it.

To choose a driver, start from how you run:

- **Memory** suits tests and throwaway prototypes. Nothing is written
  anywhere, and everything is gone when the process exits.
- **Filesystem** suits a service on a single host, and local development that
  needs files to survive a restart. It cannot be shared between hosts unless
  they mount the same volume.
- **Amazon S3**, or a service that speaks its protocol, suits production
  deployments with more than one replica, large files, and direct client
  access through [signed URLs](signed-urls.md).

Because they share one interface, you can develop on the filesystem and deploy
on S3 by changing the line that builds the driver. Run your integration tests
against the driver you deploy; see [Integration tests](../testing/integration-tests.md).

## Compatibility matrix

Compare capabilities exposed by Arkoren's storage driver and download helper,
not every feature offered by an object-storage service. The S3-compatible row
uses the Amazon S3 driver with a custom endpoint; it is not a separate driver.

| Driver | Shared across hosts | Survives app restart | Seekable reads | Download ranges | Signed downloads | Signed uploads |
| --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No | No | Yes | Yes | No | No |
| [Filesystem](#filesystem) | Partial — Hosts must mount the same volume; network-filesystem behavior still applies. | Partial — Requires the same persistent root directory. | Yes — Opened objects must be regular files. | Yes — Regular-file bodies use the native HTTP range helper. | No | No |
| [Amazon S3](#amazon-s3) | Yes | Yes | No | No | Yes | Yes — PUT; a supplied presigner must preserve the requested signed content type. |
| [S3-compatible services](#s3-compatible-services) | Yes | Partial — Depends on the service's persistence configuration. | No | No | Partial — The service must implement compatible SigV4 signing. | Partial — The service must honor SigV4 signatures and any bound headers. |

Shared storage assumes the same root or bucket and prefix, with appropriate
permissions. Restart support means an application restart does not clear the
backend; it does not promise backups, retention, or survival of backend failure.
A filesystem root on ephemeral container storage does not survive replacement.
Use S3 rather than a network filesystem for shared replica writes.

Download ranges refer to Arkoren's `Download` helper, which needs a seekable
body. S3's native range support does not make its streamed `Open` body seekable;
use a signed URL for direct client downloads instead. S3 signing supports GET
and PUT with a maximum seven-day TTL, shortened by temporary-credential expiry.
Clients supplied through `NewFrom` must support presigning; a client without it
returns `storage.ErrUnsupported`. The driver also rejects presigner results that
omit or replace a requested content type. Upload clients must send the bound headers;
compatibility with a custom endpoint, including enforcement of those headers,
must be checked against that service.

All rows use the [portable key and prefix rules](overview.md#keys), including
Windows filename restrictions even on other operating systems. These are
exposed capabilities, not a claim that an emulator proves a cloud service's
permissions, durability, or signed-header enforcement.

Every row supports the facade's [lazy object traversal](overview.md#listing-files)
through its existing paged `List` operation. `Walk` does not add driver
capabilities or snapshot guarantees. Stopping traversal prevents future facade
page requests, but does not undo memory/filesystem page preparation or S3
read-ahead already performed for the current page. Custom S3-compatible services
must still honor the underlying listing and continuation protocol.

## Wiring a driver

Run installation commands in your application's Go module. Examples use
`storage` from `arkoren.dev/core/service/storage`, your operation context `ctx`,
and an existing `app` from the [lifecycle guide](../application/lifecycle-and-ownership.md).
Focused snippets omit routine error checks, following [the overview](overview.md#introduction).

Constructors validate settings; S3 also loads AWS configuration, which can read
local files or consult configured credential services. For the filesystem driver,
register Start and Stop as shown below. Memory and S3 have no lifecycle hooks.
With your configured `driver`, wrap it in the facade:

```go
files := storage.New(driver)
```

[Lifecycle and ownership](../application/lifecycle-and-ownership.md) explains
cleanup ordering. Construction does not verify S3 bucket permissions.

## Memory

The memory driver keeps objects in the process. It is part of `arkoren.dev/core`,
so there is nothing extra to install, and it is safe for concurrent use.

### Connecting

`New` never fails:

```go
import "arkoren.dev/core/service/storage/memorystorage"

files := storage.New(memorystorage.New())
```

### Configuration

The only setting is the page size of `List` when a request sets no limit,
which is 100 by default:

```go
driver := memorystorage.New(func(store *memorystorage.Builder) {
	store.ListLimit(500)
})
```

### Storing objects

Use the shared [write](overview.md#writing-files) and [read](overview.md#reading-files)
examples with `files`. A successful write can be read immediately; no file is
written to disk and a new driver starts empty.

### Behavior and limits

Objects are copied in and out, so a body you opened is a snapshot that later
writes do not change. The body can seek, so [`Download`](uploads-and-downloads.md#headers-and-ranges)
serves range requests. Listings are ordered by key. The driver cannot sign
URLs: `Sign` returns `storage.ErrUnsupported`. Nothing limits the total size
except the memory of the machine.

For every function and option, see the [`memorystorage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage/memorystorage).

## Filesystem

The filesystem driver stores each object as a regular file below a root
directory. Choose it for a single host or for development that needs data to
persist.

### Installation

```sh
go get arkoren.dev/driver/filesystem
```

### Connecting

Pass the root directory to `New` and wrap the driver:

```go
import (
	"arkoren.dev/core/service/storage"
	"arkoren.dev/driver/filesystem/filesystemstorage"
)

driver, err := filesystemstorage.New("./data/uploads")
files := storage.New(driver)
```

`New` resolves the path to an absolute one and rejects an empty root.
Check its error before wrapping the driver. Register `Stop` as cleanup and
`Start` as startup with the
[application lifecycle](../application/lifecycle-and-ownership.md).
`Start` creates the directory if it is absent and opens it, failing when the
path exists but is not a directory. Using the driver before `Start`, or after
`Stop`, returns an error.

### Configuration

There is nothing to configure. The root is the one required argument of `New`.

### Storing objects

Use the shared [write](overview.md#writing-files) and [read](overview.md#reading-files)
examples after the application starts. With this root, writing `avatars/42.png`
creates `./data/uploads/avatars/42.png` in your application's working directory.

### Behavior and limits

A key maps to a path below the root, so `avatars/42.png` is the file
`<root>/avatars/42.png`, and intermediate directories are created as needed.
`Put` writes to a temporary file, syncs it, and renames it into place. Atomic
replacement depends on the filesystem's rename semantics; it is not a promise
of crash durability for every volume. New staging names start with `.storage-`
and end in a dot, outside portable object-key syntax, so listings omit them.
Valid names such as `.storage-report` are ordinary application objects. Never
delete files merely because they have the `.storage-` prefix: inspect leftovers
and their ownership, and stop writers before removing a confirmed staging file.

The driver opens the root with Go's `os.Root`, which keeps every operation
inside it. Reading, writing, or deleting through a symbolic link that points
outside the root fails with an error that is not `storage.ErrNotFound`. Links
that stay inside the root are followed for these operations. Listings skip
symbolic links encountered during the walk, whether they point inside or outside
the root, since a listing returns only regular files. A list prefix that tries
to traverse a link outside the root fails instead.

`List` returns files ordered by key and reads only the directory a prefix
implies, so listing `avatars/` does not walk the whole tree. A request without a
limit returns at most 100 objects. Bodies can seek, so range requests work, and
the driver cannot sign URLs.

Open and Stat accept regular files, not FIFOs, sockets, or other special entries.
Directories are missing logical objects; other nonregular entries report an
operational error. On Unix, opening is nonblocking before validating the actual
handle, so a FIFO does not wait for a writer. Regular files retain seeking and
the download helper's native HTTP range handling. Concurrent deletion that finds
the object already missing succeeds. Finish active application work before Stop;
it closes the root without joining readers or writers, while already opened
bodies remain the caller's to close.

> [!WARNING]
> Two hosts that write to the same directory through a network share can
> overwrite each other, and rename is not atomic on every network filesystem.
> For several replicas, use S3 or a service built for shared access.

Back up the root directory like any other data. Native writers can add regular
files with portable names that appear as objects; nonportable names and special
entries are not ordinary listed objects. Keep the directory to storage alone.

### Running locally

No service or emulator is needed. Use the constructor above with a writable
application directory, start your application, and write an object using the
shared task. Read the same key to verify its contents. Restart the application
with the same working directory and root to verify persistence. Delete only
the test objects with `files.Delete`; stopping the driver does not delete data.

For every function and option, see the [`filesystemstorage` API reference](https://pkg.go.dev/arkoren.dev/driver/filesystem/filesystemstorage).

## Amazon S3

The S3 driver stores objects in one bucket, under an optional key prefix. It
works with Amazon S3 and with services that implement its protocol.

### Installation

```sh
go get arkoren.dev/driver/aws
```

### Connecting

`New` takes a context and the bucket name. It loads the AWS default
configuration, the same chain the AWS CLI uses, so credentials and region come
from environment variables, shared config files, or the instance or task role.
The bucket must already exist. Grant only the needed bucket and object actions:
`s3:ListBucket` on the bucket, and `s3:GetObject`, `s3:PutObject`, and
`s3:DeleteObject` on the allowed prefix. `Put` also needs read permission for
the metadata request after upload:

```go
import "arkoren.dev/driver/aws/awsstorage"

driver, err := awsstorage.New(ctx, "my-bucket",
	func(store *awsstorage.Builder) {
		store.Region("eu-west-1")
		store.Prefix("uploads")
	},
)

files := storage.New(driver)
```

No S3 request is sent until the first operation, and the driver has no lifecycle
hooks. A blank bucket name is an error.

### Configuration

Set optional values in trailing `awsstorage.BuilderFunc` callbacks:

| Builder method | Meaning |
| --- | --- |
| `Builder.Region` | The AWS region. Defaults to the one the default chain resolves. |
| `Builder.Endpoint` | A custom endpoint, for S3-compatible services. Also switches to path-style addressing. |
| `Builder.Prefix` | A namespace inside the bucket. A trailing `/` is added for you. |
| `Builder.Credentials` | A credentials provider. Defaults to the default chain. |

The prefix is invisible to your code. With the prefix `uploads`, the logical key
`avatars/42.png` is stored as `uploads/avatars/42.png`, and listings and
signed URLs account for it. This separates names between environments or
tenants, but does not grant or restrict access; enforce that in your application
and bucket policy.

To control the S3 client yourself, for example to add tracing, retries, or a
custom HTTP client, build it and hand it over with `NewFrom`:

```go
import (
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
client := s3.NewFromConfig(awsConfig)

driver, err := awsstorage.NewFrom(client, "my-bucket", func(store *awsstorage.Builder) {
	store.Prefix("uploads")
})
```

Only Prefix applies from the construction callbacks; Region, Endpoint and
Credentials are ignored because the client already
carries the region, endpoint, and credentials. The caller keeps ownership of
the client. `driver.Client()` returns the `*s3.Client` the driver uses, for
operations the facade does not cover, such as the AWS upload manager for very
large streams.

The reporting-only `awsstorage.Config` records credential-provider types without
retrieving credentials or invoking provider formatters. Endpoint and Prefix
text are not sanitized. Native clients, middleware, credential providers and
their diagnostics remain shared; do not treat native logging or errors as
covered by the facade's privacy rules.

For a [bound signed upload](signed-urls.md#letting-clients-upload-directly), the
supplied presigner must report the exact requested `ContentType` as the sole
`Content-Type` value in the returned
[`v4.PresignedHTTPRequest`](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws/signer/v4#PresignedHTTPRequest)
record's `SignedHeader`. If that value is missing or changed, signing returns an
error wrapping `storage.ErrUnsupported` instead of an unbound URL.
The default S3 presigner supports type-only binding;
zero `ContentLength` leaves size unbound. The driver does not change the supplied
client's middleware to force a binding, and it never closes the client.

The driver adds no timeout or retry policy over the AWS SDK configuration.
Use operation contexts with deadlines; configure native SDK retries and HTTP
timeouts before passing a client to `NewFrom` when the defaults do not fit.

### Storing objects

Use the shared [write](overview.md#writing-files), [read](overview.md#reading-files),
and [direct signed upload](signed-urls.md#letting-clients-upload-directly) tasks.
The configured prefix applies to all of them. A prefix separates names, not
permissions; enforce tenant access in your application and bucket policy.

### Behavior and limits

Objects are not seekable. `Open` returns the response body as a stream, so
[`Download`](uploads-and-downloads.md#headers-and-ranges) sends the whole
object without range support, and large downloads are better served through a
[signed URL](signed-urls.md).

> [!WARNING]
> A non-seekable upload is buffered fully in memory. Bound request sizes and
> use a seekable reader or a direct signed upload for large objects to avoid
> exhausting application memory.

`Put` needs the length of the body. It streams from a reader that can seek, such
as an uploaded multipart file, and buffers any other reader fully in memory
first. A `Put` of a large body from an HTTP request is the case to avoid. After
the upload the driver reads the object's metadata from S3, so a put costs two
requests.

An already-canceled raw driver call returns the context error before key/prefix
validation or I/O; `Put` does not consume its input. Cancellation after a read
has started cannot force an arbitrary reader to return, and native credentials,
middleware, and transports must cooperate with their contexts. The facade can
reject invalid input before calling the driver; see [error precedence](overview.md#errors).

An upload can succeed while the following metadata request fails. In that case
`Put` returns an error even though the new object exists, and no facade write
event is emitted. Inspect the key with `Stat` before deciding how to recover.

`Delete` is idempotent. `List` maps the limit to S3's `MaxKeys`, and S3 itself
returns at most 1,000 keys per page. Listings are ordered by key.
Native objects with nonportable keys are omitted. To determine whether another
logical object exists, List can read ahead through physical pages containing
only omitted keys. This can add requests; the returned cursor keeps its native
S3 representation and points before the next usable page, not after it. An
empty cursor means no further matching portable object was found. Do not parse
or rewrite it, and do not assume a snapshot across concurrent bucket changes.

URL signing uses SigV4, whose longest validity is seven days. A longer TTL
returns `storage.ErrInvalidTTL`. If your credentials are temporary, such as an
assumed role, a signed URL stops working when the session expires, even if its
TTL is longer. Keep URLs short-lived and issue a fresh one after expiry rather
than creating long-lived credentials just to extend a link. See
[Signed URLs](signed-urls.md) for the options and the CORS rule a browser upload
needs.

### S3-compatible services

Set an endpoint to use MinIO, LocalStack, or another service that implements
the S3 protocol. The driver switches to path-style addressing, which these
services expect:

```go
driver, err := awsstorage.New(ctx, "dev",
	func(store *awsstorage.Builder) {
		store.Endpoint("http://localhost:9001")
		store.Region("us-east-1")
	},
)
```

Credentials come from the default chain, so export `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY` for the service, or pass a provider with
`Builder.Credentials`. Cloud services that offer an S3 API, such as Cloudflare R2,
follow the same pattern with their own endpoint and region. Check which parts
of the protocol they support before relying on them: signed uploads that bind a
content type and length depend on the service honoring signed headers.

### Using the native S3 client

For operations outside the portable contract, `driver.Client()` returns the
`*s3.Client` created or wrapped by the driver. For example, with `s3` imported
above, inspect the bucket without changing objects:

```go
_, err := driver.Client().HeadBucket(ctx, &s3.HeadBucketInput{
	Bucket: &bucket,
})
```

Here `bucket` is the same string used at construction. A nil error confirms
bucket access; it does not verify every object permission. Native calls bypass
the storage facade's validation, prefix mapping, events, and telemetry. For
large streams, use this client with the AWS SDK's upload manager rather than
passing an unbounded non-seekable stream to `Put`. `NewFrom` keeps ownership
with you; the storage driver never closes the client.

### Running locally

Install Docker with Compose/BuildKit and the AWS CLI. The original pinned
`minio/minio` image is no longer served by Docker Hub. For disposable local
storage, build the same MinIO release from its
[official release artifacts](https://github.com/minio/minio/releases/tag/RELEASE.2025-09-07T16-13-09Z)
instead of substituting an unverified mirror. This example supports Linux
amd64/arm64 containers, not a production MinIO deployment.

Save this `Dockerfile` in a new empty development directory. It pins the base
image and verifies the selected upstream binary's SHA-256 checksum. A mismatch
or unsupported architecture must fail the build; do not remove the check:

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

Beside it, save this `compose.yaml`, not over an existing application
configuration. These public example credentials are for disposable local data
only; keep the service bound to loopback:

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

```sh
docker compose up -d --build
export AWS_ACCESS_KEY_ID=localstorage
export AWS_SECRET_ACCESS_KEY=localstorage-password
export AWS_DEFAULT_REGION=us-east-1
```

Create the bucket after MinIO starts. If the endpoint is not ready, retry this
command; if the bucket already exists, reuse it:

```sh
aws --endpoint-url http://localhost:9001 s3 mb s3://dev
```

Use the S3-compatible constructor above for bucket `dev`, endpoint
`http://localhost:9001`, and region `us-east-1`. Write and read a small object
through `files` to verify its bytes, then inspect it with
`aws --endpoint-url http://localhost:9001 s3 ls s3://dev`. For signing, the
endpoint must also be reachable from the client that uses the URL.
Run `docker compose down` when finished; this disposable configuration does
not preserve its objects after the container is removed.

For every function and option, see [the `storage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage),
[`memorystorage`](https://pkg.go.dev/arkoren.dev/core/service/storage/memorystorage),
[`filesystemstorage`](https://pkg.go.dev/arkoren.dev/driver/filesystem/filesystemstorage), and
[`awsstorage`](https://pkg.go.dev/arkoren.dev/driver/aws/awsstorage).
