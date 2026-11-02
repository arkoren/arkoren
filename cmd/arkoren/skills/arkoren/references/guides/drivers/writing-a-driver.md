---
title: Writing a Driver
description: Implement an application-owned service driver or wrapper with explicit behavior, resource ownership, and contract tests.
order: 20
summary:
  - Implement an application-owned driver or wrapper against a public service contract.
  - Preserve atomic operations, error identities, native access, and resource ownership.
  - Publish construction settings and application-owned migrations without runtime schema changes.
  - Test portable behavior with conformance suites and add backend-specific coverage.
---

# Writing a Driver

If your backend is not among the [available drivers](choosing-a-driver.md), you
can implement its service contract in your own Go module. A driver translates
that contract into backend operations; the facade continues to handle typed
application calls and its ordinary events and telemetry.

A wrapper is a driver too. It forwards to another implementation while changing
one responsibility, such as key naming. Neither kind requires an Arkoren
checkout or a contribution to the toolkit.

## Contract First

Start with the public interface and its method comments. For the cache example,
inspect [`cache.Driver`](https://pkg.go.dev/arkoren.dev/core/service/cache#Driver):

```sh
go doc arkoren.dev/core/service/cache.Driver
```

The contract describes missing values, expiry, cancellation, byte ownership,
and atomic operations. Its test package checks many of those rules, but tests
are evidence of covered behavior, not a replacement for the contract.

Decide whether you need a whole driver before implementing one. If an existing
driver already handles ordinary calls and you only need a provider-specific
operation, use its [native client access](../core-concepts/facades-and-drivers.md#native-client-access).
For cache naming alone, [typed keys and namespaces](../cache/keys.md) can avoid a
wrapper. A wrapper is useful when an existing raw-driver integration needs the
same naming rule applied to every operation.

## Wrap Existing Behavior

Suppose your application needs a fixed prefix on a raw cache connection. Create
an `internal/prefixcache` package in your application module. The following
fragments use `context` and `arkoren.dev/core/service/cache`; they illustrate
selected methods, not a complete `cache.Driver` implementation.

Keep the underlying driver private rather than embedding it. Embedding would
silently forward methods you forgot to adapt, potentially writing unprefixed
keys. A constructor with no optional settings needs no construction builder:

```go
type Driver struct {
	next   cache.Driver
	prefix string
}

func New(next cache.Driver, prefix string) *Driver {
	return &Driver{next: next, prefix: prefix}
}
```

This wrapper borrows `next`: its caller owns that driver's startup and shutdown.
Choose a complete, unambiguous prefix such as `reports:tenant-42:` and encode
variable identities consistently. Prefixing is not authorization or isolation
from code that can access the underlying driver.

For reads, change the key and leave the returned value and error semantics to
the underlying implementation:

```go
func (driver *Driver) Get(ctx context.Context, key string) ([]byte, error) {
	return driver.next.Get(ctx, driver.prefix+key)
}
```

Forward every interface method, applying the same prefix to every key or lock
name. Keep values, lock tokens, durations, and counter deltas unchanged. In
particular, `IncrementWithTTL` must use the prefixed identity too, or counter
windows would disagree with ordinary reads.

### Guarded Writes

A guarded write can compare one key and replace another. Transform both keys
before forwarding the *single atomic operation*:

```go
func (driver *Driver) CompareAndPut(ctx context.Context, match cache.Match, entry cache.Entry) (bool, error) {
	match.Key = driver.prefix + match.Key
	entry.Key = driver.prefix + entry.Key
	return driver.next.CompareAndPut(ctx, match, entry)
}
```

The records are passed by value, so changing their key fields does not change
the caller's records. A mismatch returns `false, nil` without changing either
entry or its TTL. Do not replace the call with a read followed by a write:
another writer could change the guard between them. Preserve `ErrUnsupported`
when the underlying driver cannot provide the required atomicity, and preserve
uncertain transport errors rather than falling back to an unconditional write.
[Guarded Writes](../cache/overview.md#guarded-writes) shows the application use.

### Native Access

Expose the wrapped contract through `Unwrap` so the facade's `DriverAs` helper
can search through your wrapper:

```go
func (driver *Driver) Unwrap() cache.Driver {
	return driver.next
}
```

Native calls bypass your prefix transformation and the facade's operation
instrumentation. Give callers that escape hatch deliberately, not as a promise
that native operations obey wrapper policy.

After implementing all methods, add a compile-time interface check:

```go
var _ cache.Driver = (*Driver)(nil)
```

This catches missing methods and incorrect signatures. It cannot detect a
forgotten prefix or a non-atomic implementation; those need behavioral tests.

Your application can then pass the completed wrapper to the ordinary facade.
With an existing backend named `backend` whose lifecycle you already own:

```go
store := cache.New(prefixcache.New(backend, "reports:tenant-42:"))
```

Facade calls now use the wrapper's naming rule without changing application
key definitions. Keep the wrapper chain acyclic so `DriverAs` can finish its
search.

## Adapt a Backend

A backend driver performs the same contract operations through a vendor client.
Preserve distinctions that affect recovery: missing data is not an outage, a
rejected comparison is not a failed request, and a request error does not always
prove the backend performed no write.

For example, a Redis-backed `Get` can map native absence to `cache.ErrNotFound`
without hiding other failures. With a private `client *redis.Client` field,
import `errors` and `github.com/redis/go-redis/v9` in addition to the cache and
context packages:

```go
func (driver *Driver) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := driver.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, cache.ErrNotFound
	}
	return value, err
}
```

Callers can distinguish absence with `errors.Is`. A connection failure remains
a connection failure. If you wrap errors for context, preserve their identity
with `%w`; never include credentials or cached contents in the message.

The remaining implementation must satisfy the whole interface. For cache, that
includes independent byte copies, concurrent calls, cancellation before
mutation, nonmutating integer overflow, and atomic counters, pulls, and lock
ownership checks. Backend transactions, scripts, or native atomic commands may
be necessary. An unsupported result is valid only where the contract permits
it; do not advertise a capability your backend cannot implement safely.

## Construction and Ownership

Keep configuration separate from runtime operations. Put required values with
no safe default in constructor arguments. For optional settings, expose a
construction draft named `Builder` and its matching alias:

```go
type BuilderFunc = func(*Builder)
```

Use `New(required..., configure ...BuilderFunc)`. Invoke supplied callbacks
directly, synchronously, once in order on one fresh draft before ordinary
validation. A supplied nil callback panics; an expanded nil or empty slice adds
no edits. Draft methods return that draft. Publish detached private settings so
a retained draft or input container cannot reconfigure the driver. Native
clients, functions, and their captures need explicit ownership rules rather
than a claim of recursive copying.

The shipped Redis cache driver demonstrates this public shape. Import
`arkoren.dev/driver/redis/rediscache`, installed as described in
[Cache Drivers](../cache/drivers.md#redis):

```go
var connection rediscache.BuilderFunc = func(store *rediscache.Builder) {
	store.Addr("cache.internal:6379")
}
backend := rediscache.New(connection)
```

This constructs a driver without contacting Redis. `Start(ctx)` checks
connectivity, `Ping(ctx)` provides a readiness check, and `Stop(ctx)` closes its
client. Your application registers lifecycle work; the driver does not register
itself. [Lifecycle and Ownership](../application/lifecycle-and-ownership.md)
explains the wiring.

### Native Resources

If you offer `NewFrom(client)`, document whether it borrows or takes ownership
and which optional settings still apply. Settings-free conversions need no
callback. Do not infer lifetime from the constructor name: Redis cache
`NewFrom` transfers ownership and `Stop` closes the supplied client, whereas
Redis broadcasting borrows its supplied client.

Configure native connection policy before sharing or transferring a client.
An accessor such as `Client()`, or `DB()`/`SQL()` for database drivers, exposes
native operations without transferring ownership. Explain when that accessor
is usable and whether native calls bypass facade or transaction behavior.

Acquire connections and start background loops at the documented lifecycle
boundary, not incidentally in ordinary configuration callbacks. Cloud SDK
configuration and credential loading can still require a constructor context.
Release owned resources even if the stop context has expired; document whether
shutdown waits for active work and how long the caller controls that wait.
Borrowing a shared resource must not let one driver close it behind another.

Keep credentials out of diagnostic output. Redact any exported settings record
in its `String`, `GoString`, and `slog.LogValuer` implementations, and avoid
logging native clients, payloads, or raw configuration. Redacting your record
does not sanitize native errors or caller-owned SDK objects.

### Schema Ownership

If the driver needs database tables, expose `Migrations()` for the application's
migrator. Do not run DDL during construction, startup, or the first operation.
The application applies schema changes through its
[migration deployment](../migrations/deployment.md) process. Readiness can report
missing tables without trying to create them.

## Check the Contract

Once the prefix wrapper implements the complete interface, run the cache suite
against it over a fresh memory driver. In your application's wrapper test,
import `testing`, the cache package and its `cachetest` and `memorycache`
subpackages, plus your own `internal/prefixcache` package:

```go
func TestPrefixContract(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Driver {
		return prefixcache.New(memorycache.New(), "reports:tenant-42:")
	})
}
```

The cache suite creates drivers per subtest and runs cases in parallel. This
memory driver needs no `Start` for reads or expiry, and no janitor is started
here. Add wrapper-specific tests that inspect underlying keys: a consistently
unprefixed wrapper could pass the portable suite while failing its own purpose.

For a vendor driver, initialize the required backend in the factory and register
cleanup on its supplied `*testing.T` before returning. Do not defer shutdown
inside the factory: it would close the driver before the suite uses it. If the
backend controls expiry, call the suite with `cachetest.WithExternalClock()`.
Use isolated disposable resources for [integration tests](../testing/integration-tests.md).

Other suites have different factory shapes, lifecycle needs, and capability
options. Follow [Driver Conformance](../testing/conformance-suites.md), especially
its [coverage limits](../testing/conformance-suites.md#interpret-coverage), before
treating a pass as evidence. Test backend failures and application-specific
behavior separately; suite options do not relax required semantics.

For the interface and test entry points used here, see the
[`cache` API](https://pkg.go.dev/arkoren.dev/core/service/cache) and
[`cachetest` API](https://pkg.go.dev/arkoren.dev/core/service/cache/cachetest).
