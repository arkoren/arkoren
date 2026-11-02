---
title: Testing Workflows
nav: Testing
description: Separate submission tests, in-process workflow decisions, and native replay or retention checks.
order: 80
summary:
  - Verify submission with a fake and script results without executing workflow bodies.
  - Exercise workflow decisions with memory and virtual time while retaining cleanup ownership.
  - Separate portable conformance from native replay, retention, and lifecycle regressions.
---

# Testing Workflows

Test the request to start work separately from the work itself. A signup handler
test should prove that it submits onboarding for the right user, without sending
mail. A workflow test should exercise the process's decisions. Replay and native
retention need an engine test: neither a fake nor memory executes durable history.

The following are fragments inside an existing `Test...` function with
`t *testing.T`. They use `arkoren.dev/core/service/workflow` and the
[onboarding definitions](overview.md#defining-work). Use the
[confirmation-aware body](signals-and-waiting.md#awaiting-confirmation) for timeout
and signal tests.

## Submission Tests

Create a fake driver and pass its ordinary client to the application code under
test:

```go
import "arkoren.dev/core/service/workflow/workflowtest"

fake := workflowtest.New()
client := workflow.NewClient(fake)
```

Here `signup` is your application function: it accepts a client, persists or
validates signup according to your application, and starts Onboard. Exercise it,
then assert the typed payload:

```go
if err := signup(t.Context(), client, User{ID: 42}); err != nil {
	t.Fatal(err)
}

fake.AssertStarted(t, Onboard, func(user User) bool {
	return user.ID == 42
})
```

The assertion proves submission of that input, not that onboarding executed or
the signup/database handoff was atomic. The fake never calls workflow or step
bodies. Every workflow name is considered registered, so validate your real
registry in separate tests. Runs stay Running until you call Complete, Fail,
or Cancel.

Use the fake's assertions for no starts, start counts, signals, and cancellation
when those are the behavior under test. Failed driver calls are not recorded.

## Scripted Results

If application code waits for a result, script the outcome before calling it:

```go
fake.Complete(Onboard.Name(), "user-42", "confirmed")
```

Completing a run before Start stores a script; the later start returns a finished
run. Completing an already-started run releases its waiters. A repeated Start
still returns `workflow.ErrAlreadyStarted` with the existing handle.

To exercise an application error branch, script a stable error kind:

```go
var ErrInvitationRevoked = workflow.NewError("onboarding.revoked", "invitation revoked")

fake.Fail(Onboard.Name(), "user-42", ErrInvitationRevoked)
```

Use a fresh fake or a separate run ID for this alternative. Fail uses the same
wire representation as real workflow failures, so arbitrary worker-side Go types
do not survive. Application code should match the kind with `errors.Is`.

`fake.FailNext(err)` fails the next driver operation; `fake.FailWith(err)` fails
every subsequent operation until cleared with nil. These model command/lookup
failures, not a failure returned by the workflow body. Distinguish the two in
your assertions.

## Workflow Decisions

Use memory to execute the real workflow function, replacing its external
dependencies with fakes. For onboarding, define SendWelcome against a fake mail
sender so this test does not send real mail. Its handler should return promptly
and honor cancellation.

Inside your test, `synctest` advances the process-local clock when all work is
blocked. The three-day confirmation deadline needs no wall-clock wait:

```go
import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/workflow/memoryworkflow"
)

synctest.Test(t, func(t *testing.T) {
	registry, err := workflow.Handlers(Onboard, SendWelcome)
	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := driver.Stop(cleanup); err != nil {
			t.Error(err)
		}
	})
	client := workflow.NewClient(driver)

	run, err := client.Start(t.Context(), Onboard, "user-42", User{ID: 42})
	if err != nil {
		t.Fatal(err)
	}

	out, err := run.Result(t.Context())
	if err != nil || out != "expired" {
		t.Fatalf("Result = %q, %v; want expired", out, err)
	}
})
```

No confirmation is sent, so the wait expires and the workflow returns
`"expired"`. To test confirmation instead, send EmailConfirmed after Start and
before Result, then assert that token. Add retry tests only for decisions that
belong to your workflow or step policy, with explicit counts on the fake service.
See [Controlling Time](../testing/time.md).

Cleanup deadlines bound the Stop caller's wait, not the lifetime of code that
ignores cancellation. Workflow bodies and fake services must cooperate, and
application-owned background work must be joined. A synctest bubble also cannot
finish while its goroutines remain blocked indefinitely.

## Engine Regressions

Memory never replays the workflow function, so a passing memory test cannot
establish determinism. Review coordination code for clock, random, I/O, goroutine,
and native-channel use, then test representative retained histories against the
actual engine when deploying changes.

Match integration tests to the deployed server version and feature profile.
Native retry precision/caps, delayed or queued cancellation retention, endpoint
registration, startup rollback, and shutdown cleanup are distinct boundaries.
A healthy native API proves none of them. The [driver comparison](drivers.md)
identifies Temporal policy rejections and Restate's operator-managed retention
profile.

For joined children, wait until the engine acknowledges the join before ending
the parent. Seeing the parent start does not prove it joined. Verify that an
existing run finishes with its original input and without duplicate work, while
a newly created child stops with its owner. Unsupported Child must reject before
submission and leave independent runs untouched.

## Driver Conformance

If your application supplies a custom driver, use `workflowtest.Run` to exercise
the portable contract. Build the suite's registry inside the factory, not from
your onboarding definitions:

```go
workflowtest.Run(t, func(t *testing.T) workflow.Driver {
	driver := memoryworkflow.New(workflowtest.Registry(t))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := driver.Stop(cleanup); err != nil {
			t.Error(err)
		}
	})
	return driver
}, workflowtest.WithSynctest())
```

WithSynctest creates a driver inside each separate bubble and runs cases
sequentially; cleanup registered on the supplied test runs before the bubble
ends. Never use it for an external engine.

Without that option, the factory runs once with the parent test, and parallel
subtests share the driver and its dependencies. All shared fixtures must support
concurrent use. Register cleanup on the factory's `t` so it runs after those
subtests; a deferred stop in the parent function runs too early.

Declare unsupported operations with `WithoutChild`, `WithoutSpawn`, or
`WithoutImmediateStepRetries` when applicable. These options assert rejection
before submission or affected-handler execution rather than silently skipping
the contract. Keep every suite definition registered. Conformance complements,
not replaces, deployed-engine replay and regression tests. See
[Driver Conformance Suites](../testing/conformance-suites.md) and
[Integration Tests](../testing/integration-tests.md).

For fake assertions and suite options, see the
[`workflowtest` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow/workflowtest).
