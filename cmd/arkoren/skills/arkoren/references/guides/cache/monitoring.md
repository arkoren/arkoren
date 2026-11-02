---
title: Cache Monitoring
nav: Monitoring
description: Interpret cache spans, hit ratios, errors, and privacy limits using the facade's telemetry.
order: 60
summary:
  - Connect a telemetry handle and fixed store label without changing key identity.
  - Interpret operation latency, hit ratios, error classes, and untraced calls.
  - Preserve privacy boundaries and assert cache signals with an isolated test recorder.
---

# Cache Monitoring

A slow request can come from a cache miss, a waiting refresh, or a failing cache
backend. Facade telemetry distinguishes operation latency, lookup outcomes, and
error classes without recording entry names or values. The cache publishes no
events; use returned errors for application decisions and telemetry for
observation.

## Connecting Telemetry

With an existing cache `driver` and a configured `*telemetry.Telemetry` named
`signals`, select the handle when constructing the facade. Import `cache` from
`arkoren.dev/core/service/cache`; telemetry handles come from
`arkoren.dev/core/service/telemetry`:

```go
store := cache.New(driver, func(store *cache.Builder) {
	store.Telemetry(signals)
	store.Name("catalog")
})
```

Use a short fixed name such as `catalog`, not a tenant ID, token, or per-request
value. `Name` controls the `arkoren.cache.store` label only: it does not prefix
keys or isolate storage. An empty name reports `default`.

Without an explicit handle, each operation resolves the
[process default](../observability/telemetry.md#shared-handle). It is a
no-op until configured; installing the default after cache construction still
affects later operations. `Telemetry(nil)` also follows that default. Provider
connections and lifecycle remain in [Cache Drivers](drivers.md).

## Reading Signals

Each facade operation records a client span named `cache {operation}`, one
`arkoren.cache.operations` counter increment, and one
`arkoren.cache.operation.duration` histogram observation in seconds. Operation
and store labels identify the call, for example `get` on `catalog`.

The span's `arkoren.cache.hit` attribute describes a known lookup outcome:

| Operation | Hit Meaning |
| --- | --- |
| `get`, `pull`, `ttl` | Whether an unexpired entry was found. A subsequent decoding error is still a hit. |
| `has` | The returned existence result. |
| `remember` | Whether the result came from cache rather than running the loader, including a hit after waiting for another caller. |

For these lookups, `true` means found and `false` means missing or recomputed.
A read that fails before determining lookup outcome has no span hit attribute.
Writes and lock acquisitions have no lookup hit state either. The operations
counter uses string labels `"true"`, `"false"`, or `""` for unknown/inapplicable
hit state; the duration histogram has no hit label.

Compute a hit ratio for one operation and store as its `"true"` count divided by
the sum of its `"true"` and `"false"` counts. Exclude unknown states and writes;
combining all operations in the denominator understates the lookup hit ratio.
A `Remember` hit means reuse, not necessarily a request that never waited.

Errors wrapping `cache.ErrNotFound` are misses, not telemetry failures: they do
not set error status or `error.type`. Other failures set the span error status
and `error.type` using an error class, never the error text. For example, invalid
TTL declarations report `cache.ErrInvalidTTL`. A rejected guarded write is
`false, nil`, not a read miss or an error; unsuccessful `Add` and contended
`TryLock` likewise are ordinary non-error results.

For the complete attribute and metric catalog, see
[instrumented cache components](../observability/instrumented-components.md#cache).

## Operation Boundaries

`Remember` produces one span around the entire operation, not child facade spans
for each internal read, acquisition, or write. Its duration can include waiting,
loading, storage, and the separate cleanup budget described in
[Remembering Values](overview.md#remembering-values).

`Lock` and `TryLock` each emit one acquisition span (`cache lock` and
`cache try_lock`). `Lock` includes time spent polling for ownership. `Lease.Unlock`
and `Lease.Extend` emit no facade operation spans; report cleanup or renewal
failures explicitly if your application needs that visibility. Losing ownership
returns `cache.ErrLockLost`, not a published event.

`Driver` and `DriverAs` are untraced accessors. Calls through a raw driver or
native Redis client do not emit facade telemetry. Native instrumentation, if
configured independently, has its own measurement and privacy policy.

One operation span is not a backend command count or proof of exactly-once
effects. A facade operation can make several driver calls, and native clients
can retry transport failures. A lost response can leave a mutation's outcome
unknown even though the facade span reports an error.

## Privacy Boundaries

Facade spans and metrics never record cache keys, lock names, values, or error
messages. Error classes are used because backend error text can contain private
entry names. Store names remain caller-supplied telemetry labels, so keep them
fixed and non-sensitive.

This protection does not sanitize your application logs or raw/native client
instrumentation. Owned keys and namespaces redact formatting, but explicit
`Name()` results, raw arguments, and `Match` or `Entry` descriptors expose their
contents. Follow [identity inspection rules](keys.md#inspecting-identities),
and do not attach sensitive names to your own spans to compensate for their
absence from facade telemetry.

## Testing Signals

To test instrumentation without exporting it, use the fake from
`arkoren.dev/core/service/telemetry/telemetrytest`. Inside an existing test with
`t *testing.T`, configure an isolated handle. Import `memorycache` from
`arkoren.dev/core/service/cache/memorycache`:

```go
fake := telemetrytest.New()
store := cache.New(memorycache.New(), func(store *cache.Builder) {
	store.Telemetry(fake.Telemetry(t))
})
```

With standard-library `errors`, confirm absence as part of the test before
asserting its signal:

```go
_, err := store.Get(t.Context(), cache.NewKey[string]("catalog:missing"))
if !errors.Is(err, cache.ErrNotFound) {
	t.Fatalf("Get error = %v, want ErrNotFound", err)
}
```

The fresh memory driver guarantees this read misses. Assert one facade span
with a false hit attribute, using `telemetry` from
`arkoren.dev/core/service/telemetry`:

```go
fake.AssertSpanCount(t, 1)
fake.AssertSpan(t, "cache get", telemetry.Bool("arkoren.cache.hit", false))
fake.AssertCounter(t, "arkoren.cache.operations", 1,
	telemetry.String("arkoren.cache.operation", "get"),
	telemetry.String("arkoren.cache.hit", "false"))
```

These assertions verify the facade signal, not Redis connectivity, persistence,
or exactly-once execution. The fake also supports histogram and snapshot
inspection; see [telemetry testing](../testing/fakes.md).

For every instrumentation setting, see the [`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
