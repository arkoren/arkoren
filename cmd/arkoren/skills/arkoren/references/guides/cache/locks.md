---
title: Cache Locks
nav: Locks
description: Acquire, release, and renew expiring leases without mistaking them for resource-level write protection.
order: 50
summary:
  - Acquire leases by skipping or waiting without treating expiry as guaranteed exclusivity.
  - Release and extend token-owned leases with explicit cleanup and failure policies.
  - Protect harmful writes at the destination rather than relying on cache coordination.
---

# Cache Locks

When several callers want to refresh the same catalog snapshot, a cache lock
lets one caller begin while others skip or wait. It is a **lease**: permission
that expires after a chosen duration even if its holder is still working.
Expiry recovers coordination after a crash, but also permits overlapping work.

Examples assume an existing `*cache.Cache` named `store`, an operation context
`ctx`, and `cache` from `arkoren.dev/core/service/cache`. Import standard-library
`context`, `errors`, and `time` where used. Replicas coordinate only through a
[shared backend](drivers.md); memory leases coordinate callers sharing one driver
instance, not independent drivers or processes.

## Lease Safety

> [!WARNING]
> A paused holder can continue working after its lease expires and another caller
> acquires it. Backend data loss can release a lease too. Use leases to reduce
> duplicate work; protect harmful writes with a transaction, version check, or
> fencing enforced by the destination resource.

Arkoren does not issue fencing tokens: the protected database or external service
cannot identify stale writers from a cache lease. Extending or successfully
releasing a lease does not prove the work never overlapped another holder.
For work where duplication matters, make the action idempotent or protect it at
the resource itself.

Lease names are literal strings, not typed keys; key and namespace TTL policies
do not apply. The facade stores a lease named `catalog-refresh` at physical key
`lock:catalog-refresh`. An ordinary entry named `catalog-refresh` is separate,
but writing an ordinary entry named `lock:catalog-refresh` can overwrite the
lease. Reserve physical `lock:` keys and logical lease names starting with
`remember:` for the facade, including after namespace joins. These conventions
do not create an isolated namespace or access-control boundary.

## Taking Leases

If another refresh is already in progress, this caller can skip rather than wait.
Attempt acquisition once with `TryLock`:

```go
lease, held, err := store.TryLock(ctx, "catalog-refresh", time.Minute)
if err != nil {
	return err
}
if !held {
	return nil // Another caller holds the lease; skip this refresh.
}
defer lease.Unlock(ctx)
```

