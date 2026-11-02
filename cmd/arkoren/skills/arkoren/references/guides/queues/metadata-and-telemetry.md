---
title: Monitoring
description: Trace jobs across processes, count delivery attempts, and observe accepted retry and failure outcomes.
order: 60
summary:
  - Connect dispatch and delivery spans through propagated trace metadata.
  - Interpret attempt metrics separately from accepted settlement events.
  - Protect diagnostics and test queue signals with telemetry fakes.
---

# Monitoring

A report request and its background execution happen at different times, often
in different processes. Queue telemetry connects those operations through trace
context carried beside the payload. Consumer events answer a different question:
which retry, rejection, or terminal-failure settlement did the driver accept?

Neither signal is a report-completion database or a durable failure archive.
Use [Delivery and Retries](delivery-and-retries.md) to interpret ambiguous sends,
acknowledgement gaps, and backend failure handling.

## Trace Continuity

With a configured `driver` and your application's telemetry handle
`app.Telemetry()`, construct the dispatcher with explicit telemetry. Import
`arkoren.dev/core/service/queue` as `queue`:

```go
dispatcher := queue.NewDispatcher(driver, func(producer *queue.DispatcherBuilder) {
	producer.Telemetry(app.Telemetry())
})
```

Configure the worker independently; producer configuration does not reach another
process or configure its consumer:

```go
consumer := queue.NewConsumer(driver, func(worker *queue.ConsumerBuilder) {
	worker.Telemetry(app.Telemetry())
})
```

