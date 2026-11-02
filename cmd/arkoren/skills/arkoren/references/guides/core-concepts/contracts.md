---
title: Contracts
description: Understand the behavioral guarantees, error identities, and verification boundaries behind a service interface.
order: 20
summary:
  - Read behavioral guarantees alongside driver method signatures.
  - Distinguish missing data, cancellation, unsupported operations, and uncertain write outcomes.
  - Understand what conformance suites, fakes, and in-process drivers establish in tests.
---

# Contracts

Two caches can expose the same `Get` method yet disagree about expiry, returned
bytes, or missing entries. A **contract** combines a service's driver interface
with its documented behavioral rules. Those rules tell you what application
code can rely on when selecting or replacing a driver.

## Behavioral Guarantees

Read the method documentation as well as its signature. The
[`cache.Driver` contract](https://pkg.go.dev/arkoren.dev/core/service/cache#Driver),
for example, establishes that:

- Zero write TTL means no expiry; a negative TTL fails with `ErrInvalidTTL`.
- Expired entries behave like missing entries.
- `Get` reports a missing entry with `ErrNotFound`, while `Has` returns `false`
  and deleting a missing entry succeeds.
- Stored and returned byte slices are independent copies, so caller mutation
  does not change cached data.
- An already-canceled context prevents the operation and leaves the cache
  unchanged.

These are distinct promises. A successful read does not imply that an entry
never expires, and a successful deletion does not imply that an entry existed.
The contract gives each result a meaning your code can use.

The [facade](facades-and-drivers.md) adds its own documented behavior. Cache
`Get` decodes driver bytes into the key's Go type, so it can fail on invalid
JSON even after the driver successfully finds an entry. That is not a cache
miss; the stored entry remains unchanged.

## Error Identity

Classify documented conditions with `errors.Is`, not an error string. Drivers
can wrap a sentinel with additional context without changing its identity.
Sentinels belong to their service: a cache miss and a missing database row do
not share one universal not-found error.

Suppose `store` is an existing `*cache.Cache`, `key` is a `cache.Key[ReportStatus]`
as in [Typed Service Calls](facades-and-drivers.md#typed-service-calls), and `ctx`
is the operation context. Import `errors` and
`arkoren.dev/core/service/cache` to distinguish absence from a failed read:

```go
value, err := store.Get(ctx, key)
switch {
case errors.Is(err, cache.ErrNotFound):
	// Load from the authoritative store instead.
case err != nil:
	return err
default:
	// Use the cached value.
}
```

Only the missing-entry branch establishes that the cache has no value for this
key. A connection failure or decoding error must not silently become a cache
miss. Decide explicitly whether your application can continue without caching.

Contracts do not give every possible backend failure a portable sentinel.
Preserve unexpected errors for the caller or operational reporting; do not
invent a classification from vendor error text.

## Context Boundaries

Pass the request or job's context to I/O operations so cancellation and
deadlines can reach the driver. Use `errors.Is(err, context.Canceled)` or
`errors.Is(err, context.DeadlineExceeded)` when cancellation affects your next
decision. Local hashing and encryption APIs do not take a context merely to
match this shape.

Distinguish an already-canceled operation from cancellation during a write.
The cache contract guarantees no mutation for the former. Once a command has
reached a remote backend, an error does not necessarily establish whether the
backend applied it. For example, cache `CompareAndPut` explicitly documents
that a network error can leave its outcome unknown. A deadline is not a
rollback guarantee.

## Atomic Operations

An **atomic operation** makes a decision and its associated change indivisible
to competing callers. Cache `Add`, for example, stores a value only if its key
is absent; a rejected addition leaves the existing value and TTL untouched.
Calling `Has` followed by `Put` is not equivalent: another caller can write
between the two calls.

When a contract permits `ErrUnsupported`, a driver unable to meet the required
semantics returns that error instead of an unsafe approximation. Do not
replace an unsupported atomic call with separate operations. Select another
backend or redesign the operation, as explained in
[Unsupported Operations](facades-and-drivers.md#unsupported-operations).

The portable contract is not a catalog of everything a vendor provides. Native
features accessed through a vendor client sit outside those portable guarantees.
Likewise, a shared API does not promise identical durability, deployment
requirements, or performance across drivers.

## Conformance Evidence

A **conformance suite** exercises a driver's behavior against its contract.
When writing or adapting a cache driver, call the public `cachetest.Run` suite
with a driver factory. In a `_test.go` file, import `testing`,
`arkoren.dev/core/service/cache`, and its `cachetest` and `memorycache`
subpackages:

```go
func TestCacheContract(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Driver {
		return memorycache.New()
	})
}
```

The factory is called for each subtest. The cache suite uses unique keys, so an
external backend need not start empty. It checks expiry, byte copying,
conditional writes, counters, cancellation, and concurrent access. Server-owned
expiry needs the suite's `WithExternalClock` option; local expiry can use
simulated time.

Passing the suite is evidence for the tested behavior, not proof about every
production configuration or failure mode. The interface documentation remains
binding even when a rule has no test. Follow
[Writing a Driver](../drivers/writing-a-driver.md) when implementing a backend,
or [Conformance Suites](../testing/conformance-suites.md) when integrating its
tests.

## Application Tests

A **fake** records requested effects so an application test can inspect them
without a production provider. For example, a queue fake can establish that
your code dispatched the expected job through the real facade. It cannot prove
that a production broker persists or redelivers that job.

An in-process driver serves a different purpose: it executes the service's
behavior locally. Use the service's testing guide to choose between recording
an effect and executing it. The workflow fake records commands and accepts
scripted outcomes without executing workflow bodies. The scheduling fake
records work and can explicitly drain due jobs through real handlers; it does
not run timers on its own. Use memory drivers when you need workflow execution
or automatic timer behavior. [Fakes](../testing/fakes.md) describes those
boundaries without requiring backend infrastructure.

For the concrete guarantees used throughout this page, see the
[`cache` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache).
