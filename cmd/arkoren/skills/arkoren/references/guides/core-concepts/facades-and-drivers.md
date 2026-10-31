---
title: Facades and Drivers
nav: Facades & Drivers
description: How a service is split into a contract, a facade your code uses, and drivers per vendor; swapping drivers and reaching the vendor client.
order: 10
---

# Facades and Drivers

## Introduction

Your handlers should not need to change when a local cache moves to Redis or
a test replaces a mail provider. Arkoren separates the service your code uses
from the backend that performs the work. Most services, including the cache,
database, queues, mail, and file storage, use the same three parts.

A **contract** is a Go interface named `Driver` that describes the operations
a backend must provide, in the simplest form that every vendor can implement.
The cache contract, for example, stores and returns raw bytes under a key.

A **facade** is the type your application uses. It wraps a driver and adds
everything that should behave the same no matter which vendor sits behind it:
typed values instead of bytes, validation, telemetry, and events. The cache
facade, `*cache.Cache`, lets you store a Go struct and read it back as the same
type.

A **driver** implements the contract for one vendor. `memorycache` keeps
entries in process memory, and `rediscache` stores them in Redis. Drivers
know how to talk to their vendor and nothing else.

Because your code depends only on the facade, choosing a vendor is a decision
you make in one place, in `main`. Portable calls can stay the same when you
change drivers; configuration, lifecycle, durability, and supported operations
still need review.

## Using a facade

Create a facade by passing a driver to the service's `New` function. This
cache keeps its entries in memory. Import the facade and memory driver:

```go
import (
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)
```

Create one store and pass it to the code that needs it:

```go
store := cache.New(memorycache.New())
```

From then on, you work with the facade. It provides the operations you
actually want, in terms of your own types. Here, a `Profile` struct is cached
for ten minutes and read back. Here `profile` is your application-owned `Profile`
value and `ctx` is the operation context; import `time` for the TTL:

```go
key := cache.NewKey[Profile]("profiles.42", func(entry *cache.KeyBuilder) {
	entry.TTL(10 * time.Minute)
})
err := store.Put(ctx, key, profile)

cached, err := store.Get(ctx, key)
```

The facade turns the struct into bytes for the driver and back again, and
records the operation when telemetry is enabled. Every driver
gets the same behavior, because none of it lives in the driver.

Drivers map backend results to errors the contract defines. A missing
cache entry is always reported as an error wrapping `cache.ErrNotFound`,
whether the driver is the memory driver or Redis. Import `errors` to classify it:

```go
if errors.Is(err, cache.ErrNotFound) {
	// not cached yet
}
```

Pass the facade, not the driver, to the code that needs it. A store or a
handler that receives a `*cache.Cache` works with any cache driver, including
the memory driver in its tests.

## Choosing and swapping drivers

The driver is chosen in one line, where you build the facade. To move the
cache from memory to Redis, install `arkoren.dev/driver/redis` in your module
and import `arkoren.dev/driver/redis/rediscache`, then change that construction:

```go
driver := rediscache.New(func(store *rediscache.Builder) {
	store.Addr("cache.internal:6379")
})
store := cache.New(driver)
```

Portable calls using `store` stay the same. This is the reason to start a new service on
memory drivers: you can build and test every feature before you provision any
infrastructure, and switch to real drivers one at a time.