`lease, true, nil` means acquisition succeeded. `nil, false, nil` means another
holder owns it. The deferred release is best effort using the work context;
use the [separate cleanup context](#releasing-leases) below when cancellation
must not prevent release. The application can now run its rebuildable refresh.

The duration must be positive or acquisition fails with `cache.ErrInvalidTTL`.
An empty name fails with `cache.ErrEmptyLockName`. If you never unlock, the lease
expires after one minute. Acquisitions are not reentrant: another acquisition
with the same name contends even in the same goroutine.

`TryLock` calls the driver once, but the native client's transport policy can
still retry. A network error can leave acquisition uncertain; do not interpret
it as proof that the backend stored no lease. Expiry remains the fallback for
an acquisition whose response was lost.

## Waiting Briefly

If this request needs the refreshed result, `Lock` waits for ownership instead.
Give the wait its own deadline without replacing the context used for later
work:

```go
waiting, cancel := context.WithTimeout(ctx, 2*time.Second)
defer cancel()

lease, err := store.Lock(waiting, "catalog-refresh", time.Minute)
```

`Lock` polls with jittered backoff growing from 10ms to a 250ms cap. The
two-second wait budget and one-minute lease are independent: the lease lifetime
begins at acquisition, not when waiting starts. Context deadlines require the
backend to honor its context.

Handle a failed wait before using the returned lease:

```go
if errors.Is(err, context.DeadlineExceeded) {
	return nil // Choose the application's timeout fallback.
}
if err != nil {
	return err
}
defer lease.Unlock(ctx)
```

A deadline error is not proof that another caller is still refreshing, nor that
an acquisition command could not have reached the server. Only start work after
successful acquisition. Use the original `ctx` for work; a canceled wait context
would also prevent a later release through that context.

## Releasing Leases

`Unlock` removes the lease only if its random ownership token still matches.
It cannot remove a newer holder's token after yours expires. The `Lease` value
is safe for concurrent use.

If request cancellation must not prevent cleanup, use a bounded context that
retains request values but ignores its cancellation and deadline. After work
finishes, this example allows a separate one-second release budget:

```go
cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
defer cancel()
err := lease.Unlock(cleanup)
```

Use this release policy instead of the work-context defer above, arranging it on
every exit path after successful acquisition. Inspect `err` when cleanup failures
need reporting. A cooperative backend honors the timeout; an arbitrary driver
cannot be forcibly interrupted. If release fails, expiry is the fallback.

If ownership is already lost, `Unlock` returns `cache.ErrLockLost` once and
leaves another holder untouched. After confirmed release or confirmed loss,
later calls do nothing and return nil. A backend failure is returned and leaves
the call retryable with a fresh context.

A lost reply can mean the server released the lease even though `Unlock`
returned an error. A retry can then report `cache.ErrLockLost`. Neither that
error nor successful cleanup establishes whether protected work overlapped.

## Extending Leases

Longer work must renew before expiry. `Extend` sets the expiry to a positive
duration from now; it does not add that duration to the previous deadline.
For application-owned `batches` and `refresh`, check renewal before each batch:

```go
for _, batch := range batches {
	if err := lease.Extend(ctx, time.Minute); err != nil {
		return err // Stop: ownership is lost or cannot be confirmed.
	}
	if err := refresh(ctx, batch); err != nil {
		return err
	}
}
```

Each batch must still finish within the available lease time; a renewal before a
long pause cannot prevent expiry during that pause. `cache.ErrLockLost` means
the token no longer owns the lease, including after release. Treat it as
"another caller may also be working". Other backend errors also prevent you
from confirming renewal. A nonpositive extension returns `cache.ErrInvalidTTL`.

Arkoren does not renew in the background. An independent renewal loop that
continues while work is stuck could keep coordination occupied indefinitely,
and no renewal strategy makes lease ownership a resource-level correctness
guarantee.

## Protecting Writes

For a cache destination, an atomic
[`CompareAndPut`](overview.md#guarded-writes) can reject a write whose observed
guard has changed. A lease reduces conflicts; the guard protects the write.
That comparison does not fence a database update or an external API call, and
restoring identical guard bytes can allow an old update to match again.

For database work that needs connection-scoped coordination, consider a
[database lock](../database/locking.md). Unlike a lease, it has no application
TTL and releases when its owning connection/session ends or the database detects
its loss. Choose locking or transaction semantics appropriate to the protected
resource; a connection lock still does not make arbitrary external effects
exactly once.

`Remember` uses a ten-second lease named `remember:<completed-key>`, stored at
`lock:remember:<completed-key>`. It neither renews nor guards its final write;
see [loader and cleanup limits](overview.md#remembering-values).
[Sessions](../http-capabilities/sessions.md) also use ten-second leases to reduce
save conflicts, but additionally use guarded writes so an expired lease cannot
authorize a pending save past successful retirement. Cache loss or restoration
can still affect that state.

Locks publish no events; ownership loss is returned to the holder as
`cache.ErrLockLost`. Acquisition spans include waiting, but `Unlock` and `Extend`
have no facade spans. [Monitoring](monitoring.md) explains these measurement
boundaries. For expiry tests, the [memory driver](drivers.md#memory) works with
[`testing/synctest`](../testing/time.md); custom drivers can run `cachetest.Run`
through the [conformance suite](../testing/conformance-suites.md).

For every lease operation and error, see the [`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
