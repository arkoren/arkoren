# Object storage

Use `storage.Storage` to write, open, list, and delete complete objects through
your chosen driver. The facade preserves driver error identities and adds
optional telemetry and mutation notifications. See the
[file storage guide](https://arkoren.dev/docs/guides/file-storage/overview) for
application wiring and driver selection.

## Write and reopen a report

This focused example uses an existing `ctx context.Context` and imports `errors`,
`fmt`, `io`, `arkoren.dev/core/service/storage`, and
`arkoren.dev/core/service/storage/memorystorage`. Memory storage needs no lifecycle
calls and loses its objects when the process exits.

```go
files := storage.New(memorystorage.New())
_, err := files.PutBytes(ctx, "reports/a.txt", []byte("report"))

object, err := files.Open(ctx, "reports/a.txt")
contents, err := io.ReadAll(object.Contents)
err = errors.Join(err, object.Contents.Close())
fmt.Println(string(contents)) // report
```

You own an opened body and must close it, including after a read failure.
`Put` borrows its reader and never closes it. The facade does not start or stop
a native driver; keep that lifecycle with the code that created the driver.

## Keep keys literal and portable

Keys are UTF-8 relative paths of at most 1024 bytes. They are never cleaned,
renamed, case-folded, or Unicode-normalized. Validation rejects traversal, empty
segments, control characters, Win32 filename metacharacters, trailing dots or
spaces, and reserved device names. This includes `NUL`, `CON`, `COM1`–`COM9`,
`LPT1`–`LPT9`, superscript port digits `¹`, `²`, `³`, and console handles
`CONIN$`/`CONOUT$`. Device basenames with extensions such as `NUL.txt` are excluded
for Windows versions where they still alias devices, even where Windows 11
permits them. Ordinary names such as `COM0`, `COM10`, and `console.txt` remain
valid. Filesystem-specific path-length and case-sensitivity limits still apply.

If an existing backend contains a newly rejected name, migrate that object
deliberately through the native backend before changing application references.
The portable API cannot access that name, and it never silently renames data.
The same restriction applies to configured driver prefixes validated with
`ValidatePrefix`.

## Serve downloads without hiding failures

Authorize access before calling `Download` or `Inline`. Seekable bodies retain
Go's native range, multipart-range, HEAD, and conditional request handling.
Non-seekable bodies stream sequentially with HEAD support. `Inline` keeps its
sandbox and safe-type restrictions.

The helpers always close opened bodies. They return observed read, seek, write,
short-write, and close failures, joining cleanup errors with transfer errors.
An error can arrive after HTTP response commitment: return it through an
error-aware handler for observation, but never append a replacement problem
body or automatically retry the transfer. Read-only multipart upload sources
are closed best-effort; cleanup does not undo a successful storage write.

## Cancellation and signed URLs

An already canceled context wins over invalid request input. Memory uploads
check cancellation between reads and before publication; listings check during
key scanning and metadata copying and return no partial page on cancellation.
A blocked reader, mutex acquisition, or synchronous sort cannot be forcibly
interrupted by these checks.

Signing keeps the Driver contract's validation order: context, key, TTL including
the backend maximum, method, bindings, then capability. The facade delegates
backend-sensitive checks together. Memory and filesystem drivers still reject
valid signing requests with `ErrUnsupported`. A signed URL is a bearer credential:
authorize its recipient and keep it out of ordinary logs and telemetry.

The [storage API](https://pkg.go.dev/arkoren.dev/core/service/storage) documents
each operation and ownership rule. The
[memory driver API](https://pkg.go.dev/arkoren.dev/core/service/storage/memorystorage)
documents its snapshots, cancellation limits, and pagination defaults.