A driver that talks to a server also has a lifecycle. Register it on an existing
`app *application.Application`, as introduced in
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md), alongside the facade:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	composer.Check("cache", driver.Ping)
}
```

`Start` connects, `Stop` releases the connection, and `Ping` reports whether
the server is reachable for the readiness probe. This Redis constructor does
not connect; see [Lifecycle and ownership](../application/lifecycle-and-ownership.md)
for the distinction between construction and startup. Register only
methods the selected driver provides. For example, `memorycache` has `Start`
and `Stop` but no `Ping`; `memorysearch` has none of the three. Stateless HTTP
drivers may perform I/O on each call without a `Start` method.

[Choosing a driver](../drivers/choosing-a-driver.md) compares the drivers
available for each service.

## Contracts with several roles

Some services split their contract into several interfaces, because the two
sides of the service are often run by different processes, or by different
vendors. Queues have a `DispatcherDriver` that puts jobs on a queue and a
`ConsumerDriver` that takes them off. Broadcasting has a `PublisherDriver` and
a `ConsumerDriver`. Telemetry has separate drivers for traces, metrics, and
logs.

A service that only sends jobs needs only a dispatching driver, and a worker
that only processes them needs only a consuming one. Most queue drivers
implement both roles, so a single driver can serve both sides when they run in
the same process. Each service's guide explains its roles.

## Unsupported operations

A driver contract lists the backend operations, and each implementation has all
of its methods. Facades can add helpers implemented in terms of those methods.
There are no optional capability interfaces to detect, but supported operations
and provider-specific limits can still differ.

A vendor that cannot do one of the operations returns the contract's
`ErrUnsupported` from that method. The cache contract, for example, has an
`IncrementWithTTL` operation: an increment that also sets an expiry, in one
atomic step. The rate limiter depends on it, so a cache driver must either
implement it atomically or return `cache.ErrUnsupported`, in which case the
limiter reports the error instead of counting unsafely. Check for the error
with `errors.Is(err, cache.ErrUnsupported)` when you want to choose another
path.

## Reaching the vendor client

A facade covers what every vendor can do. When you need something specific to
one vendor, such as a Redis command that has no cache equivalent, you can
reach the vendor's own client. Every driver that wraps a client exposes it
with a typed method, such as `Client()` on the Redis drivers or `DB()` on the
SQL drivers.

If you only have the facade, ask it for the driver with `DriverAs`, which
finds a driver of the type you ask for:

```go
if backend, ok := store.DriverAs[*rediscache.Driver](); ok {
	client := backend.Client() // *redis.Client
}
```

`DriverAs` also looks through drivers that wrap other drivers. A driver that
adds behavior around another one, such as prefixing keys, exposes the driver
it wraps with an `Unwrap` method, and `DriverAs` follows that chain until it
finds a match. When the facade's driver is not of the requested type, `ok` is
false and your code can fall back to the portable path.

> [!NOTE]
> Code that reaches the vendor client is tied to that vendor. Keep it in one
> place, and prefer the facade wherever it does what you need, so that
> backend-specific code stays separate from portable calls. Review that code
> when swapping drivers.

## Wrapping a client you already have

Sometimes a vendor client already exists in your service, for example a Redis
client shared with another library. Drivers that wrap a client accept it with
`NewFrom` instead of building their own:

```go
driver := rediscache.NewFrom(client)
```

Here `client` is your existing `*redis.Client`. The driver uses its connection
pool. Check the driver's section in its service guide for who closes the client.
The Redis cache driver closes even a supplied client in `Stop`; the Redis
broadcast driver closes only a client it created itself. If several components
share a client, assign one owner to close it and keep it open until all borrowers
have stopped.

## Several instances of one service

Nothing prevents you from creating several facades of the same service, each
with its own driver. A service might keep sessions in one Redis database and
cached pages in another:

```go
sessions := cache.New(rediscache.New(func(store *rediscache.Builder) {
	store.DB(0)
}), func(store *cache.Builder) {
	store.Name("sessions")
})
pages := cache.New(rediscache.New(func(store *rediscache.Builder) {
	store.DB(1)
}), func(store *cache.Builder) {
	store.Name("pages")
})
```

`cache.Builder.Name` labels each facade in telemetry, so the two stores appear as
separate series in your metrics. Each driver needs its own lifecycle hooks.

## Learn more

[Contracts](contracts.md) describes what every contract promises and how the
conformance suites hold drivers to it. To implement a contract for a vendor
that Arkoren does not support yet, see
[Writing a driver](../drivers/writing-a-driver.md).

For the facade used in these examples, see the [`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
