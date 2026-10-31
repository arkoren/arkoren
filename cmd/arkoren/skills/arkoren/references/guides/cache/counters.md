---
title: Cache Counters
nav: Counters
description: Keep atomic integer counters in the cache, including counters that expire with their window through `IncrementWithTTL`.
order: 20
---

# Cache Counters

## Introduction

Counters answer questions such as "how many times was this post viewed" or
"how many requests has this client sent this minute". A read followed by a write
can lose an update when callers race. Use the cache's atomic counter operations
instead. If several replicas must share a count, use a [shared driver](drivers.md),
such as Redis; separate memory drivers keep separate counts.

## Updating a counter

`Increment` and `Decrement` change an integer stored under a key and return the
new value. The driver checks the range and applies the change as one atomic
operation; callers must not emulate this with a read, a local check, and a write.
A missing or expired key starts from zero when the result fits `int64`.

Here `store` is the [cache facade](overview.md#creating-a-cache), `ctx` is your
operation context, and `time` below is the standard library package. Focused
snippets omit routine error checks but keep the error result visible:

```go
key := cache.NewKey[int64]("views.posts.7")
views, err := store.Increment(ctx, key, 1)
```

On success, `views` is the new count. `Decrement` subtracts its delta with the
same atomic behavior. The counter is stored as its decimal text, so
`store.Get(ctx, key)` reads it as `int64`; an existing value that is not a base-10 integer
causes an error and is left unchanged.

### Rejecting an out-of-range result

`Increment`, `Decrement`, and `IncrementWithTTL` accept only mathematical results
from `math.MinInt64` through `math.MaxInt64`, inclusive. If a result falls outside
that range, the operation returns an error without changing the target's value
or TTL. A missing or expired target remains absent. In particular,
`IncrementWithTTL` does not add an expiry to an existing persistent counter when
its arithmetic fails.

Overflow errors keep the driver's native identity; there is no portable cache
overflow sentinel. Check the error before using the returned count. This is the
defined overflow rejection behavior, not a promise that a transport error proves
no write occurred: a lost reply can leave a network operation's outcome unknown.

`Decrement` checks subtraction directly, rather than negating its delta. With
`math` imported from the standard library, subtracting the minimum delta from
itself is valid:

```go
balance := cache.NewKey[int64]("balance")
err := store.Put(ctx, balance, math.MinInt64)
value, err := store.Decrement(ctx, balance, math.MinInt64)
```

On success, `value` is zero. Subtracting `math.MinInt64` from zero instead fails
without changing that zero; doing so on a missing target fails without creating
it. Do not replace subtraction with `store.Increment(ctx, key, -delta)` in
application code, because negating `math.MinInt64` overflows before the driver
sees it.

### Choosing an expiry policy

A counter key must be `cache.Key[int64]`. `Increment` and `Decrement` do not apply
its write TTL: a counter created this way has no expiry, and an existing one keeps the expiry
it had. A counter that should reset, such as "requests in this minute", needs an
expiry that is set in the same step as the increment. Doing it in two calls
leaves a gap: a crash between them gives you a counter that never resets.

## Counters with an expiry

For a count that resets after a window, use `Cache.IncrementWithTTL`. It
increments a key and makes sure it expires, in one
atomic step. It is part of the driver contract, so the memory and Redis drivers
both provide it; a driver whose vendor cannot do it atomically returns
`cache.ErrUnsupported`. Here `address` identifies the client whose requests you
are counting:

```go
clientKey := cache.NewKey[int64]("rate." + address)
count, remaining, err := store.IncrementWithTTL(ctx, clientKey, 1, time.Minute)
_ = count     // requests so far in this window
_ = remaining // time until the counter resets
```

The explicit TTL argument, not the key's write policy, controls this operation.
On success, a new key is created from zero and given the TTL. A key that has no
expiry gets the TTL, and one that already has an expiry keeps it. The TTL must
be positive, otherwise the call fails with `cache.ErrInvalidTTL`. Redis rounds every positive
TTL up to whole milliseconds, including durations shorter than one millisecond;
it returns the exact `int64` count without a floating-point round trip. Remaining
Redis lifetimes larger than `time.Duration` can represent saturate at its maximum.
The [rate limiting middleware](../abuse-prevention/rate-limiting.md) uses
`IncrementWithTTL` for its counters.

For every counter method and error, see the [`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
