---
title: Contracts
description: What a contract promises, how documented behavior and errors make drivers interchangeable, and how conformance suites and fakes enforce it.
order: 20
---

# Contracts

## Introduction

A facade can only give the same behavior on every vendor if every driver
behaves the same way underneath. A **contract** is what makes that possible.
It is the `Driver` interface of a service together with the rules written on
it: what each method must do, which errors it returns, how it treats the
context, and what it must never do. The interface alone is not enough. Two
caches can both have a `Get` method and still disagree about whether an
expired entry is an error. The contract settles that question once, for every
driver.

Use the contract when choosing a driver or relying on an edge case such as
expiry or cancellation. Its conformance suite checks those rules against an
implementation; you also use that suite when writing a driver of your own.

## What a contract contains

Take the cache contract as an example. Its `Driver` interface reads and writes
raw bytes, updates counters, and acquires leases. Its documentation states
the rules each operation must follow:

- A zero TTL means an entry does not expire, and a negative TTL is invalid and
  fails with an error wrapping `cache.ErrInvalidTTL`.
- An expired entry behaves exactly like a missing one.
- `Get`, `Pull`, and `TTL` report a missing key with an error wrapping
  `cache.ErrNotFound`; `Has` reports `false`, and `Delete` succeeds.
- Values passed to and returned from a driver are independent copies, so the
  caller can change them freely.
- When the context is already canceled, every operation fails with an error
  wrapping the context's error, and the cache is left unchanged.

Each method then adds its own rules. `Add` stores a value only when the key is
absent and leaves an existing entry and its TTL untouched. `Increment` creates
a missing key from zero, keeps the remaining TTL of an existing one, and fails
on a value that is not an integer.

None of these rules is surprising, but each is a place where two vendors would
naturally differ. Writing them into the contract means that code using the
cache can rely on them, whichever driver is configured, within its documented capabilities.

## Why contracts are small

A contract only contains what every reasonable backend can provide. The cache
contract has no tags, no pattern deletion, and no transactions, because many
cache servers cannot do these things efficiently. Keeping the contract small
has three effects. More vendors can be supported. Each driver is short and easy
to review. And every behavior in the contract can be tested thoroughly.

