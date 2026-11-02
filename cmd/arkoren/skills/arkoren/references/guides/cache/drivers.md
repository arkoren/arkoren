---
title: Cache Drivers
nav: Drivers
description: Choose process-local or shared caching and configure driver connections, ownership, and native access.
order: 20
summary:
  - Compare memory and Redis sharing, atomic operations, expiry, and restart behavior.
  - Construct a memory store and choose inline or background expiry cleanup.
  - Configure Redis connections, lifecycle ownership, native clients, and local verification.
---

# Cache Drivers

Choose memory when independent process-local copies are acceptable. Choose Redis
when replicas must read the same entries, share request allowances, or coordinate
work. A separate memory driver has separate state even inside the same process;
it does not become shared by giving its facade the same name.

Both drivers expose the same [typed cache operations](overview.md). Differences
in state sharing, capacity, network failures, and persistence still affect your
application. Install modules from your application's Go module directory. If
core is not installed yet, run `go get arkoren.dev/core`.
Examples use `cache` from `arkoren.dev/core/service/cache`, standard-library
`time`, and an operation context `ctx`. Focused snippets keep error results
visible but omit routine propagation; handle those errors in your application.

## Compatibility Matrix

Atomicity below applies to one operation on the active backend, not exactly-once
effects across retries. Shared state means callers use the same entries,
counters, and leases. Restart columns describe state retention, not guaranteed
availability or durability.

