---
title: Broadcast Metadata and Telemetry
nav: Metadata & Telemetry
description: How the trace context of a publish reaches listeners through message metadata, the spans and metrics recorded, and how to test it.
order: 20
---

# Broadcast Metadata and Telemetry

## Introduction

A request can publish an order notification that several services process later.
To follow that work in one trace, the publisher must send trace context with the
message and each consumer must extract it. Broadcasting uses **metadata**, a
small map of strings beside the payload, to carry that context. With telemetry
enabled, it records a span and metrics for each publish that reaches the driver
and each valid message the consumer routes, including in-process delivery.

Trace propagation and recording use explicit telemetry or the
[process default](../observability/telemetry.md#the-process-default), initially
a no-op. They never
change whether a message is published or how a listener runs, and neither records
payloads or metadata values. The [telemetry guide](../observability/telemetry.md)
explains how to export what is recorded. Queues share the same instrumentation, so
[Queue Metadata and Telemetry](../queues/metadata-and-telemetry.md) applies here
too, with the differences below.

## Message metadata

Every broadcast message has a `Metadata` map of strings, sent as a `metadata` field
and omitted when empty. Drivers deliver it unchanged. The listener sees the keys and
values the publisher sent, and a message sent without metadata arrives with a nil
map, so messages that were in flight before metadata existed still decode. The
drivers carry it inside the message body, so the limits of a broker's headers or
attributes never apply: MQTT user properties, AMQP headers, and the ten message
attributes of SNS are not used.

The round trip is guaranteed within limits that every driver is tested against:

| Limit | Value |
| --- | --- |
| Entries | At most 16 |
| Key | 1 to 64 bytes from `A-Za-z0-9._-`, case-sensitive |
| Value | Valid UTF-8 |
| Total | At most 4096 bytes of keys and values together |

Beyond them a driver may reject the message. The constants are
`broadcast.MaxMetadataEntries`, `broadcast.MaxMetadataKeyLength`, and
`broadcast.MaxMetadataSize`.

The facade's `Publish` has no metadata parameter. With telemetry enabled, it
injects trace context into a new map in the telemetry driver's propagation
format (`traceparent` and `tracestate` for W3C Trace Context). A custom
driver or code that talks to a driver directly can carry other values, and a
listener registered with `ListenPattern` receives the whole map in the
`broadcast.Message`. Typed listeners receive only the payload. Metadata does not
affect which listeners receive a message.

## Telemetry

With an existing `app` and configured broadcast `driver`, give the
publisher and consumer the application's telemetry. Without explicit telemetry,
both follow `telemetry.Default()` at use time; its initial value is a no-op:

```go
import "arkoren.dev/core/service/broadcast"

publisher := broadcast.NewPublisher(driver,
	func(publisher *broadcast.PublisherBuilder) {
		publisher.Telemetry(app.Telemetry())
	})
consumer := broadcast.NewConsumer(driver,
	func(consumer *broadcast.ConsumerBuilder) {
		consumer.Telemetry(app.Telemetry())
	})
```

Both sides need it: the publisher injects the trace context and the consumer
extracts it.

### Spans

With telemetry enabled, each publish that reaches the driver records a producer
span named `send {name}`, such as `send order.created`. Each valid message routed
by the consumer records one consumer span named `process {name}`. When the
publisher injects trace context and the consumer extracts it, that span is a
child of the producer span in the same trace. The span covers all the listeners
that matched the message, and their context carries it, so logs written with the
`*Context` methods get the trace ID.
When three services each process one delivery with propagated context, the trace
shows one producer span with three consumer spans beneath it. Redeliveries add
consumer spans; a process span does not prove exactly-once handling.

| Attribute | Value |
| --- | --- |
| `messaging.system` | The driver's system, such as `memory`, `redis`, `nats`, `rabbitmq`, `mqtt`, `aws_sns`, or `aws_sqs` |
| `messaging.operation.name` | `send` or `process` |
| `messaging.operation.type` | `send` or `process`; span only |
| `messaging.destination.name` | The broadcast name |
| `messaging.message.id` | The message ID, when the transport supplies one; span only |
| `error.type` | Set on failure |

The queue-only attributes `arkoren.queue.job` and `arkoren.queue.attempt` do not
appear. The driver names its system; the SNS and SQS pair reports `aws_sns` on the
publishing side and `aws_sqs` on the consuming side. To override it, call
`PublisherBuilder.System` or `ConsumerBuilder.System` in the corresponding
construction callback. If neither the driver nor the builder supplies a name,
the system is `_OTHER`.

### Metrics

The metrics are the same four as for queues, with the keys `messaging.system`,
`messaging.operation.name`, `messaging.destination.name`, and `error.type`. The
counters `messaging.client.sent.messages` and `messaging.client.consumed.messages`
count publish attempts that reach the driver and valid routed deliveries,
including failures and redeliveries, rather than successful listener effects.
The histograms
`messaging.client.operation.duration`, for the publish call, and
`messaging.process.duration`, for the time the listeners of one message took, are in
seconds. The scope is `arkoren.dev/core/service/broadcast`, so you can tell the two
services apart on a shared dashboard. [Instrumented components](../observability/instrumented-components.md#queues-and-broadcast)
lists them next to the other components.

### Failures

A publish that fails sets the producer span to the error status, and `error.type`
to the class of the error. A message fails on the consumer side when any matching
listener returns an error or panics. The class comes from the root cause when
there is one, and a message that failed in several listeners has no single cause
and reports the type of the joined error. A canceled or timed-out context is
reported as `context.Canceled` or `context.DeadlineExceeded`. Error text is never
recorded, because it can carry payload data; use your logs for it, correlated by
trace ID.

An invalid name is rejected before a span starts, so it produces no telemetry;
the publisher gets `broadcast.ErrInvalidName`. Payload encoding also happens
before the producer span. Broker drivers can reject malformed or mismatched
envelopes before the facade sees them, so those drops produce no facade process
span or consumed-message metric.

## What is recorded and what is not

Recorded: the broadcast name, the message ID when there is one, the messaging
system, durations, and the class of an error. Never recorded: payloads, metadata
values, and error text. The metadata does hold the propagated trace context, which is
how the trace crosses the broker; anyone who can read the broker can read it, as they
can read the payload. [Telemetry privacy](../observability/telemetry-privacy.md)
discusses what that means for your deployment.

## Testing

To assert what your code records, use `telemetrytest`. Its fake keeps spans and
metrics in memory. In a test with `t *testing.T`, run a message through the broadcast fake with `Drain` to see
both sides:

```go
import (
	"arkoren.dev/core/service/broadcast/broadcasttest"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

signals := telemetrytest.New()
fake := broadcasttest.New()
recorder := signals.Telemetry(t)
publisher := broadcast.NewPublisher(fake, func(publisher *broadcast.PublisherBuilder) {
	publisher.Telemetry(recorder)
})
consumer := broadcast.NewConsumer(fake, func(consumer *broadcast.ConsumerBuilder) {
	consumer.Telemetry(recorder)
})
```

Register a listener for `OrderCreatedTopic` using the
[consumer testing pattern](overview.md#testing). In your test, publish and drain
the fake before asserting the send and process spans:

```go
if err := publisher.Publish(t.Context(), OrderCreatedTopic, OrderCreated{OrderID: "o-1"}); err != nil {
	t.Fatal(err)
}
if err := fake.Drain(t.Context(), consumer); err != nil {
	t.Fatal(err)
}
signals.AssertSpan(t, "send order.created")
signals.AssertSpan(t, "process order.created")
```

The fake driver names no messaging system, so these spans report `_OTHER`; set
`broadcast.ConsumerBuilder.System` to assert on a particular value. [Fakes](../testing/fakes.md#asserting-on-telemetry)
covers the telemetry fake.

For every function and option, see [the `broadcast` API reference](metadata-and-telemetry.md)
and [the `telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
