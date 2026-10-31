---
title: Writing a Driver
description: Implement a service contract for a new vendor, follow the constructor and lifecycle conventions, map errors, and pass the conformance suite.
order: 20
---

# Writing a Driver

## Introduction

Arkoren ships drivers for the vendors most services use, but not for every
one. When yours is missing, you can write the driver yourself: a Go type that
implements a service's contract, the `Driver` interface described in
[Contracts](../core-concepts/contracts.md). Once it does, the facade, the
middleware, and every piece of your application that uses the service work
with it unchanged.

There are two kinds of driver. A **vendor driver** talks to a backend, such as
Memcached for the cache or a mail provider's API for mail. A **wrapping
driver** sits in front of another driver and adds behavior, such as a key
prefix, encryption, or a circuit breaker, without knowing which driver it
wraps. Both use the same portable-behavior check, the contract's
**conformance suite**.

This page walks through both kinds, using the cache contract as the example,
and links to the separate contributor instructions if you want to submit it
to Arkoren. Application-owned drivers need no repository checkout.

## Before you start

Read the contract first. Its doc comments are the specification: they say what
each method must do with a missing key, an expired entry, a zero duration, or a
canceled context. The conformance suite in the `<service>test` package checks
the implementation against those rules. Its subtest names help you find tested
cases, but the interface documentation remains the specification even when a
rule has no test yet.