| Driver | Shared State | Expiry | Counters | Atomic Pull | Guarded Writes | Leases | App Restart | Backend Restart |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No — Only callers sharing one driver instance coordinate. | Yes | Yes — Checked int64 arithmetic under one mutex. | Yes | Yes — Guard and destination share one mutex. | Yes — Within one driver instance. | No | No |
| [Redis](#redis) | Yes — Same server, database, and physical names. | Yes | Yes — Exact int64 results; counter TTLs round up to milliseconds. | Yes — Requires Redis 6.2 or later. | Yes — One Lua execution; positive TTLs round up to milliseconds. | Yes — Expiring token-owned entries. | Yes — Application restart does not clear Redis. | Partial — Depends on server persistence and recovery settings. |

Typed keys and namespaces are facade definitions, not native storage partitions.
Their [literal names and expiry policies](keys.md) work with both drivers.
Neither provider promises retention for entries without expiry. Redis can evict
or lose data; memory loses all data when its process exits.

Both providers reject counter arithmetic outside `int64` without changing the
target or TTL or creating an absent target. Error identities are backend-native.
Both support atomic `Pull`, `IncrementWithTTL`, and same-key or different-key
`CompareAndPut`. A [lease](locks.md) can expire while its holder still works;
none of these capabilities makes later application side effects exactly once.

## Memory

Memory is included in `arkoren.dev/core`; no additional driver module is required.
Use it for tests, local development, or replaceable data that each process can
keep independently.

### Creating Stores

Import the bundled driver and wrap it with the facade:

```go
import "arkoren.dev/core/service/cache/memorycache"

driver := memorycache.New()
store := cache.New(driver)
```

Construction cannot fail and starts no goroutines. The store is immediately
usable; follow the [report write and read](overview.md#reading-values) with it.
Expired entries are never returned, regardless of whether cleanup has run.
Raw driver byte inputs and outputs are copied, so callers can mutate their own
slices without changing stored bytes.

### Expiry Cleanup

The driver has no size limit or capacity-based eviction. Give replaceable values
a TTL and account for live entries when sizing the process. By default, writes
sweep abandoned expired entries at most once per minute. To change that interval,
use an alternative construction:

```go
driver := memorycache.New(func(store *memorycache.Builder) {
	store.Cleanup(5 * time.Minute)
})
```

Zero selects the one-minute default. A negative interval disables both inline
and background sweeping; expired entries are then removed only when accessed.
Cleanup reclaims memory but does not change expiry correctness.

If an idle cache must reclaim expired entries without subsequent writes, register
the optional janitor. With an existing `composer` of type
`*application.Composer` from `arkoren.dev/core/application`, register ownership
before startup:

```go
composer.Cleanup(driver.Stop)
composer.Startup(driver.Start)
```

`Start` runs the janitor at the cleanup interval; `Stop` ends it. Without `Start`,
there is no janitor to stop. See [Lifecycle and Ownership](../application/lifecycle-and-ownership.md)
for registration and shutdown ordering.

### Atomic Updates

Use the shared [counter](counters.md), [guarded-write](overview.md#guarded-writes),
and [lease](locks.md) examples with this store. Their atomicity applies only to
callers sharing this driver instance. Two replicas with independent memory caches
each receive their own rate-limit allowance and can each acquire the same named
lease.

Counter overflow wraps `strconv.ErrRange` and leaves the value and expiry
unchanged. Subtraction supports `math.MinInt64` directly without overflowing
negation; do not assume memory's overflow error identity for other drivers.

For every function and option, see the [`memorycache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache/memorycache).

## Redis

Redis keeps entries outside the application process. Use it for shared sessions,
counters, and cached results that should outlive an application deploy. You need
network access to a Redis 6.2+ server; `Pull` uses `GETDEL`, which requires that
version. The driver wraps a standalone `*redis.Client`, not a Redis Cluster client.
Adding application replicas does not increase Redis capacity or configure high
availability.

### Installing Redis

Install the driver module:

```sh
go get arkoren.dev/driver/redis
```

This installs the Go adapter, not the Redis server. For a disposable development
server, use [Running Locally](#running-locally).

### Connecting Stores

Point the driver at your existing service, then construct the facade:

```go
import "arkoren.dev/driver/redis/rediscache"

driver := rediscache.New(func(store *rediscache.Builder) {
	store.Addr("cache.internal:6379")
})
store := cache.New(driver)
```

`New` returns a driver without error or network I/O. Defaults are TCP,
`localhost:6379`, database `0`, empty ACL credentials, and no TLS. `Network("unix")`
instead treats `Addr` as a socket path. An empty network or address restores
its default after callbacks.

With an existing `*application.Composer` named `composer`, register cleanup and
startup:

```go
composer.Cleanup(driver.Stop)
composer.Startup(driver.Start)
```

`Start` pings Redis so connection and authentication failures reject startup.
Use `driver.Ping` for a later [readiness check](../application/health-and-readiness.md).
`Stop` closes the client pool, even with a canceled cleanup context; it does not
wait for application work. Finish cache users before closing their driver.
The same server, logical database, and physical names let other processes read
your [typed writes](overview.md#reading-values).

### Securing Connections

For an authenticated TLS service, take credentials from application configuration,
not source code. This alternative constructor assumes an existing password
named `password` and standard-library `crypto/tls`:

```go
driver := rediscache.New(func(store *rediscache.Builder) {
	store.Addr("cache.internal:6379")
	store.Username("reports")
	store.Password(password)
	store.DB(1)
	store.TLS(&tls.Config{MinVersion: tls.VersionTLS12})
})
```

Configure certificate trust for your service; do not disable verification to
make a connection succeed. Native TLS values remain shared, not deep-cloned.
The diagnostic `rediscache.Config` is not constructor input. Its `String`,
`GoString`, and structured logging methods redact the password, but reading its
`Password` field exposes the secret. Unlike owned cache keys, it does not promise
redaction for every unsupported format verb.

### Native Clients

If you need native timeouts, retry settings, or pool configuration beyond the
builder, construct the client first. Import `redis` from
`github.com/redis/go-redis/v9`:

```go
client := redis.NewClient(&redis.Options{
	Addr:                  "cache.internal:6379",
	ContextTimeoutEnabled: true,
	ReadTimeout:           2 * time.Second,
	WriteTimeout:          2 * time.Second,
	MaxRetries:            -1,
})
driver := rediscache.NewFrom(client)
```

This variant disables native command retries and transfers ownership of the
initialized, non-nil client. `NewFrom` has no construction callbacks, makes no
connection, and preserves client settings. `Stop` closes that exact client;
do not share it with code that must outlive the driver, and arrange one shutdown
owner.

`New` uses native transport timeout/retry defaults and enables
`ContextTimeoutEnabled` so operation deadlines apply during socket I/O.
`NewFrom` does not enable that option for you. Use bounded operation contexts;
context cooperation does not interrupt arbitrary application loaders or force
noncooperative drivers to stop. Neither facade construction nor its telemetry
name adds a request timeout.

Ordinary native retry policies can repeat effects after uncertain network
failures. `CompareAndPut` specifically disables replay of possibly executed
guarded writes, even if native retries are enabled. A lost reply is an error
with an unknown outcome, not proof that no write occurred or permission to
fall back to an unconditional write.

### Native Access

For connectivity diagnostics outside the portable contract, use the underlying
client without changing stored data:

```go
status, err := driver.Client().Ping(ctx).Result()
```

On success, `status` is `"PONG"`. The native client shares the driver's owned
pool. If you only have the facade, `store.DriverAs[*rediscache.Driver]()` returns
the first matching driver through wrappers that expose `Unwrap() cache.Driver`;
check its boolean result before use. `store.Driver()` exposes raw portable
operations with literal names, bytes, and explicit TTLs.

Native and raw calls bypass facade telemetry, typed-key validation, and JSON
encoding. Do not overwrite entries with incompatible encodings or write into
the reserved `lock:` key space. Configure native options before ownership
transfer, not by mutating a running client's options.

### Storage Limits

An application restart does not clear Redis. Backend restart survival depends
on Redis persistence and recovery settings; do not assume survival. Expiry,
eviction, deletion, failover, and restoration can affect both cached values and
coordination records. Atomicity on the active server is not durable retirement
of a security record across restored earlier data.

Counter scripts return decimal text to preserve the entire `int64` range.
`Decrement` with `math.MinInt64` uses one exact-decimal script because native
`DECRBY` rejects that delta even for representable results. Native overflow errors
leave the target value and expiry untouched. The adapter does not emulate
arithmetic with client-side read/check/write calls.

`TTL` reads millisecond `PTTL` directly, retaining subsecond lifetimes. Zero
means no expiry; missing or expired entries return `cache.ErrNotFound`. Remaining
lifetimes beyond `time.Duration` saturate at its maximum. Positive
`IncrementWithTTL` and guarded-write TTLs round up to whole milliseconds without
overflowing; counter lifetime reporting also saturates.

Guarded writes compare and replace with one Lua execution, including distinct
guard and destination keys. Leases acquire with `SET NX` and release or extend
with token-checking scripts. These protect individual cache operations, not
external side effects. Review [counter retry limits](counters.md#handling-failures)
and [lease safety](locks.md) before relying on coordination.

### Running Locally

With Docker and Compose installed, save this as `compose.yaml` in a new empty
development directory. Do not overwrite an existing application configuration.
The unauthenticated service binds only to loopback; never expose it to an
untrusted network:

```yaml
services:
  cache:
    image: redis:7.4-alpine
    ports:
      - "127.0.0.1:6379:6379"
```

Start it and verify connectivity:

```sh
docker compose up -d
docker compose exec cache redis-cli ping
```

The check prints `PONG`; retry it if Redis is still starting. Use
`rediscache.New()` for the default local address, start the driver, and follow
the [write and read example](overview.md#reading-values). When finished, run
`docker compose down`. This disposable configuration does not preserve cache
data.

For every function and setting, see the [`rediscache` API reference](https://pkg.go.dev/arkoren.dev/driver/redis/rediscache).
