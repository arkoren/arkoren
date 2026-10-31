---
title: File Storage Events and Telemetry
nav: Events & Telemetry
description: React to objects written and deleted with events, see storage operations as spans and duration metrics, and what is never recorded.
order: 40
---

# File Storage Events and Telemetry

## Introduction

When an image is deleted, its cached thumbnail may need cleanup; when an upload
is slow, you need to see where the time went. The storage facade reports those
changes and timings in two ways. **Events** announce
changes that your code may want to act on: an object was written, an object was
deleted. **Telemetry** measures the operations themselves, with a span and a
duration for each open, put, stat, delete, list, and sign, so you can see which
calls are slow and which fail.

Events need an explicit bus. Telemetry uses an explicit service or the
[process default](../observability/telemetry.md#the-process-default), initially
a no-op. You can pass both when you create the facade, and
neither can change the outcome of an operation: a failing listener is logged,
and telemetry only observes. Telemetry omits object keys, since they often hold
a user's identifier or a document's name. Event payloads do include keys so your
listeners can act on the object; choose carefully where you forward them.
[What is recorded](#what-is-recorded-and-what-is-not)
lists exactly what is.

To enable both, pass them to `storage.New`:

Here `storage` is `arkoren.dev/core/service/storage`, `driver` is your
[configured storage driver](drivers.md), and `app` is your existing
[application](../application/overview.md).

```go
files := storage.New(driver, func(store *storage.Builder) {
	store.Events(app.Events())
	store.Telemetry(app.Telemetry())
	store.Name("uploads")
})
```

The name labels this store in both. Give each store a short, fixed name. A
store without one reports itself as `default`.

## Events

The facade publishes two events, and they are the same for every driver:

| Event | Published | Name |
| --- | --- | --- |
| `storage.ObjectWritten` | after `Put` stored an object | `storage.object.written` |
| `storage.ObjectDeleted` | after `Delete` succeeded | `storage.object.deleted` |

`ObjectWritten` carries a `storage.ObjectWrittenEvent` with the `Store` name and
the stored object's `ObjectInfo`: its key, size, and modification time.
`ObjectDeleted` carries a `storage.ObjectDeletedEvent` with the `Store` name and
the `Key`. They are published for writes made through `PutBytes`, `PutFile`, and
`PutFileAs` too, since those call `Put`.

An event means the operation succeeded. A failed `Put`, a key that is
rejected as invalid, reads, listings, and signed URLs publish nothing.
`Delete` publishes even when the object was already gone, because deleting a
missing object succeeds. A write that a client makes straight to the backend
with a [signed URL](signed-urls.md) never passes through the facade, so it
publishes nothing either.

Listening is the usual event bus call, covered in [Events](../events/overview.md).
For example, to drop a cached thumbnail when its source image is removed, use
your application's `thumbnails` service:

```go
_, err := app.Events().Listen(storage.ObjectDeleted,
	func(ctx context.Context, e storage.ObjectDeletedEvent) error {
		if e.Store != "uploads" {
			return nil
		}

		return thumbnails.Delete(ctx, e.Key)
	})
```

A few details matter when you write listeners:

- A listener runs synchronously, on the goroutine that called `Put` or
  `Delete`, before it returns. Keep it short, and hand slow work to a queue.
- The listener receives the context of the call. If that context is canceled,
  so is the listener's work.
- A listener's error or panic is logged by the bus and never changes the
  result of the storage call: the object is already written. A failing
  listener does stop the listeners after it, so keep the important ones
  independent and make each tolerate failure.
- Compare `Store` when you share one listener between several stores.

The event describes one request. Two concurrent writes to a key publish two
events, but publication happens after each write. Do not use event order to
infer the order in which the backend replaced the objects.

## Telemetry

With telemetry enabled, each facade operation records one client span named
`storage {operation}`, where the operation is one of `open`, `put`, `stat`,
`delete`, `list`, or `sign`. The helpers use these: `PutBytes`, `PutFile`, and
`PutFileAs` record a `put`, `Download` and `Inline` record an `open`, and both
`Sign` and `SignURL` record a `sign`.

[`Walk`](overview.md#listing-files) calls `List` for each needed page, recording
one `list` operation per page request. Creating a walk emits nothing; consuming
it adds no separate traversal span or metric. Stopping early prevents telemetry
for pages never requested. S3 read-ahead within one driver page remains part of
that page's single facade operation.

### Spans

| Attribute | Value |
| --- | --- |
| `arkoren.storage.operation` | The operation name above |
| `arkoren.storage.store` | The name from `Builder.Name`, or `default` |
| `arkoren.storage.object.size` | Bytes written, on a successful `put`; span only |
| `error.type` | Set on failure |

The duration of `open` is the time to the response, not the time to read the
body, and the duration of `put` is the time to consume the reader. A put of a
large file from a slow client therefore measures the slow client as well as
storage.

### Metrics

Two metrics are recorded for every call. The counter `arkoren.storage.operations`
counts operations, and the histogram `arkoren.storage.operation.duration`, in
seconds, records how long they took. Both use the keys `arkoren.storage.operation`,
`arkoren.storage.store`, and `error.type`. The duration buckets run from a
millisecond to a minute, because a local `stat` and a large upload differ by
orders of magnitude. [Instrumented components](../observability/instrumented-components.md#storage)
lists them next to the other components, and
[Telemetry](../observability/telemetry.md) explains how to export them.

### Failures

A missing object is an answer, not a failure: `Open` or `Stat` returning
`storage.ErrNotFound` leaves the span unmarked and sets no `error.type`. Any
other error sets the span status to error and `error.type` to the class of the
error, for example `storage.ErrInvalidKey` or `storage.ErrUnsupported`, a
context error for a canceled call, or else the Go type of the error. The text
of the error is never recorded, since a driver error can quote a key.

A signing request on a driver that cannot sign shows up as a failure of
`sign` with `storage.ErrUnsupported`. If your application falls back to
proxying uploads there, as described in [Signed URLs](signed-urls.md#drivers-that-cannot-sign),
expect these errors in development and exclude them from alerts if they would
be noise.

### S3 requests

The facade span covers one storage call and says nothing about the HTTP
requests underneath it. To see those too, build the S3 client with the AWS
`otelaws` middleware and pass it to `awsstorage.NewFrom`; the SDK spans then
nest under the facade span. Those spans come from the AWS SDK, not from Arkoren,
and follow its own rules about attributes.

Review that SDK instrumentation and any native HTTP/body logging separately.
The facade's exclusions do not sanitize a supplied client, its middleware, or
returned provider errors. Never add signed URLs, credential-bearing endpoints,
payloads, or raw native error text to ordinary diagnostics.

## What is recorded and what is not

Telemetry records the operation, the store name, the size of a successful put, and the
class of an error. Never recorded: object keys, prefixes, list cursors, and
signed URLs. A signed URL is a credential for as long as it lives, and a key
often names a person or a document. Because the span has no keys, you cannot
find the trace of one file by its name. Correlate through your own identifier
instead, for example by logging the document ID with the trace ID of the
request.

## Testing

Import `telemetrytest` from `arkoren.dev/core/service/telemetry/telemetrytest`,
`telemetry` from `arkoren.dev/core/service/telemetry`, and `memorystorage` from
`arkoren.dev/core/service/storage/memorystorage` for the fragment below.

To assert what your code reports, use the in-memory telemetry fake from
`telemetrytest`. It records spans and metrics instead of exporting them. In a
test with `t *testing.T`, give the facade its telemetry and assert one write:

```go
fake := telemetrytest.New()
files := storage.New(memorystorage.New(), func(store *storage.Builder) {
	store.Telemetry(fake.Telemetry(t))
	store.Name("uploads")
})

if _, err := files.PutBytes(t.Context(), "avatars/1.png", []byte("png")); err != nil {
	t.Fatal(err)
}

fake.AssertSpan(t, "storage put",
	telemetry.String("arkoren.storage.store", "uploads"),
)
fake.AssertCounter(t, "arkoren.storage.operations", 1,
	telemetry.String("arkoren.storage.operation", "put"),
)
```

Events need no fake: create an `event.New()` bus, listen on the topics you care
about, pass it with `Builder.Events`, and assert on what the listener collected.
See [Testing](../testing/overview.md).

For every function and option, see [the `storage` API reference](https://pkg.go.dev/arkoren.dev/core/service/storage),
[the `event` API reference](https://pkg.go.dev/arkoren.dev/core/event), and
[the `telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
