---
title: Events and Telemetry
nav: Events & Telemetry
description: Observe successful object changes, interpret facade spans and metrics, and preserve storage privacy boundaries.
order: 50
summary:
  - Connect a shared event bus and telemetry handle with a stable store name.
  - React to successful writes and deletions without assuming durable event delivery.
  - Interpret operation timing, page observations, metrics, and error classifications.
  - Test facade observations and keep private object data out of diagnostics.
---

# Events and Telemetry

Deleting an image can require thumbnail cleanup, while a slow upload requires
timing evidence. Storage exposes these as different observations: **events**
announce successful facade writes and deletions; **telemetry** measures facade
operations. Neither is a durable delivery receipt or a record of every native
backend change.

Examples use `storage` from `arkoren.dev/core/service/storage`, your
[configured driver](drivers.md), and an existing application `app`.

## Connecting Observers

Pass the application's bus and telemetry when constructing the facade:

```go
files := storage.New(driver, func(store *storage.Builder) {
	store.Events(app.Events())
	store.Telemetry(app.Telemetry())
	store.Name("uploads")
})
```

The fixed store name labels events and telemetry; omitted or empty names report
`default`. A name does not select a bucket or provide tenant isolation.
Use a small stable set of names, not user identifiers.

Events require an explicit bus. Without explicit telemetry, each operation uses
the [shared handle](../observability/telemetry.md#shared-handle), initially a
no-op. Observe through the facade: direct driver or native-client calls bypass
its instrumentation and notifications.

## Object Events

When your application needs to react to storage changes, subscribe to the typed
topics on the same bus passed to `Builder.Events`:

| Topic | Payload | Successful Observation |
| --- | --- | --- |
| `storage.ObjectWritten` | `storage.ObjectWrittenEvent` | `Put` returns metadata after storing an object. |
| `storage.ObjectDeleted` | `storage.ObjectDeletedEvent` | `Delete` succeeds, including for an absent object. |

The topic names are `storage.object.written` and `storage.object.deleted`.
Written payloads contain `Store` and `Object` metadata: key, byte size, and
modification time. Deleted payloads contain `Store` and `Key`. They contain no
object bytes or signed URLs, but keys can still identify private documents.

For thumbnail cleanup, import standard `context`. Here `thumbnails` is your
existing service with `Delete(context.Context, string) error`, accepting the
source image key:

```go
unsubscribe, err := app.Events().Listen(storage.ObjectDeleted,
	func(ctx context.Context, payload storage.ObjectDeletedEvent) error {
		if payload.Store != "uploads" {
			return nil
		}
		return thumbnails.Delete(ctx, payload.Key)
	})
```

Check the registration error and retain `unsubscribe` until the listening
component stops; call it during that component's cleanup. See the
[event bus guide](../events/overview.md) for listener ownership.

Listeners run synchronously before the storage call returns, on its goroutine
and with its context. Keep them short; hand slow work to a queue when needed.
An error or panic is logged by the bus and stops subsequent listeners for that
publication, but never changes the completed storage result. Notifications are
not a substitute for durable work: arrange durable reconciliation when missing
cleanup would leave important state inconsistent.

`PutBytes`, `PutFile`, and `PutFileAs` reuse `Put` and publish the same write
event. Failed calls publish nothing, even if replacement succeeded before a
metadata lookup failed. `Delete` events do not prove the object existed.
Concurrent writes can publish in a different order from backend replacement;
do not reconstruct the current object from event order.

Reads, listings, and signing publish no object event. A client using a
[signed PUT](signed-urls.md#direct-uploads) writes directly to the provider and
also publishes none.

## Operation Timing

Each facade call records one client span named `storage {operation}` and two
metrics. Operations are `open`, `put`, `stat`, `delete`, `list`, and `sign`.
Upload helpers reuse `put`; download helpers reuse `open`; `Sign` and `SignURL`
reuse `sign` rather than adding helper spans.

`open` ends when the object is acquired, not after its contents are consumed or
closed. Later download read/write/close failures belong to the caller's HTTP
operation, not the completed storage span. `put` includes consuming the source
and backend work, so a slow source can explain a long write duration. `sign`
measures URL creation, not the client's later transfer.

[`Walk`](overview.md#listing-objects) records one `list` per requested page.
Construction records nothing, and there is no separate traversal instrument.
Early stopping avoids observations for pages never requested; S3 read-ahead
inside one driver page remains part of that single facade operation.

The counter `arkoren.storage.operations` counts calls. The histogram
`arkoren.storage.operation.duration` records seconds, with buckets from 0.001
to 60 seconds. Both label operation, store, and failure class when present:

| Attribute | Meaning |
| --- | --- |
| `arkoren.storage.operation` | The facade operation. |
| `arkoren.storage.store` | Configured name or `default`. |
| `error.type` | Error class on failure. |
| `arkoren.storage.object.size` | Successful write size in bytes; span only. |

See [Instrumented Components](../observability/instrumented-components.md#storage)
for the shared instrument catalog and [Telemetry](../observability/telemetry.md)
for exporter setup.

## Failure Classification

`Open` or `Stat` returning `storage.ErrNotFound` is an ordinary missing-object
outcome: the span is not marked failed and no `error.type` is set. Other errors
mark the span failed and report their class, not their text.

Known classes include `storage.ErrInvalidKey`, `storage.ErrUnsupported`,
`storage.ErrInvalidTTL`, and `storage.ErrInvalidSignOptions`. Cancellation uses
the context error class; other errors use their Go type. A permission failure
is not silently reclassified as absence.

If development uses memory storage and intentionally falls back from signing,
expect `sign` failures classified as `storage.ErrUnsupported`. Keep this known
capability difference separate from operational alerts; do not hide unexpected
provider failures under the same fallback.

## Privacy Boundaries

Automatic storage telemetry excludes keys, prefixes, cursors, signed URLs,
payloads, and raw error text. Correlate through a suitably chosen application
identifier and trace ID rather than adding a private key to every span. Event
payloads include keys for listeners; review destinations before forwarding them.

Facade exclusions do not sanitize native SDK middleware, credential providers,
endpoints, or returned errors. If you add AWS `otelaws` instrumentation to a
client passed through [Native Access](drivers.md#native-access), SDK request
spans can nest under facade spans, but follow their own data policies. Review
native attributes and HTTP/body logging separately; never record signed URLs
or credential-bearing endpoints in ordinary diagnostics.

## Testing Observations

For a test with `t *testing.T`, import `telemetrytest` from
`arkoren.dev/core/service/telemetry/telemetrytest`, `telemetry` from
`arkoren.dev/core/service/telemetry`, and `memorystorage` from
`arkoren.dev/core/service/storage/memorystorage`. Construct an isolated store:

```go
fake := telemetrytest.New()
files := storage.New(memorystorage.New(), func(store *storage.Builder) {
	store.Telemetry(fake.Telemetry(t))
	store.Name("uploads")
})
```

Write a small object and assert the observation rather than exporting it:

```go
if _, err := files.PutBytes(t.Context(), "reports/a.txt", []byte("ready")); err != nil {
	t.Fatal(err)
}
fake.AssertSpan(t, "storage put",
	telemetry.String("arkoren.storage.store", "uploads"),
)
fake.AssertCounter(t, "arkoren.storage.operations", 1,
	telemetry.String("arkoren.storage.operation", "put"),
)
```

This checks facade reporting, not backend durability or client transfer success.
For events, attach a real in-process bus, capture typed payloads, and assert
your listener's effect; include failure and absent-delete paths when relevant.

For every setting, see the
[`storage` API](https://pkg.go.dev/arkoren.dev/core/service/storage),
[`event` API](https://pkg.go.dev/arkoren.dev/core/event), and
[`telemetry` API](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
