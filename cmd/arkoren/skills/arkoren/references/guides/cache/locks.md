---
title: Cache Locks
nav: Locks
description: Take a named lease in the cache so one process at a time does a piece of work, extend it, and choose between cache and database locks.
order: 25
---

# Cache Locks

## Introduction

When several callers would rebuild the same report or send the same digest,
a cache lock lets one caller acquire permission to work while the others skip
or wait. Replicas coordinate only if they use the same shared cache backend;
memory locks coordinate callers sharing one driver instance, not separate
processes.

A cache lock is a **lease**: it expires after the duration you give it, whether
or not its holder is still working. That is what frees it when a process
crashes, and it is also the main difference from a [database lock](../database/raw-sql-and-locks.md#named-locks).
[Cache lock or database lock](#cache-lock-or-database-lock) helps you choose.

## Taking a lock

Use the `store` facade from [Cache Overview](overview.md#creating-a-cache) and
your operation context `ctx`. Import `context`, `errors`, and `time` from the
standard library for the examples below.

Lock names remain literal strings, not typed cache keys. Key and namespace TTL
policies do not apply to leases; pass the lease duration explicitly.

> [!WARNING]
> A paused holder can keep working after its lease expires, while another caller
> owns the lock. Cache loss can also release a lock. Protect harmful writes with
> a transaction, version check, or resource-level fencing; use a lease only to
> reduce duplicate work, not to guarantee correctness.

`TryLock` invokes the driver once; the native client can still have its own
transport retries. It returns a `*cache.Lease` and `true` when the
lock is now held, and `nil`, `false`, and a nil error when another holder has
it:

```go
lease, held, err := store.TryLock(ctx, "nightly-report", time.Minute)
if err != nil {
	return err
}
if !held {
	return nil // another instance is running the report
}
defer lease.Unlock(ctx)

return buildReport(ctx)
```

The duration is the lease: if you never unlock, for example because the process
dies, the lock frees itself after a minute. It must be positive, otherwise the
call fails with `cache.ErrInvalidTTL`, and the name must not be empty, otherwise
it fails with `cache.ErrEmptyLockName`.

`Lock` waits instead. It polls the cache, with a jittered backoff that grows
from 10ms to 250ms, until it holds the lock or `ctx` ends, in which case it
returns the context's error. Bound the wait with a deadline:

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()

lease, err := store.Lock(ctx, "nightly-report", time.Minute)
if errors.Is(err, context.DeadlineExceeded) {
	return nil // the report is still being built elsewhere
}
if err != nil {
	return err
}
defer lease.Unlock(ctx)
```

A deadline error means this caller did not acquire the lease before its wait
ended; it does not prove that another holder is still building the report.
The deferred releases above are best effort and use the operation context. If
cancellation must not prevent release, use a
[separate cleanup context](#releasing-and-extending) instead.

Locks are exclusive and not reentrant: a second `Lock` with the same name waits
even in the same goroutine. Lock names use a `lock:` key prefix, so a cache key
called `nightly-report` does not touch the lock of the same name. Reserve
`lock:` for Arkoren: an ordinary entry named `lock:nightly-report` would
overwrite that lock.
Also reserve lock names starting with `remember:` for `Remember`; taking one
yourself can block or interfere with its cache-miss coordination.
These prefixes are naming conventions in the same backend key space, not an
isolated namespace or an access-control boundary. `Remember` uses the physical
name `lock:remember:<completed-key>`. Keep ordinary entry names outside `lock:`
and application lease names outside `remember:`, including after namespace joins.

## Releasing and extending

To release a lease before it expires, call `Unlock`. It releases the lock only
while the lease still owns it. Each lease
carries a random token, and the cache removes the lock only if it still holds
that token, so a holder whose lease expired can never release the lock of
whoever took it next. `Unlock` is safe to call more than once; after the first
successful release or confirmation that the lock is lost, later calls do
nothing and return nil. A cache failure is returned and leaves `Unlock`
retryable. If the operation context is already canceled, use a separate,
bounded cleanup context to attempt release; otherwise expiry is the fallback.

A backend error can leave release uncertain: the server might have removed the
lease before the reply was lost. A retry can therefore report `cache.ErrLockLost`
even if the earlier call released it. Neither that error nor a successful release
proves the protected work did not overlap another holder.

For cleanup that retains request values but ignores request cancellation, choose
an application cleanup budget explicitly; this example allows one second:

```go
cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
defer cancel()
err := lease.Unlock(cleanup)
```

Inspect `err` if cleanup failures need reporting. The deadline requires the
backend to honor its context; it cannot interrupt a noncooperative driver.
Expiry remains the fallback when release fails.

Work that may outlast the lease must renew it before expiry. `Extend` sets the
lock to expire the given positive duration from now; a non-positive duration
returns `cache.ErrInvalidTTL`. Here `batches` and `process` are application-owned
work, and `lease` is the acquired lease:

```go
for _, batch := range batches {
	if err := lease.Extend(ctx, time.Minute); err != nil {
		return err // the lock is gone: stop
	}
	process(batch)
}
```

Both `Unlock` and `Extend` return `cache.ErrLockLost` when the lease no longer
holds the lock because it expired first (`Unlock` reports it once). Treat it as
"another process may be working on this too" and stop or make the work
idempotent. Arkoren does not renew leases in the background for you: a renewal
that keeps running while the work is stuck would hold the lock forever.

## Cache lock or database lock

| | Cache lock | Database lock |
| --- | --- | --- |
| Held by | A key in the cache with an expiry | A database connection or session |
| Freed when the holder crashes | After the lease expires | When the database detects the connection has closed |
| Time limit | Yes: the lease, extended with `Extend` | None |
| Survives a cache or database restart | No: a cache that loses data frees the lock | No: connections drop |
| Needs | The cache you already run | A supported database driver |

Use a cache lock to avoid duplicate work where an overlap is harmless or
detectable: refreshing a cache entry, sending a digest, running a scheduled job
on one replica. Use a [database lock](../database/raw-sql-and-locks.md#named-locks)
when two holders at once would corrupt data and you want the lock to last
exactly as long as the work.

> [!WARNING]
> A cache lock is not a correctness guarantee on its own. A holder that pauses
> longer than its lease, for example during a long garbage collection or a
> network stall, can keep working after the lock has passed to someone else, and
> Arkoren issues no fencing tokens for the protected resource to reject the
> stale writer. If an overlap would be harmful, protect the resource itself
> (a database lock, a transaction, or a version check) rather than relying on the
> lease.

If the protected resource is another cache entry, use
[`CompareAndPut`](overview.md#writing-only-while-an-observed-value-still-matches)
to make the observed guard comparison and destination write atomic. The lease
can reduce conflicts, but it is the guarded write that rejects stale updates.
It does not fence writes to a database or another external resource.

## Where Arkoren uses them

[`Remember`](overview.md#remembering-a-computed-value) takes a cache lock named
`remember:<key>` to coordinate concurrent misses, and
[sessions](../http-capabilities/sessions.md) take one per session ID while saving
to reduce conflicts between late saves and logout. Both use a ten-second lease.
`Remember` uses a separate ten-second, cancellation-independent cleanup context
for release; that is distinct from the lease's own expiry. Its
[cleanup budget and loader limits](overview.md#remembering-a-computed-value)
remain subject to backend context cooperation and the overlap limits above.
Sessions additionally use atomic guarded writes, so an expired lease cannot let
a pending save bypass successful retirement. Cache loss and restoration still
affect stored state.

## Events and telemetry

Locks publish no events: losing a lock is reported to its holder as
`cache.ErrLockLost`. `Lock` and `TryLock` each record one client span, `cache
lock` and `cache try_lock`, with the usual cache counter and histogram; the span
of `Lock` includes the time spent waiting. Releasing and extending are not
traced, and lock names are never recorded.

## Testing

The memory driver implements locks, and `testing/synctest` makes expiry instant,
so a test can take a lock, advance time past the lease, and check that another
holder gets in. A driver of your own passes the same behavior by running
`cachetest.Run`.

For every function and option, see [the `cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
