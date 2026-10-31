---
title: Driver Conformance Suites
nav: Driver Conformance
description: Run a service's conformance suite to check portable contract behavior in new drivers and wrappers around existing ones.
order: 70
---

# Driver Conformance Suites

## Introduction

When you [write a driver](../drivers/writing-a-driver.md)
for a new vendor, and when you write a driver that wraps another one, such as
a cache driver that adds a key prefix or encryption, check it with a
**conformance suite**. Each service contract provides this test function to
check portable behavior through focused subtests.

Shipped vendor and memory drivers use the same suites, and your driver must
satisfy the applicable checks too. The suite exercises the behavior that code
using the facade expects, rather than the vendor's entire feature set.

## Running a suite

Each suite is a function named `Run` in the service's `<service>test` package.
It takes the test and a function that builds a fresh driver. For example, run
the cache suite against the shipped memory driver. In a `_test.go` file, import
`testing`, `arkoren.dev/core/service/cache`,
`arkoren.dev/core/service/cache/cachetest`, and
`arkoren.dev/core/service/cache/memorycache`:

```go
func TestConformance(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Driver {
		return memorycache.New()
	})
}
```

The cache suite above calls the factory once per subtest, checking each behavior
against a fresh, empty driver. Use `t.Cleanup` on the test passed to the factory
to release anything the driver opened. To test your own wrapper, return it
around that driver.

Factory lifetime is suite-specific. Without `WithSynctest`, `workflowtest.Run`
calls its factory once with the parent test and runs parallel subtests against
that shared driver. The driver and shared fixtures must support concurrent use;
register cleanup on the supplied parent test so it runs after all subtests, not
with a deferred stop that could close the driver before parallel cases finish.
With `workflowtest.WithSynctest()`, cases run sequentially in separate bubbles,
and each gets a fresh driver constructed and cleaned up inside its bubble. See
[Testing Workflows](../workflows/testing.md#testing-a-driver).

A passing suite provides evidence that the driver follows the tested contract;
it does not verify every production configuration, failure mode, or provider
capability. When a subtest fails, its name says which rule was broken, such as how
an expired entry must behave, and the contract's documentation describes the
rule in full.

## Services with several roles

For services whose contract has several roles, the suite takes both sides. The
queue suite, for example, receives a dispatcher and a consumer, which are
often the same driver. Import `context`, `arkoren.dev/core/service/queue/queuetest`,
and `arkoren.dev/core/service/queue/memoryqueue` for this fragment:

```go
queuetest.Run(t, func(t *testing.T) queuetest.Driver {
	driver := memoryqueue.New()
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return queuetest.Driver{Dispatcher: driver, Consumer: driver}
}, queuetest.WithSynctest())
```

## Options

Suite options describe what a driver can do, never a different meaning of the
contract. They exist for differences that are real and documented, such as:

- **Unsupported operations.** A vendor that cannot do an operation returns
  the contract's `ErrUnsupported`; a suite option may declare
  this and skip that operation's subtests. Without one, the operation is
  required.
- **Clocks.** In-process drivers can run time-dependent subtests in a
  `testing/synctest` bubble, so they finish instantly. The queue suite opts in
  with `WithSynctest()` as above; see
  [Controlling time](time.md). A driver whose time is measured by a server, such
  as Redis, passes an option such as `cachetest.WithExternalClock()`, and the
  suite waits in real time instead.

If a driver cannot pass a subtest without an option that changes what the
contract promises, the driver is not conformant, and code that uses it
through the facade may break.

## Checking telemetry content

`telemetrytest.Run` always checks lifecycle rules and checks each implemented
signal's portable behavior. Content checks need a reader exposing retained spans
and metrics. A memory driver supplies it directly; an exporting driver can
provide `telemetrytest.WithReader` to read its test backend. Without a reader,
those content subtests are skipped, so a passing run alone does not prove
exported contents.

Event assertions require every requested attribute on one event with the
requested name. Extra attributes are allowed; when none are requested, the
name alone matches. Attributes on separate events cannot combine into a match.

Recording-only workflow and scheduler fakes do not execute their engines and
cannot substitute for full engine conformance tests. For feature flags, the
suite takes a harness that installs provider fixtures; propagation and update
capabilities remain explicit. See [Feature Flag Testing](../feature-flags/testing.md).

## Suites against real infrastructure

Infrastructure-backed drivers run their suites as
[integration tests](integration-tests.md), skipped with `-short`, against a
server started locally with Docker Compose. HTTP and SDK-backed drivers can
instead use deterministic provider fixtures; those tests run with `-short`
without credentials or a live provider.

The push suite accepts a factory returning both a driver and a valid recipient:
`func(*testing.T) (push.Driver, push.Recipient)`. This keeps browser subscription
fixtures specific to the driver under test. The suite checks the completed
driver settings; sender-default behavior is tested separately through the
facade. See [Push Drivers](../push/drivers.md).

For suite signatures and options, run `go doc` for the relevant `<service>test`
package from your application's module directory. For authenticator factories,
see [Testing Authentication](auth-tests.md).
