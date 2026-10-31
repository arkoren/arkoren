# Workflow

`arkoren.dev/core/service/workflow` runs durable, typed workflows. A workflow orchestrates
steps, waits for signals, sleeps, and starts other workflows. The facade owns
what runs; a driver owns how it survives restarts and where it runs.

Install with `go get arkoren.dev/core`. The examples import
`"arkoren.dev/core/service/workflow"` and
`"arkoren.dev/core/service/workflow/memoryworkflow"`, plus the standard
`context`, `errors`, and `time` packages. Follow the
[workflow guide](../../../cmd/arkoren/skills/arkoren/references/guides/workflows/overview.md)
for signal delivery, driver selection, and lifecycle setup.

When onboarding must wait for email confirmation, put the mail I/O in a step
and the wait in the workflow. Here `User` and `sendWelcome` are your application
type and step handler:

```go
var SendWelcome = workflow.NewStep("send-welcome", sendWelcome, func(step *workflow.StepBuilder) {
	step.Retry(5)
	step.Backoff(time.Second, 2, time.Minute)
	step.Timeout(30 * time.Second)
})

var EmailConfirmed = workflow.NewSignal[string]("email-confirmed")
```

Write the workflow's control flow separately:

```go
func onboard(execution *workflow.Context, user User) (string, error) {
	if _, err := execution.Step(SendWelcome, user); err != nil {
		return "", err
	}

	token, err := execution.Wait(EmailConfirmed, workflow.Timeout(72*time.Hour))
	if errors.Is(err, workflow.ErrTimeout) {
		return "expired", nil
	}

	return token, err
}
```

Publish the function with its maximum duration:

```go
var Onboard = workflow.New("onboard", onboard, func(definition *workflow.Builder) {
	definition.MaxDuration(96 * time.Hour)
})
```

Register the definitions with a driver. The
[memory driver](memoryworkflow/README.md) is process-local, not durable:

```go
registry, err := workflow.Handlers(Onboard, SendWelcome)
driver := memoryworkflow.New(registry) // or a durable driver built from the same registry
```

After starting your selected driver as its lifecycle requires, start and talk
to runs with a client. Separate server and worker processes need a shared durable
backend; they cannot share a memory driver:

```go
client := workflow.NewClient(driver)

run, err := client.Start(ctx, Onboard, "user-42", user)
if errors.Is(err, workflow.ErrAlreadyStarted) {
	// run is the existing run; the error is safe to ignore
}

err = client.Signal(ctx, Onboard, "user-42", EmailConfirmed, "abc123")
result, err := run.Result(ctx) // ctx bounds the wait, not the run
status, err := run.Status(ctx)
err = run.Cancel(ctx)
```

- Names are 1-64 lowercase letters, digits, or hyphens with no hyphen at either
  end. `New` and `NewStep` keep the first invalid name, nil function, or bad
  duration on the value (`Err()`); `Handlers` returns it as
  `ErrInvalidName`, `ErrNilFunc`, or `ErrInvalidOptions`. Workflows and steps
  share one namespace. Every workflow needs `Builder.MaxDuration`;
  omission fails only at registration, while an explicit nonpositive duration
  is invalid construction. `NewSignal`, which
  has no registration step, still panics with `ErrInvalidName`.
- Definitions are immutable snapshots. Construction callbacks share one fresh
  draft, run directly once in order, and return no value. Settings return that
  draft. Nil entries and callback panics propagate; expanded nil or empty slices
  omit edits. Retaining a draft cannot change a published definition.
- Change a step for one call with `Step.Derive`, which preserves its name,
  function, inherited policy, and first error. Registration snapshots the
  function and settings independently of the published pointer.
- Workflow functions must be deterministic: use `execution.Now()`, `execution.UUID()`, and
  steps instead of the clock, random numbers, and I/O.
- `execution.Wait` and `execution.Select` remember the first value of a signal per run.
- `execution.Child` acquires cancellation ownership only when it creates the
  child: parent cancellation or timeout also stops that new run. Joining an
  existing ID ignores the new input and acquires no ownership, so parent
  cancellation or timeout ends only the wait. Drivers unable to decide ownership
  atomically and retain it across replay reject with `ErrUnsupported` before
  submission. `Spawn` intentionally creates detached work instead.
- Return `workflow.Permanent(err)` from a step to stop retries; check with
  `errors.Is(err, workflow.ErrPermanent)`.
- `ErrCanceled`, `ErrTimeout`, and `*workflow.Error` keep their kind across
  the wire, so `errors.Is` works where the run was started.
- Payloads are JSON objects `{"v": ...}` made by a `Codec`; the default is
  `encoding/json/v2`.

The package uses only the standard library. Drivers implement
`workflow.Driver` and `workflow.Runtime`, and run the definitions from
`workflow.Handlers`. To test a custom driver with `workflowtest.Run`, create
its registry with `workflowtest.Registry(t)` inside the supplied driver factory.

Inside a workflow, use its `execution` context to disable retries for one call
without changing the registered default:

```go
attempt := SendWelcome.Derive(func(step *workflow.StepBuilder) {
	step.Retry(1)
})
_, err := execution.Step(attempt, user)
```
