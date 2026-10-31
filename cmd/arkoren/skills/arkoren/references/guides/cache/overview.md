---
title: Cache Overview
nav: Overview
description: Create a cache, store and read typed values with expiry, compute them on a miss with Remember, and learn what relies on the cache.
order: 10
---

# Cache Overview

## Introduction

A cache keeps the result of work you do not want to repeat, such as a rendered
report or the response of a slow upstream API. It trades freshness for speed:
values can expire or be evicted, even when you stored them without an expiry.
Treat the cache as a copy
you can rebuild, never as the only place a value lives.

Arkoren's cache has two parts. The **facade**, `*cache.Cache`, is what your code
uses. It stores any value that survives `encoding/json/v2`, so you put a struct in
and get the same struct back. A **driver** stores the raw bytes: [memory](drivers.md#memory)
for tests and single-process applications, [Redis](drivers.md#redis) when several
processes must share the cache. Switching between them changes the line that
builds the facade, but also changes whether callers share state and whether
operations can fail because of the network.

Use [counters](counters.md) when callers need to update a shared integer rather
than replace a computed value. Use [locks](locks.md) when callers need to
coordinate work; these are expiring leases, not a guarantee against overlap.

Other parts of Arkoren use a cache too.
[Sessions](../http-capabilities/sessions.md) keep their data in it, and the
[rate limiting middleware](../abuse-prevention/rate-limiting.md) counts requests
in it. Both take a `*cache.Cache`, and both need a shared driver in production,
or each replica of your service keeps its own sessions and its own counts.

## Creating a cache

Run `go get arkoren.dev/core` in your application's Go module. For a
process-local cache, wrap the included memory driver with `cache.New`:

```go
import (
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

store := cache.New(memorycache.New())
```

This cache is ready to use without lifecycle hooks and loses its state on
process exit. Expiry works without starting the optional background janitor.
When replicas must share entries, use the [Redis driver](drivers.md#redis);
register its `Stop`, `Start`, and `Ping` with the
[application lifecycle](../application/lifecycle-and-ownership.md) before serving traffic.

## Creating keys

Focused snippets keep error results visible but omit routine error handling.
Handle those errors in your application; checks remain where failure behavior
is the lesson.

To store a profile, create a **key**: an immutable definition carrying its exact
storage name, value type, and write time to live (TTL). Share it between readers
and writers so they agree on the name and type. Creating a key does not contact
the cache, allocate storage, or change an existing entry's expiry.

First define the value type, then create a ten-minute key with `NewKey`.
These snippets use `cache` from
`arkoren.dev/core/service/cache` and the standard library `time` package:

```go
type Profile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

key := cache.NewKey[Profile]("profiles.v1.1", func(entry *cache.KeyBuilder) {
	entry.TTL(10 * time.Minute)
})
```

This produces a `cache.Key[Profile]` named exactly `profiles.v1.1`; no separator
is added. Without a callback, `NewKey` uses TTL zero, meaning no expiry. The
same key works with the memory or Redis facade, and reads infer the value type
from it. `Name()` reveals the physical name; ordinary formatting and structured
logging redact the owned `Key` and `Namespace` definitions. Unsupported or
mismatched `fmt` verbs, such as `%d` for a key, also render a redacted marker
rather than reflecting the private identity. This protection does not extend to
explicit `Name()` results, raw driver arguments, or raw `Match` and `Entry`
descriptors. Avoid logging those when they contain personal data or secrets.

Use stable, feature-owned names; version them when a schema or meaning change
requires separate entries. Names are literal: Arkoren does not split, escape,
trim, or normalize them. Punctuation and even an explicit empty name are valid
with `NewKey`. Differently typed keys with the same physical name address the
same entry; types do not enforce a backend schema or isolate data.

### Overriding a key's TTL

If writes to the same entry need a shorter lifetime, create another key with
the same exact name and a different TTL:

```go
brief := cache.NewKey[Profile]("profiles.v1.1", func(entry *cache.KeyBuilder) {
	entry.TTL(time.Minute)
})
```

`brief` still addresses `profiles.v1.1`, but writes through it use one minute
instead of ten. `Put`, `Add`, `CompareAndPut`, and a computing `Remember` use the key's TTL.
Reading with a different TTL does not renew or alter an entry.
`key.TTL()` reports the declared write policy; `store.TTL(ctx, key)` reports
the stored entry's remaining lifetime. Atomic counters have
[separate expiry rules](counters.md).

### Deriving independent variants

If you already have a key and need a different policy, use `Derive`.
It returns a new key without changing the original. For example, derive a
variant without expiry from the ten-minute key above:

```go
permanent := key.Derive(func(entry *cache.KeyBuilder) {
	entry.TTL(0)
})
```

`permanent` keeps `key`'s name with TTL zero. The original key keeps its
ten-minute TTL. Deriving a variant does not update stored entries; the new
policy applies on a later write.

### Checking definition errors

If names or TTLs come from configuration, inspect `Err()` before serving
requests. Check the published key after construction:

```go
if err := key.Err(); err != nil {
	return err
}
```

Zero-value keys wrap `cache.ErrInvalidKey`; an explicit empty name passed to
`NewKey` is valid. Negative TTLs wrap `cache.ErrInvalidTTL`. The first declaration
error remains
attached to descendants and derived variants even if a later callback repairs
a setting; construct a new valid definition instead. Facade operations reject
invalid definitions before accessing the driver, including on cache hits.
Construction callbacks run synchronously once, in order; a nil callback panics.

## Storing and reading values

To reuse a profile across requests, store it with `Put` and read it with `Get`.
Here `store` is the facade created above, `key` is the profile key from
[Creating keys](#creating-keys), `ctx` is your operation context, and `errors`
is the standard library package:

```go
err := store.Put(ctx, key, Profile{ID: 1, Name: "Ada"})

profile, err := store.Get(ctx, key)
if errors.Is(err, cache.ErrNotFound) {
	// never stored, expired, or evicted
}
```

On success, `profile` contains Ada's profile. A miss returns an error that wraps
`cache.ErrNotFound`. It is the normal way for a read to come back empty, so check
for it before treating an error as a failure. A value that is stored but cannot
be decoded as the key's type returns the decoding error instead.

Values go through JSON, so the type must round-trip through `encoding/json/v2`:
exported fields, no channels or functions. Keep values small. A cache is for
many reads of a modest value, not for blobs; keep those in
[file storage](../file-storage/overview.md).

`Put` encodes a separate stored representation and `Get` decodes a separate
value: mutating an input or a returned decoded value does not change the stored
entry. A freshly computed `Remember` result remains application-owned, as
described below.

`Has` reports
whether a key exists, `TTL` returns the time that remains (zero for an entry that
never expires), and `Delete` removes a key and succeeds when it is not there.

### Taking a value out

A password-reset token should be usable once, even if two requests arrive
together. Reading it and deleting it in separate calls would let both requests
see it. `Pull` reads and removes the value in one atomic operation. Here `token`
is your string token identifier:

```go
reset := cache.NewKey[string]("reset." + token)
code, err := store.Pull(ctx, reset)
```

`Pull` is one atomic step in the driver (Redis `GETDEL`, or one locked step in
memory), so when several callers pull the same key exactly one receives the
value and the others get `cache.ErrNotFound`. That makes it safe for one-time
tokens. The value is removed even when it cannot be decoded as the requested
type.

### Storing only when absent

Two requests may both try to claim the same piece of work. A separate check
followed by a write leaves room for both to succeed. `Add` combines them: it
stores a value only if the key is absent or expired and tells you whether this
caller claimed it. Here `id` is your string work identifier:

```go
welcome := cache.NewKey[bool]("welcome." + id, func(entry *cache.KeyBuilder) {
	entry.TTL(24 * time.Hour)
})
claimed, err := store.Add(ctx, welcome, true)
if !claimed {
	return nil // another request already claimed this work
}
```

This claims a key, not the completion of the work that follows. If the process
crashes after claiming it, later callers still skip the work until the key
expires. Do not use this alone for must-not-lose work such as sending a welcome
email; use a [queue job](../queues/overview.md) with an idempotent handler.

### Writing only while an observed value still matches

If another request can retire or replace an entry while your update is pending,
a separate read and write is not sufficient. `CompareAndPut` checks an existing
entry's exact bytes and writes the destination in one atomic operation.

The raw `Match` and `Entry` descriptors are not redacted definitions: they expose
literal names and byte values. Their slices are borrowed for the call, not
modified or retained afterwards; do not mutate them concurrently or log them as
though they were a redacted key.

Using the profile `key`, `Profile`, and `store` from above, read the raw guard
before submitting the replacement:

```go
raw, err := store.Driver().Get(ctx, key.Name())
applied, err := store.CompareAndPut(ctx,
	cache.Match{Key: key.Name(), Value: raw}, key, Profile{ID: 1, Name: "Grace"})
if !applied {
	return nil // The guard changed or disappeared; discard this edit.
}
```

On success, the profile and its TTL are replaced together. A missing, expired,
or byte-different guard returns `false, nil` and leaves the destination and its
TTL unchanged. The guard can name a different key; in that case its value and
TTL remain untouched. An existing empty value can match empty bytes, but an
absent entry never matches. The guard uses raw bytes from `Driver.Get`, which
bypasses facade telemetry; decoded and re-encoded JSON might have different bytes.

Unlike a [lease](locks.md), this protects the write at the resource itself.
It is not a monotonic version: deleting and restoring the same guard bytes can
authorize an old update again. Do not restore retired security records or fall
back to `Put` after rejection. A transport error can mean the write's outcome is
unknown; Redis does not replay a possibly executed guarded write automatically.
Custom drivers must implement the atomic comparison and write, or return
`cache.ErrUnsupported`; never substitute separate reads and writes. Wrappers
that transform names must transform both the guard and destination. Before
relying on this for session retirement, follow the
[session writer rollout requirements](../http-capabilities/sessions.md#when-changes-are-saved)
so an older unconditional writer cannot bypass the guard.

## Remembering a computed value

The most common use of a cache is to compute a value on a miss and reuse it
afterwards. `Remember` does both in one call. Here `Report` and `buildReport`
are your application's report type and loader:

```go
reportKey := cache.NewKey[Report]("reports.2026-03", func(entry *cache.KeyBuilder) {
	entry.TTL(time.Hour)
})
report, err := store.Remember(ctx, reportKey, func() (Report, error) {
	return buildReport(ctx, "2026-03")
})
```

If the key holds a value, `Remember` returns it and never calls the function.
Otherwise it calls the function, stores the result for the TTL, and returns it.
If the function returns an error, nothing is stored and the error is returned.
Use a key with TTL zero to store the result without an expiry.

If a stored value no longer decodes as the requested type, for example after
you changed the struct, `Remember` treats it
as a miss, recomputes, and overwrites it. This repairs incompatible JSON decode
failures, not schema or semantic changes that still decode successfully. Version
the physical names or invalidate entries for those changes. If the function
succeeds but storing the result fails, you
get the computed value and the storage error together; decide whether your
caller can use the value anyway.
The computed value is returned without cloning it. Successful storage captures
its JSON representation; it does not take ownership of the application's result.

A cache read or lock-acquisition failure returns before the function runs.
Do not assume that every error comes with a computed value; choose an explicit
fallback policy if the application must keep serving when the cache is down.

On a miss, `Remember` takes a [cache lock](locks.md)
named `remember:<completed-key>`, reads the key again, and runs the function only
if it is still missing. When a popular key expires, one request computes the
value and the others wait for it and read what it stored. This coordinates callers sharing
the same backend, not independent memory caches. A hit never takes the lock.
The lock is a ten-second lease, and waiting callers give up when their `ctx` ends.

After acquiring a lease, `Remember` attempts release with a separate ten-second
cleanup timeout. Cleanup keeps the request's context values but ignores its
cancellation and deadline. Release errors are ignored; lease expiry is the
fallback. This can delay return beyond the request deadline, and the timeout
bounds cleanup only if the backend honors its context. It does not interrupt a
noncooperative driver or bound your loader's execution.

> [!NOTE]
> A function that runs longer than the ten-second lease can overlap with
> another caller's. Storing the result does not check lease ownership, so a
> slower computation can overwrite a newer result. Keep loaders idempotent;
> for work that long, refresh the value on a schedule instead.

## Sharing a prefix and TTL with namespaces

When related entries need the same naming and expiry policy, an optional
**namespace** supplies a literal prefix and write TTL for keys of one value
type. It produces ordinary keys; cache operations do not require namespaces.
Using the `Profile` type above, declare a shared ten-minute policy:

```go
profiles := cache.NewNamespace[Profile]("profiles.v1", func(namespace *cache.NamespaceBuilder) {
	namespace.TTL(10 * time.Minute)
})
profileKey := profiles.Key("1") // profileKey.Name() == "profiles.v1:1"
```

`profileKey` is an ordinary `cache.Key[Profile]`, usable with `Put`, `Get`, and
`Remember` just like the standalone key above. A namespace is not bound to a
facade and does not allocate storage. Without configuration it uses separator
`":"` and TTL zero (no expiry). Dots already in `profiles.v1` remain unchanged.
A `KeyBuilder` callback on `Key` can override
the inherited TTL for one key. `Namespace.TTL()` reports write policy, not the
remaining lifetime of stored entries. Ordinary formatting and logging redact
namespaces as well as keys; `Name()` reveals the literal name.
As with keys, even unsupported format verbs redact the owned namespace, not any
raw names or descriptors you extract from it.

### Choosing names and separators

A child key's physical name is the namespace name, followed by its separator,
followed by the entry name. Names are not parsed, escaped, trimmed, or normalized.
If an existing deployment uses the former default dot separator, preserve it
explicitly before upgrading:

```go
legacy := cache.NewNamespace[Profile]("profiles.v1", func(namespace *cache.NamespaceBuilder) {
	namespace.Separator(".")
})
legacyKey := legacy.Key("1") // legacyKey.Name() == "profiles.v1.1"
```

An explicit empty separator concatenates names; it does not restore the default.
`Separator()` reports the separator used for future joins. Choose names and
separators that keep completed names distinct: prefix `a:b` with child `c`
and prefix `a` with child `b:c` both produce `a:b:c`. Different
namespaces or value types do not isolate entries with the same physical name.
Namespaces create neither separate storage nor bulk-clear boundaries.

For untrusted components, validate an allowed alphabet or encode each component
with an unambiguous application-owned scheme before joining it. Namespaces do
not provide authorization. Child names are strings; convert numeric IDs
explicitly, for example `profiles.Key(strconv.Itoa(id))` with standard-library
`strconv`.

Changing a separator changes the stored identity, not existing data. Standalone
`NewKey` names remain byte-for-byte unchanged. Keep readers, writers, deletions,
and `Remember` calls on the same final name during a rollout: `Remember` also
coordinates its lease using that final key. For security-sensitive entries such
as webhook receipts, changing names can admit replays; preserve their old names
for the required retention period or plan an explicit migration. Built-in
session records and rate-limit counters already select `":"` explicitly and
need no migration for this default change.

### Creating subnamespaces

If a feature needs a subgroup with the same value type, use `Subnamespace`:

```go
archive := profiles.Subnamespace("archive", func(namespace *cache.NamespaceBuilder) {
	namespace.TTL(time.Hour)
})
archived := archive.Key("1") // archived.Name() == "profiles.v1:archive:1"
```

The child inherits its parent's type, separator, and TTL unless its callback
overrides the policy. Here archived profiles use one hour; `profiles` still uses
ten minutes. The join that creates the child uses the parent's separator.
Changing the child's separator affects only subsequent joins, not its existing
name.

### Deriving namespace policies

To reuse a prefix with a different policy, derive an independent namespace:

```go
permanentProfiles := profiles.Derive(func(namespace *cache.NamespaceBuilder) {
	namespace.TTL(0)
})
```

Future keys from `permanentProfiles` inherit TTL zero; `profiles` keeps ten
minutes. Derivation does not update stored entries. Changing a derived
namespace's separator changes future child names, never its existing prefix.

Namespaces expose the same `Err()` check as keys. Empty namespace names, empty
names passed to `Key` or `Subnamespace`, and zero-value namespaces wrap
`cache.ErrInvalidKey`. Negative TTLs wrap `cache.ErrInvalidTTL`. The first
declaration error survives derivation and child creation, even if a later
callback repairs the setting. Construct a new valid definition instead.

## Counters

Unlike a cached report, an integer counter changes atomically with each update,
so callers do not need a separate read followed by a write. With a shared driver,
many processes can update page views or requests per client without losing a
count. Counters that reset on their own after a window, as rate limits need, use
`IncrementWithTTL`. [Cache Counters](counters.md) covers both. To reduce
duplicate work across callers, use a [cache lease](locks.md).

## Who uses the cache

The cache is shared infrastructure, so a few parts of Arkoren take a
`*cache.Cache`:

- [Sessions](../http-capabilities/sessions.md) store a record per visitor in a namespace.
  Losing the cache logs people out.
- [Rate limiting](../abuse-prevention/rate-limiting.md) keeps counters per client
  and window. Losing the cache resets everyone's limits.

Use a different Redis database or separate backend when you want to clear or
size these independently. `Builder.Name` only labels telemetry; it does not
isolate keys. Choose distinct physical names when sharing a backend, for
example `reports.v1.2026-03`. Namespaces do not create separate storage or bulk-clear
boundaries. A cache shared with sessions is not a place for data you
may want to clear in bulk.

## Events and telemetry

The cache publishes no events. Telemetry is the same for every driver. Pass
`Builder.Telemetry` to choose a handle explicitly; otherwise, each operation uses
the [process default](../observability/telemetry.md#the-process-default), which
is a no-op until configured. To label several caches separately, set
`Builder.Name`; it changes telemetry labels, not stored keys. Each facade cache operation records one client span named
`cache {operation}`, such as `cache get` or `cache remember`, plus a counter and
a duration histogram. `Remember` is a single span, not a span for each driver
call inside it.

`Driver` and `DriverAs` are untraced accessors. Calls through a raw driver, and
`Lease.Unlock` or `Lease.Extend`, do not emit facade operation spans. A facade
operation can make several driver calls, and a native client can have its own
transport retry policy; one span does not imply exactly-once effects.

Reads also record `arkoren.cache.hit`, so you can compute a hit ratio per cache.
For `get`, `pull`, `ttl`, and `has` it says whether the key was found. For
`remember` it is `true` when the value came from the cache
and `false` when the function ran. A miss is not an error: it never sets the
span status or `error.type`. Other failures set both, to the class of the error
and never its text. Keys and values are never recorded, because both often hold
personal data.

[Instrumented components](../observability/instrumented-components.md#cache)
lists every attribute and metric. To assert them in a test, use the fake from
`telemetrytest`, as shown in [Events and telemetry](../file-storage/events-and-telemetry.md#testing)
for storage.

## Testing

Import `testing`, standard library `time`, and
`arkoren.dev/core/service/cache/memorycache` for this complete test function.
It reuses the `Profile` type declared above.

Use the memory driver in tests; it needs no server and behaves like the others:

```go
func TestProfileIsCached(t *testing.T) {
	store := cache.New(memorycache.New())
	key := cache.NewKey[Profile]("profiles.1", func(entry *cache.KeyBuilder) {
		entry.TTL(time.Hour)
	})
	calls := 0

	load := func() (Profile, error) {
		calls++

		return Profile{ID: 1, Name: "Ada"}, nil
	}
	for range 2 {
		if _, err := store.Remember(t.Context(), key, load); err != nil {
			t.Fatal(err)
		}
	}

	if calls != 1 {
		t.Fatalf("loader ran %d times, want 1", calls)
	}
}
```

To test expiry without sleeping, run the test inside `testing/synctest`. The
memory driver reads the clock through the `time` package, so the bubble's fake
clock moves time forward instantly; see [Time](../testing/time.md). If you write
a driver of your own, `cachetest.Run` checks it against the whole contract;
see [Conformance suites](../testing/conformance-suites.md).

For every function and option, see the [`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
