---
title: Overview
description: Hand background work to a worker with typed jobs, dispatchers, and consumers.
order: 10
summary:
  - Separate job definitions from messages and delivery attempts.
  - Dispatch typed work and register the handler that completes it.
  - Recognize acceptance, duplicate risks, and the limits of dispatch tests.
---

# Overview

A report request can finish before the report is generated. A queue holds the
work so a worker can pick it up later, independently of the requesting process.
Use it when work needs a handler and an explicit completion or failure outcome;
use [events](../events/overview.md) for in-process notifications or
[broadcasting](../broadcasting/overview.md) for notifications between processes.

The queue facade separates three responsibilities. A **job definition** names a
kind of work and its payload type. A **dispatcher** submits instances of that
work. A **consumer** receives messages, calls registered handlers, and asks the
driver to acknowledge, retry, reject, or fail each delivery.

Choose and configure a [driver](drivers.md) before using the examples below.
Memory keeps work in one process; broker-backed drivers let producers and workers
run separately. Focused fragments leave error results visible without routine
propagation; your application must handle them.

## Job Definitions

Keep the payload and definition in a package that both producers and workers can
import. For a report, carry a stable report ID rather than all its source data:

```go
import "arkoren.dev/core/service/queue"

type BuildReport struct {
	ReportID string `json:"report_id"`
}
```

Give that work a stable name and a finite attempt limit:

```go
var BuildReportJob = queue.NewJob[BuildReport]("build-report", func(job *queue.JobBuilder) {
	job.OnQueue("reports")
	job.Retry(3)
})
```

This declares work on the logical `reports` queue; it sends nothing. `Retry(3)`
means three total attempts, including the first. The worker uses its own copy of
this policy, not policy supplied in the message. [Producing Jobs](producing-jobs.md)
covers payload evolution and routing.

## Sending Work

With an existing configured `driver`, build the producer-facing facade:

```go
dispatcher := queue.NewDispatcher(driver)
```

After the report request is durably stored, submit its ID using the request's
operation context `ctx`:

```go
err := dispatcher.Dispatch(ctx, BuildReportJob, BuildReport{ReportID: "rpt-204"})
```

The compiler checks the payload type. The dispatcher JSON-encodes it and creates
a message with a fresh random ID, job name, and queue name. Success means driver
acceptance, **not report completion**. A transport error can leave acceptance
uncertain, so repeating the call can create another message.

A database commit and dispatch are not atomic. If a lost handoff is unacceptable,
record dispatch intent in the same transaction as the report request and use a
[durable outbox handoff](producing-jobs.md#durable-handoff).

## Handling Work

In the worker, wrap its configured driver in a consumer. With `context` imported
and your application's `reports.Build(ctx, reportID) error` operation available,
register the handler before running the consumer:

```go
consumer := queue.NewConsumer(driver)
err := consumer.Handle(BuildReportJob, func(ctx context.Context, payload BuildReport) error {
	return reports.Build(ctx, payload.ReportID)
})
```

Each delivery attempt decodes a fresh payload. A `nil` result requests
acknowledgement; an ordinary error requests retry while attempts remain, then
terminal failure handling. Pass `ctx` to blocking work: timeouts and shutdown
cancel the context but cannot kill your handler.

[Consuming Jobs](consuming-jobs.md) shows how to run and stop the worker. A web
process needs only the dispatcher; a worker needs the consumer and its handlers.
Both must agree on job names, payloads, logical queues, and driver routing.

## Delivery Boundaries

An attempt is one delivery to a worker, not a promise of exclusive or exactly-once
execution. If report generation succeeds but acknowledgement fails, the message
can return. Lost leases can also let two handlers overlap. Make repeats harmless
using the report ID and enforce uniqueness where the effect is stored.

No portable unconditional at-least-once guarantee covers every driver and retry
path. Memory loses pending work on process exit or driver stop; broker retention,
delivery caps, and retry forwarding can also remove work. Competing workers and
retries can reorder jobs. Enforce required ordering in application state rather
than relying on dispatch order.

[Delivery and Retries](delivery-and-retries.md) explains acknowledgement gaps,
retry policy, poison messages, and remote-effect recovery. The
[compatibility matrix](drivers.md#compatibility-matrix) compares backend limits.

## Testing Jobs

To test a producer without executing its handler, use the bundled
`arkoren.dev/core/service/queue/queuetest` driver. In a test with `t *testing.T`,
give your application's `RequestReport(ctx, dispatcher, reportID) error`
operation a fake-backed dispatcher:

```go
fake := queuetest.New()
dispatcher := queue.NewDispatcher(fake)
if err := RequestReport(t.Context(), dispatcher, "rpt-204"); err != nil {
	t.Fatal(err)
}
```

Assert the requested work, not the transport envelope's generated ID:

```go
fake.AssertDispatched(t, BuildReportJob, func(payload BuildReport) bool {
	return payload.ReportID == "rpt-204"
})
```

This proves that the application submitted the expected payload, not that a
broker retained it or a handler completed it. To exercise consumer policy, use
the fake's [Drain support](../testing/fakes.md#separate-execution).
It runs due jobs synchronously; delayed retries stay pending unless you pass
`queuetest.WithScheduled()`, and a delivery limit detects endless retry loops.
The fake records failed and rejected messages too. Use `memoryqueue` when a test
needs a live consumer with delayed retries; it works with `testing/synctest`.

See the [`queue` API](https://pkg.go.dev/arkoren.dev/core/service/queue) and
[`queuetest` API](https://pkg.go.dev/arkoren.dev/core/service/queue/queuetest).