Convenience lives in the facade instead. The cache facade adds typed values,
`Remember` (read a value, or compute and store it when missing), typed `Pull`
(decode a value after the driver's atomic read-and-delete), and telemetry. A feature
added to the facade is immediately available on every driver.

There are no optional extras. Every operation is a method of the driver
interface, and a vendor that cannot do one returns the contract's
`ErrUnsupported` from it. See
[Facades and drivers](facades-and-drivers.md#unsupported-operations).

## Errors are part of the contract

Every service defines its own sentinel errors, such as `cache.ErrNotFound`,
`database.ErrNoRows`, and `storage.ErrNotFound`. A driver wraps the matching
sentinel when that condition occurs, so your code checks errors the same way
for every vendor:

```go
_, err := store.Get(ctx, key)
switch {
case errors.Is(err, cache.ErrNotFound):
	// not cached: compute it
case err != nil:
	return err
}
```

Import `errors` and `arkoren.dev/core/service/cache`. The fragment above assumes
an existing `*cache.Cache` named `store`, your own `Profile` type, a `cache.Key[Profile]` named `key`,
and an operation context `ctx`. The missing-entry branch is where your application
loads the profile from its authoritative store; other errors must not be treated
as a cache miss.

Only the conditions the contract names are translated into sentinels. Any
other failure, such as a lost connection, is returned as the driver reports
it, so log it as an unexpected error rather than trying to match it.

## How contracts treat the context

Contract methods that perform I/O take a `context.Context` as their first
argument, and drivers honor it. Hashing and encryption instead run locally
without a context. For an I/O operation, a canceled or expired context makes it
fail with an error that wraps the context's error, so
`errors.Is(err, context.DeadlineExceeded)` works on any driver. Set deadlines
on the context of each request or job to bound how long an operation may take.

## Conformance suites

A contract written in prose is only as good as the drivers that follow it. For
this reason, every contract has a **conformance suite**: a function named
`Run` in the service's `<contract>test` package that runs one subtest for each
documented behavior. Arkoren's backend drivers call it from their own tests,
including the memory drivers. In a `_test.go` file, import `testing`,
`arkoren.dev/core/service/cache`, `arkoren.dev/core/service/cache/cachetest`, and
`arkoren.dev/core/service/cache/memorycache`:

```go
func TestDriver(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Driver {
		return memorycache.New()
	})
}
```

The suite calls your factory for each subtest. The cache suite also uses unique
keys, so drivers can share an external backend without requiring it to be empty.
It covers missing keys, expiry, conditional writes, counters, copies,
cancellation, and concurrent use. A driver that
passes has evidence that it follows the contract behind the facade. The
interface documentation remains the contract: if a rule has no test yet,
it is not optional. Passing the suite is evidence, not proof that every backend
configuration or production failure mode behaves identically.

Time-dependent subtests run in a simulated clock, so expiry tests finish
instantly for drivers that measure time in the process. Drivers whose expiry is
measured by a server, such as Redis, pass an option that makes the suite wait
in real time instead.

When you [write a driver](../drivers/writing-a-driver.md), implement the
documented contract and run the suite to verify it.

## Fakes

Some services have effects that leave your process: sending an email,
dispatching a job, publishing a message, starting a workflow. Testing code that
causes these effects should not require a mail server or a broker. For these
services, the `<contract>test` package also provides a **fake**: a driver that
records what happened and offers typed assertions about it.

Here `job` is your existing `queue.Job[InvoicePayload]` declaration, and the
code under test receives `dispatcher`. Import `arkoren.dev/core/service/queue`
and `arkoren.dev/core/service/queue/queuetest`; `t` is your test's `*testing.T`:

```go
fake := queuetest.New()
dispatcher := queue.NewDispatcher(fake)

// ... run the code under test, which dispatches a job ...

fake.AssertDispatched(t, job, func(invoice InvoicePayload) bool {
	return invoice.ID == 42
})
```

A fake implements the driver interface, so the code under test still uses the
real facade. The queue fake can drain recorded jobs through a real consumer to
exercise handlers, retries, and failures without a broker. This verifies the
application's behavior, not a production broker's delivery guarantees.
[Fakes](../testing/fakes.md) shows the fake of each service in detail.

## The contracts at a glance

| Service | Driver interfaces | In-process driver | Fake |
| --- | --- | --- | --- |
| Broadcasting | `PublisherDriver`, `ConsumerDriver` | `memorybroadcast` | yes |
| Cache | `Driver` | `memorycache` | no |
| Database | `Driver` | SQLite in memory | no |
| Feature flags | `Driver` | `memoryfeature` | no |
| Encryption | `Driver` | none needed | no |
| Hashing | `Driver` | none needed | no |
| Language models | `Driver` | none (use `llmtest.Fake`) | yes |
| Mail | `Driver` | `slogmail` (writes to the log) | yes |
| Payments | `Driver` | `memorypayment` | yes |
| Queues | `DispatcherDriver`, `ConsumerDriver` | `memoryqueue` | yes |
| Scheduling | `Driver` | `memoryschedule` | yes |
| Search | `Driver` | `memorysearch` | no |
| Secrets | `Driver` | `memorysecret` | no |
| File storage | `Driver` | `memorystorage` | no |
| Telemetry | `TraceDriver`, `MetricDriver`, `LogDriver` | `memorytelemetry` | yes |
| Workflows | `Driver` | `memoryworkflow` | yes |

Encryption and hashing run entirely in your process, so their real drivers
already need no infrastructure. Services without a fake are tested against
their memory driver, which you can inspect directly.

SQLite is a separate database driver, not an implementation bundled with core.
The workflow and schedule fakes record requests without running the full engine
contract; use their memory drivers when testing workflow bodies or timer execution.

## Learn more

Each service's source documentation defines its method contracts. For example,
run `go doc arkoren.dev/core/service/cache.Driver` from your application's module
directory to read the selected version. The
[`cache` source reference](https://pkg.go.dev/arkoren.dev/core/service/cache)
also provides online lookup; choose the version matching your application.
