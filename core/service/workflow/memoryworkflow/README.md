# Memory Workflow

`arkoren.dev/core/service/workflow/memoryworkflow` is an in-process implementation of the Arkoren
`workflow.Driver` contract for local development and single-replica
deployments. It runs every workflow in a goroutine of the process and uses the
system clock. Runs are lost on restart and are not shared between replicas.

It is included in `go get arkoren.dev/core`. Import
`"arkoren.dev/core/service/workflow"` and
`"arkoren.dev/core/service/workflow/memoryworkflow"`. The
[memory workflow guide](../../../../cmd/arkoren/skills/arkoren/references/guides/workflows/drivers.md#memory)
covers cancellation, signal limits, and cleanup ownership.

Declare workflows with `workflow.New(name, fn, configure...)` and steps with
`workflow.NewStep(name, fn, configure...)`. Configure
`workflow.Builder.MaxDuration` before registration; omission fails in
`workflow.Handlers`. Definition callbacks publish detached settings, and
`Step.Derive` supplies a local policy override without changing the base step.

Given the [onboarding definitions](../README.md), construct the process-local
driver inside an application composer and register cleanup for its runs:

```go
registry := compose.Must(workflow.Handlers(Onboard, SendWelcome))
driver := memoryworkflow.New(registry)
compose.Cleanup(driver.Stop)

client := workflow.NewClient(driver)
```

Here `compose` is the `*application.Composer` passed to your bootstrap function.
Pass `client` explicitly to the application services that need it.

`Context.Child` checks for an existing run and inserts a new run under one lock.
Only a newly created child's context derives from the parent; it stops when the
parent is canceled or times out. Joining an existing run ignores replacement
input and acquires no cancellation ownership. Ending that joining parent's wait
leaves the original run active and able to finish through a signal.

To test code that starts workflows, use `workflowtest.Fake`. To test the
workflows themselves with sleeps and timeouts, run the driver inside
`testing/synctest`, which makes time pass instantly.
