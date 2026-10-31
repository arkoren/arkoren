---
title: Queue Metadata and Telemetry
nav: Metadata & Telemetry
description: How a job's trace context travels in message metadata, the spans and metrics dispatching and consuming record, and how to test it.
order: 40
---

# Queue Metadata and Telemetry

## Introduction

A job leaves the process that dispatched it and runs somewhere else, later. That
is exactly where a trace usually breaks: the request that queued the work ends,
and the worker's activity has nothing to connect it to. The queue repairs this
with **metadata**, a small map of strings that travels beside the payload and
carries the trace context from the dispatcher to the consumer. On top of it
the queue records **telemetry**: a span and metrics for every dispatch and every
delivery attempt.

Trace propagation and recording use explicit telemetry or the
[process default](../observability/telemetry.md#the-process-default), initially
a no-op. They never
change the outcome of a dispatch or a job, and neither records payloads or
metadata values. The [telemetry guide](../observability/telemetry.md) explains
how to export what the queue records.

## Message metadata

Every queue message has a `Metadata` map of strings, sent as a `metadata` field
in the message and omitted when empty. Drivers deliver it unchanged: the consumer
sees the same keys and values the dispatcher sent, and a message sent without
metadata arrives with a nil map, so messages queued before metadata existed still
decode. The broker drivers carry it inside the message body, which is why broker
limits on headers and attributes, such as the ten message attributes of SQS,
never apply to it.

For a delivered message, the portable metadata round-trip contract covers these
limits:

| Limit | Value |
| --- | --- |
| Entries | At most 16 |
| Key | 1 to 64 bytes from `A-Za-z0-9._-`, case-sensitive |
| Value | Valid UTF-8 |
| Total | At most 4096 bytes of keys and values together |

Beyond them a driver may reject the message. The constants are
`queue.MaxMetadataEntries`, `queue.MaxMetadataKeyLength`, and
`queue.MaxMetadataSize`.

In practice the only writer is the queue itself. `Dispatch` has no parameter for
metadata; with telemetry enabled it adds the keys that propagate the trace,
`traceparent` and `tracestate`, to a new map, and never modifies a map you own.
The map exists on the message so that a custom driver, or code that talks to a
driver directly, can carry its own values, and so that the consumer can read
them. Metadata is not part of the payload and never affects which queue or
handler a message reaches.

## Telemetry

Give the dispatcher and the consumer the application's telemetry; without it
they use `telemetry.Default()`, which records nothing until you set one:

```go
import "arkoren.dev/core/service/queue"

dispatcher := queue.NewDispatcher(driver, func(dispatcher *queue.DispatcherBuilder) {
	dispatcher.Telemetry(app.Telemetry())
})
consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
	consumer.Telemetry(app.Telemetry())
})
```

Both sides need it. The dispatcher starts the producer span and injects the
trace context; the consumer extracts it. A consumer without telemetry still runs
the job, but its work is not part of the trace.

### Spans

Each dispatch records a producer span named `send {queue}`, such as `send emails`.
Each delivery attempt records a consumer span named `process {queue}` that is a
child of the producer span, in the same trace, so a trace that began in an HTTP
request continues through the worker that ran its job. The handler's context
carries the consumer span, so logs written with the `*Context` methods get the
trace ID. A retry is a new consumer span with a higher attempt, which shows up as
a series of siblings under the one producer span.

| Attribute | Value |
| --- | --- |
| `messaging.system` | The driver's system, such as `memory`, `rabbitmq`, `nats`, `aws_sqs`, or `cloudflare_queues` |
| `messaging.operation.name` | `send` or `process` |
| `messaging.operation.type` | `send` or `process`; span only |
| `messaging.destination.name` | The queue name |
| `messaging.message.id` | The message ID; span only |
| `arkoren.queue.job` | The job name; span only |
| `arkoren.queue.attempt` | The attempt, when the driver reports one; span only |
| `error.type` | Set on failure |

The driver names its system. If it names none, or the wrong one, set it with
`System("name")` on both `DispatcherBuilder` and `ConsumerBuilder`; without either,
the system is `_OTHER`.

### Metrics

Four metrics are recorded, all with the keys `messaging.system`,
`messaging.operation.name`, `messaging.destination.name`, and `error.type`. The
counters `messaging.client.sent.messages` and `messaging.client.consumed.messages`
count dispatches and delivery attempts. The histograms
`messaging.client.operation.duration`, for the dispatch call, and
`messaging.process.duration`, for decoding, execution, and settlement of one
delivery, are in seconds. They follow the OpenTelemetry messaging conventions,
and the broadcast side records the same
four, so one dashboard serves both. [Instrumented components](../observability/instrumented-components.md#queues-and-broadcast)
lists them next to the other components.

The consumer counts every attempt, not every job. A job that succeeds on its
third attempt adds three to the consumed counter, one of them without an error,
which is what lets you chart the retry rate.

### Failures

A failed dispatch sets the producer span to the error status and `error.type`
to the class of the error. A delivery attempt fails when the handler returns an
error, including `queue.RetryAfter`, `queue.Reject`, and `queue.Permanent`, a
recovered panic, and a timeout; the consumer span records the class of the root
cause. A message that names no handler reports `queue.ErrUnknownName`. Returned
context cancellation or deadline errors are classified as `context.Canceled` or
`context.DeadlineExceeded`; other errors use their Go type. Merely passing a
deadline does not make a successful handler an error.

The consumer span reports the decoding or handler outcome, not settlement
acceptance. A handler can return `nil` and still encounter an acknowledgement
failure, or return an error whose retry settles successfully. For accepted retry,
rejection, and terminal-failure notifications, configure the
[consumer's event bus](consuming-jobs.md#when-a-job-fails-for-good). `JobFailed` is
published only after successful terminal settlement, never for a failed settlement
or a rejection.

The text of an error is never recorded, because it can carry payload data. A span
tells you that an attempt failed with `*net.OpError`, not what the message said;
look in your logs for the text, correlated by trace ID.

## What is recorded and what is not

Recorded: the queue name, the job name, the message ID, the attempt, the
messaging system, durations, and the class of an error. Never recorded: payloads,
metadata values, and error text. The metadata does hold the propagated trace
context, which is how the trace crosses the broker. Anyone who can read the broker can read it, as they can read the payload;
[Telemetry privacy](../observability/telemetry-privacy.md) discusses what that
means for your deployment.

## Testing

To assert what your code records, use `telemetrytest`. Its fake keeps spans and
metrics in memory. In a test with `t *testing.T`, dispatch the welcome job from
[Queues Overview](overview.md#jobs-dispatchers-and-consumers) to the memory
driver and check the producer side without running a worker:

```go
import (
	"arkoren.dev/core/service/queue/memoryqueue"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

fake := telemetrytest.New()
dispatcher := queue.NewDispatcher(memoryqueue.New(), func(dispatcher *queue.DispatcherBuilder) {
	dispatcher.Telemetry(fake.Telemetry(t))
})

if err := dispatcher.Dispatch(t.Context(), SendWelcomeJob, SendWelcome{UserID: 42}); err != nil {
	t.Fatal(err)
}

fake.AssertSpan(t, "send emails",
	telemetry.String("arkoren.queue.job", "send-welcome"),
	telemetry.String("messaging.system", "memory"),
)
fake.AssertCounter(t, "messaging.client.sent.messages", 1,
	telemetry.String("messaging.destination.name", "emails"),
)
```

Give the consumer the same `fake.Telemetry(t)` and run a job with `queuetest`'s
`Drain` to assert on the `process emails` span. [Fakes](../testing/fakes.md#asserting-on-telemetry)
covers the telemetry fake.

For every function and option, see [the `queue` API reference](https://pkg.go.dev/arkoren.dev/core/service/queue)
and [the `telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
