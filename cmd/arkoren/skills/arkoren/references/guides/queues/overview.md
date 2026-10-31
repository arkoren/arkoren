---
title: Queues Overview
nav: Overview
description: Define typed background jobs, dispatch them in one process and handle them in another, and understand delivery and retry limits.
order: 10
---

# Queues Overview

## Introduction

Some work does not belong in a request. Sending a welcome email, resizing an
upload, or calling a slow partner API can take seconds, can fail for reasons
that go away on their own, and should not make a person wait or lose the
request when it fails. A queue lets the request record what needs to happen and
return. A **job** is that record, and a separate **consumer**, usually running
in a worker process, picks it up later and does the work, trying again when it
fails.

Arkoren's queue has two parts. The **facade** is what your code uses: a typed
`queue.Job` that names a kind of work, a `*queue.Dispatcher` that puts jobs on a
queue, and a `*queue.Consumer` that runs your handlers. A **driver** moves the
messages: [memory](drivers.md#memory) for tests and a single process,
[RabbitMQ](drivers.md#rabbitmq), [NATS JetStream](drivers.md#nats), or
[Amazon SQS](drivers.md#amazon-sqs) when jobs must survive a restart and reach
other machines, and [Cloudflare Queues](drivers.md#cloudflare) when you already
use them. The facade owns the parts every driver would otherwise repeat: JSON
encoding, finding the handler, the retry and backoff policy, timeouts, panic
recovery, telemetry, and the failure event.

A queue tracks work through attempts and an explicit outcome; it does not make
completion unconditional. If you only want to tell other code that something
happened, and it does not matter whether anyone listens, use
[events](../events/overview.md) inside a process or [broadcasting](../broadcasting/overview.md)
between processes.

Focused examples show the ordinary success path and leave error results visible
without routine checks. Handle those errors in your application; examples about
failure behavior and tests keep the relevant checks.

## Delivery guarantees

Arkoren provides no unconditional at-least-once delivery or exactly-once execution
guarantee across all drivers and retry paths. A successful `Dispatch` means the
driver accepted the message, not that a handler processed it. Redelivery depends
on a retained message,
the driver's retry support, and the broker's storage, retention, delivery limits,
and forwarding policy. Compare the [driver guarantees and limits](drivers.md#compatibility-matrix)
before choosing a backend.

The consumer requests acknowledgement after a handler returns `nil`. If the
handler's effect succeeds but acknowledgement fails, the message can return and
repeat that effect. A dispatch error can also leave acceptance uncertain: the
broker may have accepted the message before its reply was lost. Retrying the
dispatch can create another copy.

Persistence does not protect every retry path. RabbitMQ confirms a retry transfer
before acknowledging its source, but that confirmation covers the retry queue,
not later dead-letter forwarding. A lost source acknowledgement can duplicate
work, while forwarding failure or queue expiry can discard it; see
[RabbitMQ's forwarding and retention requirements](drivers.md#retry-forwarding-and-retention).
The memory driver has no restart durability: process exit or `Stop` loses pending
jobs and delayed retries. [Canceling a memory consumer](consuming-jobs.md#canceling-an-in-memory-consumer)
instead preserves work not yet admitted.

Make repeats harmless with a stable business identifier in the payload, such as
an invoice ID, and enforce uniqueness where the effect is stored. The handler
does not receive the transport message ID, and each facade dispatch creates a
fresh one. For remote effects, use an idempotency key and reconcile uncertain
outcomes with the remote service before repeating an operation; a local completion
flag does not make the remote effect atomic. [Writing reliable handlers](consuming-jobs.md#writing-reliable-handlers)
explains the failure windows.

A database commit and dispatch are not atomic either. Record dispatch intent
durably when losing work between them is unacceptable; [Producing Jobs](producing-jobs.md#dispatching)
shows that boundary. Do not depend on queue ordering: competing consumers and
retries can reorder work. If order matters, enforce it in application state;
idempotency alone prevents duplicate effects, not out-of-order effects.

## Jobs, dispatchers, and consumers

A job has a name, a payload type, and the settings that govern how it runs. You
declare it once, as a package-level variable, so that the code that dispatches
it and the code that handles it share one definition:

```go
import "arkoren.dev/core/service/queue"

type SendWelcome struct {
	UserID int64 `json:"user_id"`
}

var SendWelcomeJob = queue.NewJob[SendWelcome]("send-welcome")
```

The type parameter fixes what a dispatch may carry and what a handler receives,
so passing the wrong payload is a compile error. This uses the `default` queue
and the default retry policy. [Producing Jobs](producing-jobs.md) covers the
settings in full.

To send a job, build a dispatcher. A consumer uses the same driver to receive
jobs; the memory driver keeps both sides in one process:

```go
import "arkoren.dev/core/service/queue/memoryqueue"

driver := memoryqueue.New()
dispatcher := queue.NewDispatcher(driver)
consumer := queue.NewConsumer(driver)
```

Register a handler before running the consumer. Here `mailer` is your own
welcome-mail service, whose `SendWelcome` method accepts a user ID:

```go
err := consumer.Handle(SendWelcomeJob, func(ctx context.Context, job SendWelcome) error {
	return mailer.SendWelcome(ctx, job.UserID)
})
```

Run the consumer through the [application lifecycle](#wiring-into-the-application).
Once it is running, submit work with the dispatcher:

```go
err := dispatcher.Dispatch(ctx, SendWelcomeJob, SendWelcome{UserID: 42})
```

`Dispatch` encodes the payload as JSON, wraps it in a message with a random ID,
and hands it to the driver. The consumer receives the message, finds the
handler by the job's name, decodes the payload into a fresh `SendWelcome`, and
calls your function. A handler that returns `nil` requests acknowledgement.
Ordinary errors request retry while attempts remain; permanent errors and
rejections stop the normal retry path, as described in
[Retries and failures](#retries-and-failures).

The dispatching and the consuming process do not need to be the same, and
usually are not. A web process builds a dispatcher and never runs a consumer; a
worker process registers handlers and runs one. They agree through the job's
name, the queue name, and the driver's configuration, so keep job definitions in
a package both can import. [Consuming Jobs](consuming-jobs.md) covers handlers
and the worker side.

## Wiring into the application

If your configured `driver` is RabbitMQ or NATS, it connects at application startup
and releases its owned connection during cleanup. With an existing
`*application.Application` named `app`, import `arkoren.dev/core/application` and
pass this callback to `app.Run(ctx, compose)`. Register the driver hooks, a health
check, and the consumer's runner so the application owns their lifetimes:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	composer.Check("queue", driver.Ping)
	composer.Run(consumer.Run)
}
```

`Check` needs a driver that has `Ping`, which the RabbitMQ and NATS drivers do.
The memory driver has `Start` and `Stop` but no `Ping`, and the SQS and
Cloudflare drivers have none of the three, so there is nothing to register for
them except the runner. [Lifecycle and Ownership](../application/lifecycle-and-ownership.md)
explains why connecting happens in `Start` and what the application does when a
[runner fails](../application/lifecycle-and-ownership.md). A consumer that stops with an error stops the whole service, so a
worker that lost its broker is restarted by your platform instead of idling.

For the local memory setup, `Start` is a no-op. Register `driver.Stop` for cleanup
after consumers stop; it discards queued work and delayed retries rather than
draining them. Cancel and join consumers first if their final settlement must
finish before the driver closes.

Telemetry and events are opt-in, not required for dispatch or consumption. See
[Queue Metadata and Telemetry](metadata-and-telemetry.md#telemetry) for tracing
both sides, and [failure notifications](consuming-jobs.md#when-a-job-fails-for-good)
for the consumer's event bus.

## Retries and failures

Most background failures are temporary: a partner API times out, a database
fails over. The consumer therefore retries by default. When a handler returns an
ordinary error, the consumer asks the driver to deliver the message again after
a delay and uses the driver's reported attempt count. A job's `Retry` setting is
the total number of attempts including the first, and its `Backoff` shapes the
delay between them. When the attempts are used up, the consumer requests terminal
failure handling from the driver. With an event bus configured, it publishes
`queue.job.failed` only after the driver returns success from that settlement.

If welcome mail needs its own queue and up to five attempts, replace the baseline
job declaration with this policy:

```go
var SendWelcomeJob = queue.NewJob[SendWelcome]("send-welcome", func(job *queue.JobBuilder) {
	job.OnQueue("emails")
	job.Retry(5)
})
```

`OnQueue` selects `emails` instead of `default`; `Retry(5)` sets five total
attempts, not five retries. The facade enforces that limit when the driver reports
attempt counts; otherwise the driver's own redelivery limit applies. Dispatchers
and consumers must share this definition.

A handler can also take the decision itself. It can ask for a specific delay,
reject the job without requesting a facade retry, or mark an error as permanent
when trying again cannot help, such as a payload that refers to a user who was
deleted. An undecodable payload or a job still unknown after its grace period
also requests terminal failure. A recovered handler panic instead follows the
ordinary retry and attempt-limit policy. [Consuming Jobs](consuming-jobs.md#handler-outcomes)
lists every outcome and the driver's rejection differences.

What "failed for good" leaves behind depends on the driver. RabbitMQ can publish
the failed job to a failure queue, NATS to a failure subject, and SQS to a failure
queue; the memory and Cloudflare drivers log and drop it. If final settlement
fails, no `queue.job.failed` event is published, and redelivery can remain possible.
A rejection publishes its separate event only when accepted; it does not publish
`queue.job.failed`. An accepted terminal settlement does publish `queue.job.failed`,
carrying the job's ID, name, queue, attempt count, and error, so you can alert or
record the outcome. It does not prove that a failure archive exists or that a
remote effect was rolled back. The payload is not part of the event.
[Built-in events](../events/built-in-events.md#queues)
lists it with the other services' events.

## Testing

Test code that dispatches jobs with the fake from `queuetest`. It is a driver
that records every dispatch, so you can assert on what was queued without
running any handler. In a test with `t *testing.T`, call your application's
`Register` function with a fake-backed dispatcher:

```go
import "arkoren.dev/core/service/queue/queuetest"

fake := queuetest.New()
dispatcher := queue.NewDispatcher(fake)

if err := Register(t.Context(), dispatcher, 42); err != nil {
	t.Fatal(err)
}

fake.AssertDispatched(t, SendWelcomeJob, func(payload SendWelcome) bool {
	return payload.UserID == 42
})
```

To test a handler through the real consumer, register it and call `Drain`. It
runs currently due jobs synchronously, with the same decoding, retry, rejection,
and failure handling as production, and returns when nothing is left to run.
Delayed retries stay pending unless you pass `queuetest.WithScheduled()`;
`Drain` also has a delivery limit to catch infinite retry loops. The
fake also keeps the `Failed` and `Rejected` messages for assertions. The
[Fakes](../testing/fakes.md#running-the-dispatched-jobs) guide shows `Drain`
next to the other services' fakes, and the same suite that checks your own driver
is described in [Conformance suites](../testing/conformance-suites.md).

For an end-to-end test that needs no broker, use `memoryqueue` itself: it
behaves like a real driver, including delayed retries, and works inside
`testing/synctest`.

For every function and option, see [the `queue` API reference](https://pkg.go.dev/arkoren.dev/core/service/queue)
and [`memoryqueue`](https://pkg.go.dev/arkoren.dev/core/service/queue/memoryqueue).
