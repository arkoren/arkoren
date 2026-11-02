---
title: Cache Counters
nav: Counters
description: Update shared integer counts atomically and give request windows an expiry without a separate write.
order: 40
summary:
  - Update int64 counts atomically without applying the key's write TTL.
  - Establish expiring request windows while preserving an existing deadline.
  - Distinguish unchanged arithmetic rejection from uncertain network outcomes.
---

# Cache Counters

Concurrent requests can lose increments if each reads a count and writes back
its own replacement. Counter operations perform the arithmetic and mutation
atomically instead. Use them for replaceable counts such as article views or
requests within a window—not as the only record of billing or inventory.

These examples assume an existing `*cache.Cache` named `store` and operation
context `ctx`. Use `cache` from `arkoren.dev/core/service/cache` and
standard-library `time`. A [shared driver](drivers.md) is required for replicas
to share counts; separate memory drivers each keep their own total. Focused
snippets omit routine propagation while keeping error results visible.

## Updating Counts

An article-view counter needs a `cache.Key[int64]`. Increment it when a view
should count:

```go
views := cache.NewKey[int64]("article-views.v1:article-73")
total, err := store.Increment(ctx, views, 1)
```

On success, `total` is the new value. A missing or expired counter starts from
zero, so its first increment by one returns one. `Decrement` subtracts its delta
with the same atomic behavior:

```go
total, err = store.Decrement(ctx, views, 1)
```

This is useful for correcting a counted view; it is not a condition that prevents
negative totals. Counters store base-10 integer text, so `store.Get(ctx, views)`
can decode the count as `int64`. An existing noninteger value causes an error
and remains unchanged.

`Increment` and `Decrement` ignore the key's declared write TTL. A newly created
counter has no expiry, and an existing counter keeps its current expiry. Giving
the key a five-minute policy therefore does not make these updates expire.

## Expiring Windows

Request counts usually need to reset. A separate increment followed by an
expiry-setting call leaves a crash window in which the count never expires.
`IncrementWithTTL` updates the count and ensures an expiry in one atomic step.

For requests from an already authorized account, use a stable internal account
ID as the key component. Here `accountID` is that application's string ID:

```go
requests := cache.NewKey[int64]("export-requests.v1:" + accountID)
count, remaining, err := store.IncrementWithTTL(ctx, requests, 1, time.Minute)
```

On success, `count` is the updated total and `remaining` is time until expiry.
A missing entry starts from zero with a one-minute expiry. An existing entry
without expiry receives one minute; an existing expiring entry keeps its current
deadline. Later requests do not slide that deadline forward. The window begins
when expiry is first established, not at an aligned wall-clock minute.

The explicit TTL argument controls this operation, not the key's write policy.
It must be positive; zero or a negative duration returns `cache.ErrInvalidTTL`.
A custom driver unable to provide atomic increment-and-expiry must return
`cache.ErrUnsupported`, not emulate the operation with separate calls.

Redis rounds positive counter TTLs up to whole milliseconds, including
submillisecond durations. Its result remains exact throughout `int64`, and
remaining lifetimes beyond `time.Duration` saturate at that type's maximum.
The [rate limiting middleware](../abuse-prevention/rate-limiting.md) uses this
operation and handles request rejection for you.

## Handling Failures

Check `err` before using the returned count or lifetime. Arithmetic overflow and
noninteger contents are rejected without changing the stored value or expiry.
That rejection is different from a transport failure: Redis might have applied
an increment before its reply was lost.

Do not blindly repeat an increment after an uncertain result; doing so can count
the same action twice. Native clients can also have transport retry policies,
so a single facade call or telemetry span is not proof of one backend execution.
Choose native retry settings in [Cache Drivers](drivers.md#native-clients) and
an application recovery policy appropriate to the count. Exactly-once business
effects require a durable application protocol, not a cache counter.

No expiry does not guarantee retention. Backend loss, eviction, deletion, or
restart can erase counts and reset allowances. A cache-based limit must account
for those failures rather than treating a successful update as permanent state.

## Integer Bounds

All three counter operations accept only mathematical results from
`math.MinInt64` through `math.MaxInt64`, inclusive. An out-of-range result returns
a backend-native error: there is no portable overflow sentinel. Rejection leaves
the target value and TTL unchanged; a missing or expired target remains absent.
In particular, overflow in `IncrementWithTTL` does not add expiry to a persistent
counter.

Subtraction checks the mathematical result directly, without first negating the
delta. With standard-library `math`, initialize an adjustment counter at its
minimum:

```go
adjustment := cache.NewKey[int64]("view-adjustments.v1:article-73")
err := store.Put(ctx, adjustment, math.MinInt64)
```

After that successful write, subtracting the same minimum is representable:

```go
total, err := store.Decrement(ctx, adjustment, math.MinInt64)
```

On success, `total` is zero. Subtracting `math.MinInt64` from zero instead fails
and leaves zero unchanged; doing so against an absent target fails without
creating it. Do not replace subtraction with `Increment(ctx, key, -delta)`:
negating `math.MinInt64` overflows in application code before the driver sees it.
Nor should callers replace checked atomic arithmetic with a read, local range
check, and write; concurrent updates can invalidate that check.

For every counter operation and error, see the [`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
