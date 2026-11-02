---
title: Consuming Jobs
description: Register typed handlers, choose worker queues, and run consumers with a controlled shutdown.
order: 40
summary:
  - Register handlers that receive fresh payloads and propagate cancellation.
  - Select queues and understand concurrency within and across workers.
  - Own worker shutdown and test handlers through the real consumer policy.
---

# Consuming Jobs

A producer can request a report without knowing how to build it. The worker
supplies that implementation by registering a handler on a `*queue.Consumer`.
The consumer finds the handler by job name, decodes the payload, executes it, and
asks the driver to settle the delivery according to the result.

Start with a [configured driver](drivers.md) and the shared `BuildReportJob` and
`BuildReport` payload from [Producing Jobs](producing-jobs.md#defining-jobs).
The consumer owns execution policy, not the driver's connection lifecycle.

## Registering Handlers

Register handlers before starting the worker. With `context` imported, `queue`
from `arkoren.dev/core/service/queue`, and your application's
`reports.Build(ctx, reportID) error` operation, connect the job to its implementation:

```go
consumer := queue.NewConsumer(driver)
err := consumer.Handle(BuildReportJob, func(ctx context.Context, payload BuildReport) error {
	return reports.Build(ctx, payload.ReportID)
})
```

Each attempt gets a fresh decoded payload. The context carries consumer
cancellation, the job's execution deadline when configured, and propagated trace
context when telemetry is enabled. Pass it to database, storage, and HTTP calls.
A timeout cancels the context; it does not stop application code by force.

Handle `err` before running the worker. Invalid definitions return
`queue.ErrInvalidName` wrapping the definition error. A second registration for
the same job name returns `queue.ErrDuplicateName`, even if its payload type
differs. A nil handler is rejected without registration.

`Run` discovers queues once when it starts. A later handler on an already
consumed queue can handle subsequent deliveries, but adding a new queue does not
start another consume call until a later run. Register everything first to avoid
timing-dependent coverage.

## Selecting Queues

By default, a consumer reads every queue that has a registered handler. If report
generation needs dedicated workers, restrict an alternative consumer construction:

```go
consumer := queue.NewConsumer(driver, func(worker *queue.ConsumerBuilder) {
	worker.Queues("reports")
})
```

Register handlers on that consumer as above. Other handlers may remain registered,
but their queues are skipped. This restriction neither changes driver routing nor
provisions broker resources. A selected queue without a handler is not consumed;
if none remain, `Run` returns `queue.ErrNoQueues`.

Each `Queues` call replaces the previous restriction; `Queues()` restores all
handled queues. Inputs and the published restriction are copied. Construction
callbacks run once in order, and a retained draft cannot reconfigure the published
consumer. Telemetry and event buses remain shared services.

## Worker Lifetime

`Handle` does not begin consumption. Register `Run` as a supervised runner in an
existing application composition callback, with `application` imported from
`arkoren.dev/core/application`:

```go
compose := func(composer *application.Composer) {
	composer.Run(consumer.Run)
}
```

Pass the callback to your existing `app.Run(ctx, compose)`. Include the driver's
startup, readiness, and cleanup hooks from [Drivers](drivers.md) when required.
RabbitMQ and NATS need those hooks; memory needs cleanup but no readiness probe;
SQS and Cloudflare have no driver hooks. Construction alone does not connect
RabbitMQ or NATS.

`Run` starts one driver `Consume` call per selected, handled queue. Shipped
drivers execute handlers sequentially within each call, including fetched
batches. Different queues run concurrently. For more concurrency on one queue,
run additional workers sharing the same physical work queue or JetStream durable;
they compete for deliveries rather than receiving a broadcast copy each.
Prefetch and batch size reserve work ahead, not parallel handler slots.

If any consume call returns an error, `Run` cancels its sibling queue consumers,
waits for them, and returns the first error. As an application runner, that error
stops the service; let your deployment supervisor restart a worker after broker
failure rather than silently leaving it idle. Ordinary handler errors whose
retry or failure settlement succeeds do not themselves end `Run`.

### Stopping Workers

Cancel the consumer and wait for it to return before stopping its driver or
closing a borrowed connection. The active handler must cooperate with cancellation;
the consumer cannot kill it. A handler that completes successfully still needs
its acknowledgement, so allow shutdown time for execution and settlement.

Settlement has a fresh 30-second budget after execution and is detached from
consumer cancellation. It can still fail if transport cleanup happens first or
the backend is unavailable. If the application's shutdown budget expires,
cleanup can release a driver while a handler is still running. See
[shutdown settlement](delivery-and-retries.md#shutdown-settlement) for the retry
exception and its attempt-count implications.

Memory consumer cancellation preserves work not yet admitted; it does not drain
the backlog or revoke the active delivery. An active handler can acknowledge or
retry with the detached settlement context. `memoryqueue.Stop` is different: it
discards ready jobs and delayed retries, prevents new admissions, and waits for
active work only until its context ends. Retrying into that stopped driver fails.
Use a broker-backed driver if pending work must outlive the process.

## Handler Results

Return `nil` only when the work is complete; swallowing an error requests
acknowledgement of unfinished work. Return an ordinary error for a transient
failure. The consumer requests retry using the registered job's attempt limit
and backoff, or terminal failure if attempts are exhausted.

Use `queue.Permanent(err)` when another attempt cannot help. `queue.RetryAfter`
requests a specific delay while still respecting the attempt cap.
`queue.Reject` delegates unwanted-message handling to backend rejection policy;
it is not a portable delete or dead-letter operation. Choose among these using
[Delivery and Retries](delivery-and-retries.md#handler-outcomes).

Make `reports.Build` repeat-safe with the stable report ID. A successful report
write followed by a lost acknowledgement can run again, and a lost lease can
overlap another attempt. For database-only effects, record completion and the
effect in one transaction. For remote effects, use provider idempotency and
reconcile uncertain results; a local completion flag cannot make a remote call
atomic. Required ordering also belongs in application state.

Job timeout and broker reservation are separate. NATS and SQS try to renew
reservations, but renewal failure can permit duplicates. Cloudflare cannot renew
its lease: all earlier handlers in a fetched batch consume the same lease time,
so keep batches small and finish execution plus settlement before expiry.

## Testing Handlers

Test the report-building operation directly for its application behavior. To
exercise JSON decoding and consumer settlement too, use
`arkoren.dev/core/service/queue/queuetest`. In a test with `t *testing.T`, construct
both facades around the same fake:

```go
fake := queuetest.New()
dispatcher := queue.NewDispatcher(fake)
consumer := queue.NewConsumer(fake)
```

Register your handler using `Handle` and check its registration error. Dispatch
a report payload, then drain due work through that consumer:

```go
if err := dispatcher.Dispatch(t.Context(), BuildReportJob, BuildReport{ReportID: "rpt-204"}); err != nil {
	t.Fatal(err)
}
if err := fake.Drain(t.Context(), consumer); err != nil {
	t.Fatal(err)
}
```

Assert the report's observable stored result using your application's test
fixtures. Successful draining proves pending due work was processed without a
settlement error, not that every handler succeeded: a terminally failed job also
drains. Inspect the fake's `Failed()` or `Rejected()` records when testing those
outcomes.

Delayed retries remain pending by default. `queuetest.WithScheduled()` includes
them by skipping waits, not advancing time; use it when testing attempt policy,
not elapsed-time behavior. A per-message delivery limit catches endless retry
loops. See [running dispatched jobs](../testing/fakes.md#separate-execution)
for assertions and fake options.

See the [`queue` API](https://pkg.go.dev/arkoren.dev/core/service/queue) and
[`queuetest` API](https://pkg.go.dev/arkoren.dev/core/service/queue/queuetest).