Without an explicit handle, both follow the [process default](../observability/telemetry.md#shared-handle)
at each operation, initially a no-op. Configure an exporter through the
[telemetry guide](../observability/telemetry.md). Telemetry is not required for
dispatching or consuming and does not change the selected settlement outcome.

Dispatch records a producer span named `send reports`; each delivery records a
consumer span named `process reports`. The dispatcher injects trace context into
message metadata and the consumer extracts it, so attempts can appear as siblings
under the producer span. The handler receives the consumer span through its
context; context-aware logging can correlate its work with that trace.

Spans identify the logical destination with `messaging.destination.name`, the
transport with `messaging.system`, and the operation with
`messaging.operation.name` and `messaging.operation.type`. They also carry
`messaging.message.id`, `arkoren.queue.job`, and `arkoren.queue.attempt` when
available. Shipped system names are `memory`, `rabbitmq`, `nats`, `aws_sqs`, and
`cloudflare_queues`. `System("name")` on either construction builder overrides
driver discovery; unnamed custom drivers use `_OTHER`.

## Attempt Metrics

The queue records these instruments, also used by broadcasting:

| Instrument | Observation |
| --- | --- |
| `messaging.client.sent.messages` | Number of dispatch attempts reaching driver instrumentation, including failures. |
| `messaging.client.consumed.messages` | Number of delivery attempts, not distinct jobs. |
| `messaging.client.operation.duration` | Dispatch operation duration in seconds. |
| `messaging.process.duration` | Decode, execution, and settlement duration in seconds. |

Metric keys are `messaging.system`, `messaging.operation.name`,
`messaging.destination.name`, and `error.type` on errors. Message IDs, job names,
and attempt numbers are span attributes, not metric labels. A job succeeding on
its third attempt contributes three consumed observations. Do not interpret the
consumed counter as the number of successfully completed reports.

The consumer span and error classification describe decoding or the handler
result, **not settlement acceptance**. A `nil` handler result with a failed
acknowledgement still has successful handler telemetry. An ordinary error whose
retry settles successfully still records a failed attempt. Context errors use
`context.Canceled` or `context.DeadlineExceeded`; unknown jobs use
`queue.ErrUnknownName`; other causes use their Go type. Merely exceeding a
deadline does not mark a handler returning `nil` as failed.

## Outcome Events

To observe accepted settlements, supply an existing `*event.Bus` named `bus` from
`arkoren.dev/core/event` when constructing the consumer:

```go
consumer := queue.NewConsumer(driver, func(worker *queue.ConsumerBuilder) {
	worker.Events(bus)
})
```

This is an alternative construction; combine `Events` and `Telemetry` in one
callback if you need both. Nil events publish nothing. The bus is process-local,
not a broker archive or durable notification channel.

| Topic | Meaning |
| --- | --- |
| `queue.JobRetried` (`queue.job.retried`) | Driver accepted a policy retry; carries the requested delay. |
| `queue.JobRejected` (`queue.job.rejected`) | Driver accepted rejection, not proof of native dead-letter transfer. |
| `queue.JobFailed` (`queue.job.failed`) | Driver accepted terminal failure handling. |
| `queue.JobTimedOut` (`queue.job.timed-out`) | Handler returned an error after its execution deadline; emitted before settlement. |

For example, register a failure listener during application composition. Here
`composer` is an existing `*application.Composer` and
`alerts.Record(ctx, jobName, err) error` is your application operation. Import
`context` and `arkoren.dev/core/application` for the surrounding composition code:

```go
unsubscribe, err := bus.Listen(queue.JobFailed, func(ctx context.Context, outcome queue.JobFailedEvent) error {
	return alerts.Record(ctx, outcome.Name, outcome.Err)
})
composer.Require(err)
composer.Defer(unsubscribe)
```

`Require` prevents registering a nil cleanup after listener-registration
failure; `Defer` releases the subscription with its owner. Listeners run
synchronously, so keep them short and honor their context. Listener failures do
not change already accepted settlement.

Outcome payloads identify the message ID, job name, logical queue, and reported
attempt count. Retry, rejection, and failure carry error causes, but no job
payload. `JobFailed` confirms accepted terminal handling, not archive durability
or reversal of external effects. Rejection never also emits `JobFailed`.

Failed settlement emits no accepted-outcome event. An errored timed-out attempt
can emit `JobTimedOut` without a follow-up if settlement fails. Shutdown retries
and unknown-name retries do not emit `JobRetried`; the shutdown retry path also
skips timeout notification. Successful handlers emit none of these events.
Missing events therefore do not prove absence of attempts or broker side effects.
See [built-in queue events](../events/built-in-events.md#queues) for their types.

## Message Metadata

`queue.Message.Metadata` is a string map traveling beside the JSON payload.
Broker drivers carry it inside the body, not as native message attributes, so
SQS's native attribute-count limit does not constrain it. It never selects the
queue or handler. A message without metadata arrives with a nil map; nil and
empty are equivalent.

Portable round trips cover at most 16 entries, keys of 1–64 bytes from
`A-Za-z0-9._-` (case-sensitive), valid UTF-8 values, and at most 4096 bytes of
keys plus values. Messages outside those bounds may be rejected. Constants
`MaxMetadataEntries`, `MaxMetadataKeyLength`, and `MaxMetadataSize` expose those
bounds.

`Dispatch` has no caller-metadata argument. With enabled telemetry it creates a
fresh propagation map, including `traceparent` and `tracestate` when supplied by
the propagator. Raw driver callers can supply metadata themselves, but then own
validation and encoding. Typed handlers receive the decoded payload and context,
not the raw envelope or an arbitrary metadata-map argument.

## Privacy Boundaries

Automatic telemetry records names, IDs, durations, attempt counts, systems, and
error classes. It does not record payloads, metadata values, or error text.
Choose names safe to export rather than embedding personal data in destinations
or job names. Anyone who can read broker messages can still inspect their payload
and propagated trace metadata.

Driver logs, handler logs, event error causes, native SDK diagnostics, and failure
archives are separate surfaces; they can contain unsanitized data and error text.
Keep secrets out of errors, apply logger redaction, and restrict broker/archive
access and retention. [Telemetry Privacy](../observability/telemetry-privacy.md)
explains deployment-level controls.

## Testing Signals

Use `telemetrytest` to inspect recorded spans without an exporter. In a test with
`t *testing.T`, import `arkoren.dev/core/service/queue`, its `memoryqueue`
subpackage, `arkoren.dev/core/service/telemetry`, and its `telemetrytest`
subpackage. Use the report definition from
[Producing Jobs](producing-jobs.md#defining-jobs):

```go
signals := telemetrytest.New()
dispatcher := queue.NewDispatcher(memoryqueue.New(), func(producer *queue.DispatcherBuilder) {
	producer.Telemetry(signals.Telemetry(t))
})
```

Dispatch one report and check the producer observation:

```go
if err := dispatcher.Dispatch(t.Context(), BuildReportJob, BuildReport{ReportID: "rpt-204"}); err != nil {
	t.Fatal(err)
}
signals.AssertSpan(t, "send reports", telemetry.String("arkoren.queue.job", "build-report"))
signals.AssertCounter(t, "messaging.client.sent.messages", 1,
	telemetry.String("messaging.destination.name", "reports"))
```

This checks producer instrumentation, not worker execution or broker retention.
To inspect `process reports`, give the consumer the same fake telemetry and
[drain a fake-backed job](consuming-jobs.md#testing-handlers). Assert event
publication separately from spans when testing settlement behavior.

See the [`queue` API](https://pkg.go.dev/arkoren.dev/core/service/queue) and
[`telemetrytest` API](https://pkg.go.dev/arkoren.dev/core/service/telemetry/telemetrytest).
