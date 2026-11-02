---
title: Cache Overview
nav: Overview
description: Reuse typed values, compute missing results, and choose atomic operations for concurrent callers.
order: 10
summary:
  - Store and read typed JSON copies with explicit expiry and miss behavior.
  - Rebuild missing values with Remember while accounting for lease and cleanup limits.
  - Consume, claim, or conditionally replace entries using atomic cache operations.
---

# Cache Overview

A cache lets requests reuse a report or an upstream response instead of rebuilding
it each time. Treat cached values as replaceable copies: an entry can disappear
through expiry, eviction, deletion, or backend loss. No expiry means no scheduled
expiration, not guaranteed retention.

The `*cache.Cache` facade encodes Go values as JSON; its driver stores the bytes.
These examples assume an existing facade named `store` and an operation context
`ctx`. Choose and configure that store in [Cache Drivers](drivers.md).
Use `cache` from `arkoren.dev/core/service/cache`, plus standard-library `errors`
and `time`. Focused snippets keep error results visible but omit routine error
propagation; your application must handle those errors.

## Reading Values

Suppose your application builds a monthly sales report. Give its cached copy a
small, JSON-compatible shape:

```go
type Report struct {
	Month        string `json:"month"`
	Orders       int64  `json:"orders"`
	RevenueCents int64  `json:"revenue_cents"`
}
```

A typed key supplies the exact storage name, value type, and write lifetime.
Declare a ten-minute copy of March's report:

```go
key := cache.NewKey[Report]("reports.v1:2026-03", func(entry *cache.KeyBuilder) {
	entry.TTL(10 * time.Minute)
})
```

Creating this definition performs no I/O. [Cache Keys](keys.md) explains literal
identities, namespaces, and reusable expiry policies. Sharing a key definition
helps readers and writers agree; its Go type does not isolate backend entries.

To publish a freshly built report, write it through the facade:

```go
err := store.Put(ctx, key, Report{
	Month:        "2026-03",
	Orders:       318,
	RevenueCents: 924500,
})
```

`Put` replaces both the value and its expiry. A key without a TTL writes without
expiry. Values must round-trip through `encoding/json/v2`; use exported fields
and JSON-compatible members, not channels or functions. Keep large blobs in
[file storage](../file-storage/overview.md), rather than a cache entry.

Read the report and distinguish ordinary absence from a storage or decoding
failure:

```go
report, err := store.Get(ctx, key)
if errors.Is(err, cache.ErrNotFound) {
	// Load a replacement from the application's source of truth.
}
```

On success, `report` is a `Report`. A missing, expired, or evicted entry returns
an error wrapping `cache.ErrNotFound`. An existing value with incompatible JSON
returns a decoding error and remains stored. Writes capture a separate JSON
representation; reads decode a separate value. Mutating either your input or a
decoded result does not modify the stored copy.

To inspect expiry, use `remaining, err := store.TTL(ctx, key)`: zero means an
existing entry has no expiry; absence returns `cache.ErrNotFound`. This differs
from `key.TTL()`, which reports the declared write policy. Reads do not renew
expiry. `Has` checks existence without decoding, but cannot guard a later write.
`Delete` removes an entry and succeeds if it is already absent.

## Remembering Values

When every miss should rebuild the report, `Remember` combines lookup and
computation. Here `buildReport` is your application's loader returning
`(Report, error)`:

```go
report, err = store.Remember(ctx, key, func() (Report, error) {
	return buildReport(ctx, "2026-03")
})
```

A hit returns the decoded report without calling the loader. A miss runs the
loader and stores its result using the key's TTL. Undecodable stored JSON also
triggers rebuilding and replacement. A schema or meaning change that still
decodes successfully does not; version the physical key name or invalidate it.

Loader failure stores nothing and returns the error. If the loader succeeds but
encoding or storing its result fails, `Remember` returns that computed value
together with the error. Read or lock-acquisition failures return before the
loader runs, so an arbitrary error does not imply a usable value. Choose an
explicit fallback policy if the application must serve while the cache is down.
The computed result remains application-owned and is returned without cloning;
successful storage captures its JSON representation.

Concurrent misses sharing a backend coordinate through a ten-second
[lease](locks.md). After acquiring it, each caller reads again before computing;
hits never acquire the lease. Waiting ends when `ctx` ends. Independent memory
drivers do not coordinate, even if their keys have identical names.

> [!WARNING]
> `Remember` does not renew its lease or guard the final write. A loader that
> outlasts ten seconds can overlap another loader and overwrite a newer result.
> Keep computation idempotent; move long refresh work to a schedule instead of
> relying on this lease for correctness.

