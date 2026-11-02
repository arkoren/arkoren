---
title: Driver Conformance
description: Check custom drivers and wrappers against shared public contracts with suite-specific fixtures and lifecycle ownership.
order: 70
summary:
  - Run shared contract tests against custom drivers and wrappers with suite-specific fixtures.
  - Manage factory lifetime, matching queue roles, and clock or capability declarations.
  - Interpret skips and reader-dependent checks without overstating provider coverage.
---

# Driver Conformance

When your application needs a [custom driver](../drivers/writing-a-driver.md), test the behavior callers rely on, not only the calls your adapter makes to a vendor SDK. A **conformance suite** runs shared contract checks against your driver. It is also useful for a wrapper that adds behavior such as key prefixes: forwarding most methods correctly does not establish that all operations still agree on the same keys.

A passing suite is evidence for the tested public contract, not a certification of every vendor feature, production setting, or failure mode. Keep application-specific and real-backend tests alongside it.

## Run a Suite

Each service's test package exposes its own `Run` function. This complete cache example uses `testing`, `arkoren.dev/core/service/cache`, and the `cachetest` and `memorycache` subpackages:

```go
func TestCacheContract(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Driver {
		return memorycache.New()
	})
}
```

Replace the returned driver with your implementation or wrapper. The cache suite calls its factory for each subtest, uses unique keys, and runs subtests in parallel. A fresh memory driver supplies isolated empty state. If your factories share a backend, ensure both the implementation and fixture allocation support concurrent tests.

Read the relevant suite's documentation before copying this factory shape. Signatures, readiness requirements, parallelism, and factory lifetimes differ between services.

## Own Test Resources

Register resource cleanup with `t.Cleanup` on the `*testing.T` passed to the factory. Initialize required connections or fixtures before returning. Use a bounded cleanup context independent of the canceled test context, as shown in [Integration Tests](integration-tests.md#own-connections).

Do not defer a driver's shutdown in the factory: that closes it when the factory returns, before the suite uses it. A defer in a parent test is also too early for parallel subtests; test cleanup runs after its subtests finish.

Workflow factories need particular care. Without `workflowtest.WithSynctest()`, the suite calls its factory once with the parent test and runs parallel cases against that shared driver. With the option, each case gets a fresh driver inside its own bubble and cases run sequentially. Register cleanup on the test supplied by the suite in either mode. [Workflow driver conformance](../workflows/testing.md#driver-conformance) owns the required registry and engine setup; a recording fake does not execute that engine.

## Connect Both Roles

Some contracts need a matching producer and consumer. The queue suite takes a `queuetest.Driver` containing both roles; they must use the same queues. Import `context`, `time`, `arkoren.dev/core/service/queue/queuetest`, and `arkoren.dev/core/service/queue/memoryqueue`:

```go
func TestQueueContract(t *testing.T) {
	queuetest.Run(t, func(t *testing.T) queuetest.Driver {
		driver := memoryqueue.New()
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := driver.Stop(ctx); err != nil {
				t.Error(err)
			}
		})
		return queuetest.Driver{Dispatcher: driver, Consumer: driver}
	}, queuetest.WithSynctest())
}
```

The factory runs once per queue subtest and must supply empty queues. The in-process clock option lets timer cases advance without real waits. For a backend that requires queue provisioning or name mapping, return dedicated empty names in the factory's `Queue` and `Other` fields and clean up only those owned resources.

Do not use the in-process clock option for a driver that talks to an external system. The [queue suite reference](https://pkg.go.dev/arkoren.dev/core/service/queue/queuetest) describes its role and fixture requirements.

## Declare Capabilities

Suite options describe actual supported capabilities or the test environment; they do not redefine an operation's meaning.

For expiry governed by a remote server, the cache suite uses real time when given `cachetest.WithExternalClock()`. With your configured factory named `newDriver`, the invocation is:

```go
cachetest.Run(t, newDriver, cachetest.WithExternalClock())
```

Without that option, timed cache cases construct the driver in a synctest bubble. Queue and workflow suites instead opt into bubbles explicitly with their own `WithSynctest()` options. Follow the particular suite, not a guessed common default; [Controlling Time](time.md) explains why a process clock cannot control a server clock.

Where a contract permits an unsupported operation, a suite may expose a corresponding option. Use it only when the implementation really has that documented limitation and returns the required unsupported result. Some options assert rejection without submission rather than merely skipping a check. A failing required operation cannot be made conformant by treating it as an optional feature.

## Interpret Coverage

Read subtest names and skips along with the final result. A failure identifies the rule to investigate; fix the driver or its fixtures against the public contract rather than weakening the assertion. A skipped content check is not evidence that the content was correct.

Telemetry illustrates this boundary. `telemetrytest.Run` checks lifecycle and the signal capabilities the driver implements. Content assertions need a `telemetrytest.Reader`, supplied by the driver or through `telemetrytest.WithReader`; without one those checks are skipped. An exporting driver's reader should inspect its test backend. Run the suite on individual drivers rather than a telemetry composite. See the [`telemetrytest` reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry/telemetrytest).

Telemetry event checks require all requested attributes on one event with the requested name. Extra attributes are allowed; attributes split across events cannot combine into a match. With no requested attributes, the event name alone matches.

Other suites need domain fixtures: the push factory returns both a ready driver and a driver-valid recipient, while feature-flag testing installs provider fixtures through a harness. Follow [Push Drivers](../push/drivers.md) and [Feature Flag Testing](../feature-flags/testing.md) for those requirements. Push conformance checks portable direct-driver submission, not device delivery or facade sender defaults.

Run infrastructure-backed suites as [integration tests](integration-tests.md), with an explicit short-mode guard, isolated resources, and teardown on failure. Deterministic HTTP or SDK fixtures can stay in the short run when they need neither credentials nor a live provider; they still do not establish live-provider behavior.

For authentication's request factories, use [Authentication Tests](auth-tests.md#check-authenticators). For the cache example's full checks and options, see the [`cachetest` API reference](https://pkg.go.dev/arkoren.dev/core/service/cache/cachetest).
