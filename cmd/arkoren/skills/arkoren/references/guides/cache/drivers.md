---
title: Cache Drivers
nav: Drivers
description: Compare cache driver capabilities, multi-instance coordination, persistence, and operational requirements.
order: 30
---

# Cache Drivers

## Introduction

Choose a driver based on whether cached state must be shared between application
instances and whether it must survive an application restart. Memory suits
process-local state; Redis provides a shared backend. See [Cache
Overview](overview.md) for using the cache, and [Choosing a
Driver](../drivers/choosing-a-driver.md) for every service's drivers side by
side.

## Compatibility matrix

Compare cache operations, shared state, and restart behavior. An **instance** is a
running application process; **shared state** means replicas can use the same
entries, counters, and leases rather than keeping independent copies.

| Driver | Shared state | Expiry | Counters | `Pull` | Guarded writes | [Locks](locks.md) | App restart | Backend restart |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No | Yes — Typed key and namespace write TTLs apply through the facade. | Yes — Atomic checked int64 arithmetic within one driver instance. | Yes — Atomic within one driver instance, not across replicas. | Yes — Same-key and different-key comparison and write share one mutex. | Yes — Leases coordinate callers sharing one driver instance, not separate processes. | No | No |
| [Redis](#redis) | Yes | Yes — Positive guarded-write TTLs round up to milliseconds. | Yes — Atomic checked int64 arithmetic; positive counter TTLs round up to milliseconds. | Yes | Yes — One Lua execution checks the guard and writes the destination. | Yes | Yes | Partial — Depends on Redis persistence settings; the driver does not guarantee survival. |

Counters, `Pull`, and `CompareAndPut` are atomic operations. Restart columns indicate whether
cached state survives the restart.

All three [counter operations](counters.md) reject mathematical results outside
`int64` without changing the target value or TTL or creating an absent target.
This includes `Decrement` with a `math.MinInt64` delta; valid subtraction must
not fail because negating the delta would overflow. Error identities remain
backend-native, not a portable overflow sentinel. Atomicity is the operation's
backend guarantee, not a read-check-write sequence in the facade or a promise
of exactly-once effects across transport retries.

Typed keys, optional namespaces, literal name joining (default separator `":"`),
and inherited write TTLs belong to the facade and work with both providers. They
do not add native namespace support or backend isolation. Preserve deployed
dot-joined names with `NamespaceBuilder.Separator(".")`; see
[namespace migration](overview.md#choosing-names-and-separators). The
raw `cache.Driver` uses opaque string names, raw bytes, and explicit TTLs,
including `Match` and `Entry` for guarded writes.

- **Memory:** atomic operations and leases apply only within one driver instance.
  No external infrastructure is required; restarting the process loses its state.
- **Redis:** shared state assumes the same Redis server, logical database, and
  keys. Redis 6.2+ and network access are required. An application restart does
  not clear Redis; entries can still expire, be evicted, or be deleted. Survival
  across a Redis restart depends on its persistence settings, not the driver.

Choose memory even with several replicas if independent caches are acceptable.
Choose Redis if those replicas must share entries, enforce a common
[rate limit](../abuse-prevention/rate-limiting.md), or coordinate through leases.
A memory-backed rate limit gives each independent cache its own allowance.

Shared state does not guarantee unlimited backend capacity, high availability,
or Redis Cluster support. The driver wraps a `*redis.Client`; adding application
replicas does not scale the Redis backend. Leases expire and do not guarantee
exactly-once work; review the [lock limits](locks.md) before using them to
coordinate side effects.

## Example setup

Run installation commands from your application's Go module. Examples use
`cache` from `arkoren.dev/core/service/cache`, `application` from
`arkoren.dev/core/application`, standard-library `time`, and an operation context
`ctx`. Composer callbacks use the [application lifecycle](../application/lifecycle-and-ownership.md).
Focused snippets omit routine error checks, as described in [Cache Overview](overview.md#storing-and-reading-values).

## Memory

The memory driver keeps entries in the process. It is part of `arkoren.dev/core`,
so there is nothing to install. Choose it for tests, local development, and a
single process whose cached data does not need to survive a restart or be shared.

### Connecting

`New` never fails and starts nothing:

```go
import "arkoren.dev/core/service/cache/memorycache"

store := cache.New(memorycache.New())
```

Register `Start` and `Stop` if you want a background janitor, described next:

```go
func Compose(composer *application.Composer) {
	driver := memorycache.New()
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	store := cache.New(driver)
	_ = store // Pass store to your handlers and services.
}
```

### Configuration

The one setting is how often expired entries are swept away:

```go
driver := memorycache.New(func(store *memorycache.Builder) {
	store.Cleanup(5 * time.Minute)
})
```

An expired entry is never returned, whether or not a sweep has run. The sweep
only frees memory held by entries nobody reads again. Writes sweep inline when
the last sweep is older than the interval, so abandoned expired entries are
reclaimed even if you never call `Start`; `Start` adds a goroutine that sweeps on the same interval
and `Stop` ends it. The default is one minute, zero selects that default, and a negative value turns
sweeping off, which leaves expired entries in memory until their key is accessed.

### Storing values

Use the shared [typed write and read example](overview.md#storing-and-reading-values)
with `store`. A successful write is available immediately within this driver;
another memory driver has independent state.

### Behavior and limits

Values are copied in and out, so mutating a slice you stored or read cannot
change the cache. There is no size limit and no eviction: the cache grows until
entries expire, so give everything a TTL. Counters, `Pull`, `CompareAndPut`, and
[locks](locks.md) are atomic within one driver instance. Nothing is shared between
processes, so with several replicas each has its own entries, a rate limit
allows its full count on every replica, and a lock excludes only callers in the
same driver instance. Two separately constructed memory drivers do not
coordinate even inside one process.

Counter lookup, checked arithmetic, and mutation hold the same mutex. Overflow
wraps `strconv.ErrRange` and leaves the target unchanged, including whether it
has an expiry. Subtracting `math.MinInt64` is checked directly without negating
it. This error identity is specific to memory; portable code must not assume it
for another driver.

For every function and option, see the [`memorycache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache/memorycache).

## Redis

The Redis driver stores entries in a Redis server using `go-redis`. Choose it
when several processes must share cached entries, session state, or rate-limit
counters, or when the cache should outlive an application deploy.
It requires Redis 6.2 or later.

### Installation

```sh
go get arkoren.dev/driver/redis
```

### Connecting

`New` takes construction callbacks, never fails, and does not connect until the first command.
Set the address of your existing Redis service and wrap the driver:

```go
import "arkoren.dev/driver/redis/rediscache"

driver := rediscache.New(func(store *rediscache.Builder) {
	store.Addr("cache:6379")
})
store := cache.New(driver)
```

Register `Stop` as cleanup, `Start` as startup, and `Ping` as the cache health
check using [Lifecycle and ownership](../application/lifecycle-and-ownership.md).
`Start` checks the connection so a misconfigured Redis fails startup; `Stop`
closes the client. For a server on your host, use `127.0.0.1:6379` instead.

### Configuration

| Option | Default | Meaning |
| --- | --- | --- |
| `Builder.Network` | `tcp` | `tcp` or `unix` |
| `Builder.Addr` | `localhost:6379` | Server address, or socket path for `unix` |
| `Builder.Username` | none | ACL user |
| `Builder.Password` | none | ACL password |
| `Builder.DB` | `0` | Logical database number |
| `Builder.TLS` | none | A `*tls.Config`; enables TLS |

Read the password from your configuration, never from source. The driver's
diagnostic `Config` redacts its password through `String`, `GoString`, and
`slog.LogValuer`; its public `Password` field is still the secret. Do not assume
this record has the owned cache key's protection for every unsupported format
verb. For settings these builder methods do not
cover, build and configure a `*redis.Client` yourself and pass it to
`rediscache.NewFrom`. This conversion has no construction callbacks. Stop closes
the supplied client too, so arrange one shutdown owner when sharing it. Its native
TLS settings apply. `driver.Client()` returns the underlying client for commands the
cache does not offer.

`New` uses go-redis's transport timeout and retry defaults. If you need a
different budget, configure the native client before `NewFrom`; use bounded
operation contexts as well. Neither facade construction nor `Builder.Name`
adds key isolation or a request timeout.

`New` enables the native `ContextTimeoutEnabled` setting so operation deadlines
apply during socket I/O. `NewFrom` preserves the supplied client's settings:
enable that option before transfer if request deadlines must bound its socket
I/O. This is cooperative timeout behavior, not forced interruption of arbitrary
application callbacks.
An already-canceled operation context takes precedence over invalid TTL
validation, including for raw driver calls.

### Storing values

Use the shared [typed write and read example](overview.md#storing-and-reading-values).
Processes using the same server and logical database can read the stored value.
Use [counters](counters.md) for atomic increments and [leases](locks.md) to reduce duplicate work.
Use [guarded writes](overview.md#writing-only-while-an-observed-value-still-matches)
when an entry must still match its observed bytes before you replace it or write
another key. Missing or changed guards reject the write without altering its
destination. This applies to callers sharing the same backend, not independent
memory drivers.

### Behavior and limits

Entries live in Redis, so they are shared by every process that uses the same
server and database, and they survive an application restart. Whether they
survive a Redis restart depends on your Redis persistence settings; assume they
do not. Expiry is measured by Redis. Counters are atomic on the server, and
`IncrementWithTTL` increments and ensures an expiry in one step, preserving an
existing expiry. Positive counter TTLs round up to whole milliseconds.
The returned count remains exact across the full `int64` range; the script
reads decimal text rather than returning a rounded Lua number. `TTL` reads
Redis's millisecond `PTTL` reply directly, preserving subsecond lifetimes rather
than rounding to whole seconds. Zero means no expiry, and a missing or expired
entry returns `cache.ErrNotFound`. Both `TTL` and counter lifetime reporting
saturate at the maximum `time.Duration` instead of overflowing. Native Redis
overflow errors leave the target and its TTL unchanged. For a `math.MinInt64`
decrement, the driver uses one atomic exact-decimal script because native
`DECRBY` rejects that argument even for representable results; it does not
negate the delta in Go or perform a client-side read-check-write sequence.
This atomic operation lets replicas share rate-limit counters;
it does not preserve counts if Redis loses or evicts them. `Pull` uses `GETDEL`, the reason for
the Redis 6.2 minimum. Locks use `SET NX PX` to take a lock and small Lua scripts
to release or extend it only while it still holds the caller's token, so one
replica can never release another's lock.

`CompareAndPut` uses one Lua execution for both keys. Positive destination TTLs
round up to milliseconds without overflowing, and a successful write replaces
the old TTL. A distinct guard keeps its expiry; a mismatch changes neither
entry. The adapter disables replay of possibly executed guarded writes even
when the native client enables retries: a lost response returns an error with
an uncertain outcome, not a second comparison reported as a mismatch. This is
atomicity on the active Redis server, not durability across eviction, restart,
failover, or restoration of earlier data. It does not add Redis Cluster support.

Because Redis is a network service, calls can fail or be slow for reasons that
do not apply to memory. Memory operations can still fail, for example on a
canceled context or an invalid TTL. Give Redis operation contexts a deadline. A cache that is
down needs an explicit fallback policy. `Remember` returns a computed value
alongside an error only if computation succeeded and storing that value failed.
A read or lock failure returns before computation, so do not assume every error
comes with a usable value.

### Using native Redis commands

For a Redis operation outside the portable cache contract, use the driver's
`*redis.Client`. This example verifies server connectivity without changing data:

```go
status, err := driver.Client().Ping(ctx).Result()
```

On success `status` is `"PONG"`. Native commands bypass facade telemetry and JSON
encoding. Do not overwrite cache entries or `lock:` keys with incompatible data.
`NewFrom` accepts an already configured client, including its timeout and retry
settings; the driver closes that client in `Stop` too.

### Running locally

With Docker and Compose installed, save this as `compose.yaml` in a new empty
development directory, not over an existing application configuration. The unauthenticated service binds only to loopback;
do not expose it to an untrusted network:

```yaml
services:
  cache:
    image: redis:7.4-alpine
    ports:
      - "127.0.0.1:6379:6379"
```

```sh
docker compose up -d
docker compose exec cache redis-cli ping
```

The check prints `PONG`. Use `rediscache.New()` for `localhost:6379`, or change
the earlier `Addr` to `"127.0.0.1:6379"`. Start the driver, then write and read
a value using the shared task above. If the service is still starting, retry
the check before starting your application. Run `docker compose down` when
finished; this disposable configuration does not preserve cache data.

For every function and builder setting, see [the `cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache),
[`memorycache`](https://pkg.go.dev/arkoren.dev/core/service/cache/memorycache), and
[`rediscache`](https://pkg.go.dev/arkoren.dev/driver/redis/rediscache).
