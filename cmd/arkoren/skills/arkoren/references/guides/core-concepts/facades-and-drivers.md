---
title: Facades and Drivers
nav: Facades & Drivers
description: Separate application-facing service calls from backend selection, configuration, and ownership.
order: 10
summary:
  - Use a typed facade while its driver performs backend operations under a shared contract.
  - Keep cache entry identity, expiry, and result types consistent across reads and writes.
  - Review backend capabilities and resource ownership when replacing a driver or accessing its native client.
---

# Facades and Drivers

A report handler should not need Redis commands to cache a report's status.
Arkoren separates the service your code calls from the backend that stores the
data. You select the backend when constructing the service, then pass the
application-facing object to its consumers.

## Three Roles

A **facade** is the application-facing service, such as `*cache.Cache`. It adds
shared behavior around backend operations: the cache facade encodes Go values
as JSON, decodes reads, and records telemetry when enabled.

A **driver** performs the backend operations. For example, `memorycache` stores
bytes in the current process, while `rediscache` sends commands to Redis. A
[contract](contracts.md) defines the interface and behavioral rules between the
facade and driver, including expiry, copying, atomicity, and errors.

```text
Your handler -> Cache facade -> Cache driver -> Stored bytes
               typed values    raw operations
```

This separation does not make every backend equivalent. Portable service calls
can remain unchanged when you replace a driver; connectivity, supported
operations, sharing, and durability still need review.

## Typed Service Calls

For a process-local cache, import `arkoren.dev/core/service/cache` and
`arkoren.dev/core/service/cache/memorycache`, then construct the facade:

```go
driver := memorycache.New()
store := cache.New(driver)
```

The facade uses that driver; constructing another facade does not create a
separate backend unless you also supply a separate driver. Pass `store` to the
handlers or services that need caching, rather than making them select drivers.

To cache a report status, define the value your application wants to read:

```go
type ReportStatus struct {
	Ready bool `json:"ready"`
}
```

A `cache.Key[T]` associates a physical entry name with a Go value type and write
policy. Import `time` and give this entry a ten-minute lifetime:

```go
key := cache.NewKey[ReportStatus]("reports:42:status", func(entry *cache.KeyBuilder) {
	entry.TTL(10 * time.Minute)
})
```

With the operation's `ctx context.Context`, write the value through the facade:

```go
err := store.Put(ctx, key, ReportStatus{Ready: true})
```

On success, the driver holds JSON bytes with the configured expiry. A key with
no TTL setting has no expiry; reading it does not renew its lifetime.

The same key determines the read's result type:

```go
status, err := store.Get(ctx, key)
```

On success, `status` is a `ReportStatus`. Missing and expired entries return an
error matching `cache.ErrNotFound`; malformed stored JSON returns a decoding
error. The Go type does not create a separate storage namespace: two keys with
the same name address the same entry even if their type arguments differ. Keep
the type consistent for each name; [Cache Keys](../cache/keys.md) explains key
identity and shared prefixes.

## Backend Changes

To move this cache to Redis, use the installation, connection, and lifecycle
instructions in [Cache Drivers](../cache/drivers.md). The composition boundary
supplies a `rediscache.Driver` instead of a `memorycache.Driver`; the `Put` and
`Get` calls above stay the same.

An in-memory cache belongs to one process and loses its contents on restart.
Redis can provide a shared backend, but its persistence depends on your Redis
deployment. Choosing a driver does not migrate existing entries.

Construction is not a connectivity check. The Redis cache constructor performs
no network I/O; `Start` checks connectivity, and `Stop` closes its client. Other
drivers have different lifecycle requirements. Register only the methods the
selected driver supplies, under one owner, using
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md).

Several facades can coexist. For example, a service can use one cache for
sessions and another for report status. The cache facade's `Name` setting labels
telemetry; it does not isolate entries. Use separate backend configuration or
explicit key prefixes when you need separate data.

## Separate Roles

Some services separate the sending and receiving sides of their contract.
Queues have `DispatcherDriver` and `ConsumerDriver`; broadcasting has
`PublisherDriver` and `ConsumerDriver`. Telemetry has separate trace, metric,
and log driver interfaces.

A web process that only dispatches jobs needs the dispatcher role, not a running
consumer. A worker needs the consumer role. One concrete driver can implement
both, but the service guide determines which facade and lifecycle each process
needs.

## Unsupported Operations

A method's presence does not establish backend support. Contracts can specify
an `ErrUnsupported` result when a backend cannot provide the required semantics.
Read the service's driver comparison before depending on such an operation.

For example, `cache.Driver.IncrementWithTTL` must update the counter and expiry
atomically. A backend that cannot do that returns `cache.ErrUnsupported`; it
must not substitute separate reads and writes. The rate limiter propagates
that failure rather than counting unsafely. Use `errors.Is` to classify a
documented sentinel, and do not weaken an atomic operation as a fallback.

## Native Client Access

If a required operation exists only in the vendor API, keep that dependency in
a narrow backend-specific part of your application. With a Redis-backed `store`,
import `fmt` and `arkoren.dev/driver/redis/rediscache` to print the native pool's
current connection count:

```go
if backend, ok := store.DriverAs[*rediscache.Driver](); ok {
	fmt.Println(backend.Client().PoolStats().TotalConns)
}
```

`DriverAs` finds the requested concrete type, including through wrappers that
expose `Unwrap() Driver`. It returns `false` when no match exists. Direct driver
and native-client calls bypass facade behavior, including its telemetry, and
must be reviewed when changing backends.

If you already have a native client, check the driver's `NewFrom` ownership
contract before wrapping it. Redis cache's `NewFrom` transfers ownership:
`Stop` closes the supplied client too. Other drivers can borrow clients instead.
Never infer ownership from the constructor name or close a borrowed client
while another component still needs it.

For the facade and accessors used here, see the
[`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
