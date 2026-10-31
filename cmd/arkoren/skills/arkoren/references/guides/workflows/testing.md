---
title: Testing Workflows
nav: Testing
description: Test code that starts workflows with a fake driver, and workflow logic on the memory driver with instant time.
order: 30
---

# Testing Workflows

## Introduction

Test the handoff separately from the work. A signup handler needs to start the
right onboarding run with the right input; its test does not need to send mail
or wait for confirmation. Use the `workflowtest` fake to record that handoff
without executing the workflow.

To test the workflow's decisions, run its real logic on the memory driver.
Combine it with `testing/synctest` to exercise sleeps, retries, and missing-signal
timeouts without real waits. Neither tool needs Temporal, Hatchet, or Restate,
but neither proves durable replay compatibility. Use a real engine for that
integration check and for engine-driver conformance.

Match that integration test to the deployed engine version and feature profile.
Memory and fake tests cannot establish native retry precision/caps, cancellation
retention, startup cleanup, or asynchronous SDK failure behavior. The
[driver comparison](drivers.md#compatibility-matrix) identifies the remaining
Hatchet SDK limits and the operator-managed Restate retention profile. A native API
health check does not verify that profile or close native lifecycle gaps.

## Testing the code that starts a run

With the workflow definitions from the [overview](overview.md), create a fake
driver for your application code. `workflowtest.New` returns a `workflow.Driver`.
Hand it to `workflow.NewClient`, run the code under test, and assert on what was recorded.
The fake never executes workflow bodies; a run stays running until the test
ends it:

Here, `signup` is the application function under test: it accepts the workflow
client and starts the `Onboard` workflow.

```go
import (
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/workflowtest"
)

func TestSignupStartsOnboarding(t *testing.T) {
	fake := workflowtest.New()
	client := workflow.NewClient(fake)

	err := signup(t.Context(), client, User{ID: 42})
	if err != nil {
		t.Fatal(err)
	}

	fake.AssertStarted(t, Onboard, func(in User) bool {
		return in.ID == 42
	})
}
```

The assertions are typed by the workflow, so the predicate receives a `User`
and a wrong type is a compile error. There are assertions for a start, no start,
a number of starts, a signal that was sent, and a cancel. For code that waits
for the result, script the outcome with `Complete` or `Fail`:

```go
fake.Complete(Onboard.Name(), "user-42", "welcome")

run := client.Run(Onboard, "user-42")
out, err := run.Result(t.Context()) // "welcome", nil
```

`Fail` ends a run with an error. The error crosses the same wire encoding as in
production, so only its kind and message survive; use `workflow.NewError` for
the errors your code recognizes:

```go
fake.Fail(Onboard.Name(), "user-42", workflow.NewError("card-declined", "declined"))
```

Completing or failing a run that has not started yet scripts it, so the later
`Start` returns a run that is already finished. A second `Start` with the same
identifier returns `workflow.ErrAlreadyStarted`, as a real driver does. To test
how your code reacts when the driver itself fails, `FailNext` makes the next call
return an error and `FailWith` makes every call return one.

## Testing a workflow's logic

Use the same `workflow.New` and `workflow.NewStep` builder callbacks as in
production. Set `Builder.MaxDuration` before building the registry.
For a single-call policy variation, use `Step.Derive`; it keeps the typed body
and inherited settings without changing the registered default.

To test the workflow function, build a registry from the definitions and run it
on the memory driver. Inside a `synctest` bubble, a three-day wait or a
backoff of an hour needs no real wait. Use the overview's `Onboard` definition
with a `SendWelcome` step backed by a fake mail sender, so this test exercises
the timeout branch without sending mail:

```go
import (
	"context"
	"testing"
	"testing/synctest"

	"arkoren.dev/core/service/workflow/memoryworkflow"
)

func TestOnboardExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry, err := workflow.Handlers(Onboard, SendWelcome)
		if err != nil {
			t.Fatal(err)
		}

		driver := memoryworkflow.New(registry)
		t.Cleanup(func() {
			_ = driver.Stop(context.Background())
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
}
```

Nobody sends the confirmation signal, so the `Wait` times out after 72 hours of
fake time and the workflow returns `"expired"`. To test the other branch, call
`client.Signal` before asking for the result. Steps run their real functions, so
replace the services they call (the mail sender, the payment gateway) with fakes
in the test, as you would for any code.

Two details keep these tests honest. Cancellation does not forcibly stop your
code. Give `driver.Stop` a context with a deadline when a workflow function
could ignore cancellation. Arrange for workflow bodies and fake step services
to honor cancellation and join any application-owned background work; a cleanup
deadline bounds your wait, not that code's lifetime.

The memory driver also never replays a workflow, so it cannot show you a
determinism bug. Keep the function free of clock, random, and I/O calls by
review, and run the same workflow against an engine in an integration test
when the risk is high. [Controlling Time](../testing/time.md) covers `synctest`
in more detail.

## Testing a driver

Use the conformance suite, `workflowtest.Run`, to check common contract behavior;
it does not replace deployed-engine regression and replay tests. Build the
registry with `workflowtest.Registry` inside
the function that creates the driver, and the suite runs its cases against it:

```go
func TestConformance(t *testing.T) {
	workflowtest.Run(t, func(t *testing.T) workflow.Driver {
		driver := memoryworkflow.New(workflowtest.Registry(t))
		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})

		return driver
	}, workflowtest.WithSynctest())
}
```

`WithSynctest` runs cases sequentially in separate bubbles for drivers that use
the `time` package. Each case constructs its own driver inside its bubble, and
cleanup registered on the supplied `t` runs before that bubble ends.

Without `WithSynctest`, the factory runs once with the parent test and parallel
subtests share the resulting driver. The driver, registry dependencies, and
other shared fixtures must support concurrent use. Register cleanup on the
factory's supplied `t`, as above, so it runs after the parallel subtests finish;
do not defer a stop in the parent test. Drivers backed by an engine use this
mode against a real server, usually from an integration test.

Child ownership tests distinguish creation from joining: canceling or timing out
a join must leave the existing run able to finish with its original input and
without duplicate work, while a newly created child must stop with its parent.
An unsupported driver must reject before submission and leave independent runs
untouched. For a native join regression, observe the engine's join acknowledgment
before ending the parent; seeing the parent start is not proof that it joined.
See [Integration Tests](../testing/integration-tests.md) and [Driver Conformance
Suites](../testing/conformance-suites.md).

For every function and option, see [the `workflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow).