After acquisition, release is best effort with a separate ten-second cleanup
timeout. Cleanup retains context values but ignores request cancellation and
deadlines; release errors are ignored because the lease expires. Return can
therefore occur after the request deadline. The cleanup timeout bounds I/O only
when the driver honors its context, and it does not bound the loader.

## Consuming Values

A one-time download grant must not be returned to two concurrent requests.
`Pull` reads and removes an entry in one atomic driver operation. With an
existing `cache.Key[string]` named `grantKey`, consume it once:

```go
grant, err := store.Pull(ctx, grantKey)
if errors.Is(err, cache.ErrNotFound) {
	// Reject the missing, expired, or already consumed grant.
}
```

For one stored entry, one concurrent caller receives its bytes; later pulls
observe absence. Removal happens before JSON decoding, so invalid JSON still
consumes the entry. A lost network response can also leave the entry consumed
without delivering the value to your application. This atomic consumption is
not a transaction with the action authorized by the grant.

## Claiming Entries

To reserve a short-lived refresh marker, use `Add` rather than a separate `Has`
and `Put`. Here `marker` is an existing `cache.Key[bool]` with a bounded TTL:

```go
claimed, err := store.Add(ctx, marker, true)
if err != nil {
	return err
}
if !claimed {
	return nil // Another caller already holds the marker.
}
```

`true, nil` means this call stored the value; `false, nil` means an existing
unexpired entry and its TTL were left untouched. This claims an entry, not
completion of subsequent work. A crash after claiming leaves the marker until
expiry or deletion. For work that must not be lost, use a
[queue job](../queues/overview.md) with an idempotent handler instead.

## Guarded Writes

If another caller can retire or replace a report while an edit is pending,
check the observed value and write the replacement atomically. `CompareAndPut`
uses an exact raw-byte guard, not decoded JSON equality. Read those bytes before
preparing your replacement:

```go
raw, err := store.Driver().Get(ctx, key.Name())
if err != nil {
	return err
}
```

Raw access bypasses facade validation and telemetry. Do not decode and re-encode
the guard: the representation could change. Once your application has prepared a
`Report` named `replacement`, submit it only while those bytes still match:

```go
applied, err := store.CompareAndPut(ctx,
	cache.Match{Key: key.Name(), Value: raw}, key, replacement)
if err != nil {
	return err // A transport failure can leave the outcome unknown.
}
if !applied {
	return nil // Discard or reload the edit; never fall back to Put.
}
```

On success, the destination value and TTL are replaced together. A missing,
expired, or byte-different guard returns `false, nil` without changing either
entry or its TTL. The guard can name a different entry; a distinct guard and its
expiry remain untouched. An existing empty value can match empty bytes, but an
absent guard never matches. Invalid destination definitions fail before I/O,
including on a mismatch.

Unlike a lease, this check protects the cache write itself. It is not a monotonic
version: deleting and recreating identical guard bytes allows an old update to
match again (the **ABA problem**). Use a nonrepeating application version when
that distinction matters. Do not restore retired security records. Before using
guards for session retirement, follow the
[session writer rollout requirements](../http-capabilities/sessions.md#save-changes)
so older unconditional writers cannot bypass rejection.

`Match` and raw `Entry` descriptors expose names and bytes without redaction.
Their slices are borrowed during the call, never modified or retained afterward;
do not mutate them concurrently or log sensitive contents. A custom driver must
perform comparison and writing atomically or return `cache.ErrUnsupported`.
Name-transforming wrappers must transform both guard and destination. Network
atomicity is not exactly-once execution or durability; Redis specifically avoids
replaying a possibly executed guarded write after an uncertain response.

## Shared Infrastructure

Use [counters](counters.md) for atomic integer updates and [locks](locks.md) to
reduce duplicate work. [Sessions](../http-capabilities/sessions.md) and
[rate limiting](../abuse-prevention/rate-limiting.md) also depend on the cache:
losing their entries logs visitors out or resets request allowances. Independent
replica-local caches keep independent sessions and allowances.

Choose a separate backend or Redis database when state needs independent sizing
or clearing. A facade name only labels telemetry, and namespaces do not provide
bulk-clear or isolation boundaries. Do not share a session cache with entries
your application intends to clear indiscriminately. [Monitoring](monitoring.md)
explains operation spans, hit ratios, and privacy boundaries.

For tests, the [memory driver](drivers.md#memory) needs no server. Its clock works
with [`testing/synctest`](../testing/time.md), so expiry tests need not sleep in
real time. Custom drivers can run the shared
[conformance suite](../testing/conformance-suites.md), `cachetest.Run`.

For every function and option, see the [`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