Check also whether you need a driver at all. A vendor feature that the
contract does not cover is usually better reached through an existing
driver's client, as described in
[Reaching the vendor client](../core-concepts/facades-and-drivers.md#reaching-the-vendor-client).

## Wrapping a driver

A wrapping driver can keep application calls unchanged while adding a key
namespace. For example, put a tenant-specific prefix in front of every cache
key before forwarding it. Add the driver to a package in your own module, such
as `internal/prefixcache`. Import `context`, `time`, and
`arkoren.dev/core/service/cache`. Start with the wrapper and its `Get` method:

```go
type Driver struct {
	next   cache.Driver
	prefix string
}

func New(next cache.Driver, prefix string) *Driver {
	return &Driver{next: next, prefix: prefix}
}

func (driver *Driver) Get(ctx context.Context, key string) ([]byte, error) {
	return driver.next.Get(ctx, driver.prefix+key)
}
```

Every other method of the contract follows the same pattern: change every key
the operation uses, then call the wrapped driver. The wrapper passes errors
through unchanged, so a
missing entry still wraps `cache.ErrNotFound`, as the contract requires.

Choose unambiguous prefixes, such as `tenant:42:`, and encode variable parts
consistently. Namespacing is not authorization: code with access to the underlying
driver can still read other tenants' keys.

These are focused fragments, not a complete driver: `Get` alone does not
implement `cache.Driver`. After forwarding the remaining methods, add this
compile-time check next to the type:

```go
var _ cache.Driver = (*Driver)(nil)
```

### Exposing the wrapped driver

Give the wrapper an `Unwrap` method that returns the driver it wraps:

```go
func (driver *Driver) Unwrap() cache.Driver {
	return driver.next
}
```

Facades use it in `DriverAs`, which follows the chain of `Unwrap` methods until
it finds the type it was asked for. Without it, code that needs the Redis
client behind your wrapper could not reach it.

### Forwarding every method

Every operation of the contract is a method of the driver interface, so a
wrapper must implement or forward all of them, including ones you do not care
about. A wrapper that embeds the wrapped driver forwards anything it does not
override, which is also how a method gets past your key changes: override every
method that takes a key, such as `IncrementWithTTL`, or the rate limiter on a
prefixed Redis cache would count under the unprefixed key:

```go
func (driver *Driver) IncrementWithTTL(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, time.Duration, error) {
	return driver.next.IncrementWithTTL(ctx, driver.prefix+key, delta, ttl)
}
```

`CompareAndPut` uses two keys: the guard and the destination. Prefix both before
forwarding the operation, even when they have the same original name:

```go
func (driver *Driver) CompareAndPut(ctx context.Context, match cache.Match, entry cache.Entry) (bool, error) {
	match.Key = driver.prefix + match.Key
	entry.Key = driver.prefix + entry.Key

	return driver.next.CompareAndPut(ctx, match, entry)
}
```

The records are passed by value, so these edits do not change the caller's
records. Forward the expected bytes, replacement bytes, and TTL unchanged.
Do not replace the atomic operation with `Get` followed by `Put`: another writer
can change the guard between those calls. A mismatch returns `false, nil` and
must leave both values and their TTLs unchanged; see
[guarded writes](../cache/overview.md#writing-only-while-an-observed-value-still-matches).

A wrapper never needs to check what the wrapped driver supports: if the vendor
cannot do the operation, its method returns `ErrUnsupported` and the wrapper
passes that error on.

## Writing a vendor driver

A vendor driver does the same job as a wrapper, but against a vendor's client
library. Its package exports the driver type, a construction builder, a constructor,
and any lifecycle methods its backend needs. Arkoren's Redis cache driver,
[`rediscache`](https://pkg.go.dev/arkoren.dev/driver/redis/rediscache), illustrates the caller-facing API.

### Configuration and constructors

Use [construction callbacks](../first-steps/configuration.md) on a builder with
private settings. Invoke callbacks directly once, synchronously in order on one
fresh draft before ordinary validation. Nil entries panic; expanded nil/empty
slices omit edits. Methods return the same draft. Publish completed private
settings and detach owned containers, so a retained draft cannot change the
driver. For a caller using the shipped Redis driver, install it with
`go get arkoren.dev/driver/redis` and import
`arkoren.dev/driver/redis/rediscache`. Its address is configured like this:

```go
driver := rediscache.New(func(store *rediscache.Builder) {
	store.Addr("cache:6379")
})
```

Settings without a safe default, such as an API key or a bucket name, are
arguments of `New` rather than builder settings, and `New` returns an error when
they are invalid. Constructors do not start workers or listeners. Cloud SDK
constructors retain their documented context and credential/configuration
loading. Application callback panics and supplied nil callbacks propagate;
ordinary configuration errors follow the constructor's error contract.
[Lifecycle and ownership](../application/lifecycle-and-ownership.md) explains the
separation between construction and startup.

`NewFrom` wraps a vendor client that the caller built, for applications that
already have one. Document which settings apply, since the client carries its
own connection settings, and who closes the client; some conversions take
ownership. Settings-free conversions take no callback. If one constructor
delegates to another, pass completed private settings rather than replaying
the caller's callbacks. Native SDK options configure the native client; they
are not Arkoren builder callbacks.

### Lifecycle

To detect an unreachable backend before serving traffic and release its resources
on shutdown, expose lifecycle methods for the application to register. For a
driver whose private `client` field is a `*redis.Client`, the methods are below.
Install `github.com/redis/go-redis/v9` in your module and import it as `redis`
when constructing that client; these methods also use `context`:

```go
func (driver *Driver) Start(ctx context.Context) error {
	return driver.Ping(ctx)
}

func (driver *Driver) Stop(context.Context) error {
	return driver.client.Close()
}

func (driver *Driver) Ping(ctx context.Context) error {
	return driver.client.Ping(ctx).Err()
}
```

`Start` connects, or checks that the backend is reachable, so a wrong address
fails the deployment rather than the first request. `Stop` releases everything
the driver holds, even when its context has already expired, and says in its
documentation what it waits for. `Ping` answers the readiness probe, so keep it
cheap. A driver never imports the `application` package and never registers itself:
the composition function wires the methods with `composer.Startup`, `composer.Cleanup`, and
`composer.Check`. Pass that function to `app.Run(ctx, Compose)`.

### Mapping errors

The contract defines what errors mean, and the facade relies on it. Translate
the vendor's errors into the contract's sentinels, wrapped so the cause is
kept where it helps. Import `errors` for this fragment inside your driver's
`Get` implementation:

```go
value, err := driver.client.Get(ctx, key).Bytes()
if errors.Is(err, redis.Nil) {
	return nil, cache.ErrNotFound
}
```

Return other vendor errors as they are, or wrapped with `%w` and the name of
the operation. Never turn an outage into a "not found", which would make the
application believe data is missing when the backend is only unreachable.

### Behavior the contract leaves to you

A few rules hold for every contract, even where its comments do not repeat
them:

- Every method is safe to call from many goroutines at once, because facades
  are shared across requests.
- Methods that take a context stop waiting when it is canceled and return its
  error.
- Byte slices passed in or returned are not kept or changed after the call
  returns. Copy them if the vendor library holds on to them.
- Leave ordinary operation events and telemetry to the facade, so every
  backend reports them consistently. Driver workers can still need their own
  operational logger; never log credentials or payloads.

### Exposing the vendor client

Give the driver an accessor for the vendor client, named `Client()`, or `DB()`
for SQL drivers, so applications can use vendor features the contract does
not cover.

### Keeping secrets out of logs

Connection strings, passwords, and API keys must not appear in logs. Avoid
logging native clients or their configuration structs. If your driver exposes
a diagnostic settings record, implement redacted `String`, `GoString`, and
`slog.LogValuer` output for that record. Redacting one record does not sanitize
caller-owned SDK objects or values captured by callbacks.

## Testing with the conformance suite

Run the contract's suite with a function that builds a fresh driver for each
subtest. Once `prefixcache` forwards the complete interface, test it over the
memory driver. In `internal/prefixcache/prefixcache_test.go`, use
`package prefixcache_test` and import `context`, `testing`,
`arkoren.dev/core/service/cache`, `arkoren.dev/core/service/cache/cachetest`,
`arkoren.dev/core/service/cache/memorycache`, and your module's
`internal/prefixcache` package:

```go
func TestConformance(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Driver {
		inner := memorycache.New()
		t.Cleanup(func() {
			if err := inner.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})

		return prefixcache.New(inner, "tenant:42:")
	})
}
```

A wrapper can be tested over a memory driver without starting external
infrastructure. A vendor driver needs its backend, so run the suite as an
[integration test](../testing/integration-tests.md), skipped with `-short`,
against a server started with Docker Compose. Vendor backends measure time
themselves, so pass the suite's external clock option, such as
`cachetest.WithExternalClock()`, and the suite waits in real time where it
otherwise uses a simulated clock.

Passing the suite provides evidence of portable behavior. When a subtest
fails, fix the driver. Suite options only describe real differences, such as
an external clock, and cannot redefine a required operation's semantics.
[Conformance suites](../testing/conformance-suites.md)
covers the suites in more detail.

## Contributing a driver to Arkoren

A driver written for your own service can live in any package of your module.
If you want to submit it to Arkoren, follow the separate
[contributor guide](../about/contributing.md) for repository layout, tests,
and documentation requirements.

For every contract, its suite, and its options, see the
[testing API reference](../testing/overview.md).
