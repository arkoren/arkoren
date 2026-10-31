# Queue

Use `arkoren.dev/core/service/queue` to hand typed work to a consumer without
binding application handlers to a broker. Install with `go get arkoren.dev/core`.
The [queue guides](../../../cmd/arkoren/skills/arkoren/references/guides/queues/overview.md)
cover driver ownership, producers, and workers.

Declare a shared job in application code. Producers and workers must agree on
its name and payload type; worker policy belongs to the registered definition:

```go
job := queue.NewJob[string]("send", func(job *queue.JobBuilder) {
	job.Retry(3)
	job.Timeout(time.Minute)
})
```

With an existing `dispatcher`, send a value:

```go
err := dispatcher.Dispatch(ctx, job, "hello")
```

In your worker's composition callback, use an existing `consumer` and your
`sendText(context.Context, string) error` handler:

```go
compose.Require(consumer.Handle(job, sendText))
compose.Run(consumer.Run)
```

Own the selected driver's startup and cleanup separately. Success from Dispatch
means driver acceptance, not execution. Handlers return nil for success; wrap
only non-nil failures with `queue.Permanent`. `Permanent(nil)` is still a terminal
failure, not success.

Consumer cancellation requests retry without applying the immediate attempt
cap. It does not discount the driver's delivery count: a later ordinary failure
can exhaust the cap. Settlement receives a fresh 30-second context after
execution, independent of consumer cancellation. Drivers and synchronous event
listeners must cooperate with that context; Go code is not forcibly terminated.
`JobFailed` is emitted only after terminal settlement succeeds.

The memory driver owns payload and metadata copies, checks cancellation before
reservation, and discards queued work on Stop. Its Stop waits only until its
context ends; an admitted handler can still be running. See the
[driver comparison](../../../cmd/arkoren/skills/arkoren/references/guides/queues/drivers.md)
for durability and backend failure handling, and the
[`queue` API](https://pkg.go.dev/arkoren.dev/core/service/queue) for declarations.
