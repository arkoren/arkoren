---
title: Monitoring
description: Propagate trace context through notifications and interpret broadcast spans and metrics without recording message contents.
order: 40
summary:
  - Connect producer and consumer traces through message metadata.
  - Carry bounded application metadata without treating it as routing or authorization.
  - Interpret broadcast spans and metrics as attempts and deliveries, not unique effects.
  - Protect broker data and verify instrumentation with recording fakes.
---

# Monitoring

An order request can publish one notification that several services handle
later. Trace propagation connects those deliveries to the publication; broadcast
spans and metrics show transport calls and listener execution. They do not prove
that every intended recipient received the notification.

Broadcasting carries trace context in **metadata**, a string map beside the
application payload. Telemetry can record routing names and error classes
without recording payloads, metadata values, or error text. Broker readers can
still read the message body, including that metadata.

## Connecting Telemetry

By default, publishers and consumers resolve the
[shared telemetry handle](../observability/telemetry.md#shared-handle) at each
operation. Its initial no-op records and propagates nothing. If your application
configures that shared handle, facade construction needs no telemetry override.

For an explicit application handle, configure both sides. With an existing
`app` and configured broadcast `driver`:

```go
import "arkoren.dev/core/service/broadcast"

publisher := broadcast.NewPublisher(driver, func(sender *broadcast.PublisherBuilder) {
	sender.Telemetry(app.Telemetry())
})
consumer := broadcast.NewConsumer(driver, func(receiver *broadcast.ConsumerBuilder) {
	receiver.Telemetry(app.Telemetry())
})
```

The publisher injects context using the telemetry driver's propagator; the
consumer extracts it before running listeners. Both sides need compatible
propagation. Export configuration belongs in the
[telemetry guide](../observability/telemetry.md). Instrumentation does not add
retention, retries, or different listener routing.

## Message Metadata

The typed `Publisher.Publish` operation has no metadata parameter. It allocates
a fresh propagation map when enabled telemetry injects values, such as W3C
`traceparent` and `tracestate`. Typed listeners receive only their decoded
payload; pattern listeners can inspect the whole envelope.

If you integrate directly with a driver, you can supply application metadata
yourself. This intentionally bypasses facade JSON encoding and producer telemetry.
With a configured `driver`, `ctx`, and `broadcast` imported, send valid payload
JSON plus a nonsecret correlation identifier:

```go
message := broadcast.Message{
	Name:    "order.created",
	Payload: []byte(`{"order_id":"o-1042"}`),
	Metadata: map[string]string{
		"correlation-id": "order-import-7",
	},
}
err := driver.Publish(ctx, message)
```

A receiving pattern listener sees `message.Metadata["correlation-id"]` as
`order-import-7`. Metadata does not select listeners or authorize the message.
To observe that field with an existing application `logger`, allowlist a
nonsecret value and treat values from external publishers as untrusted:

```go
unsubscribe, err := consumer.ListenPattern("order.#",
	func(ctx context.Context, message broadcast.Message) error {
		logger.InfoContext(ctx, "order notification",
			"correlation", message.Metadata["correlation-id"])
		return nil
	})
if err != nil {
	return err
}
defer unsubscribe()
```

Missing metadata permits map lookup and yields an empty string. Do not log a
field merely because it is metadata. Keep payload bytes and the metadata map
read-only because matching listeners share them.

Drivers preserve metadata within this portable round-trip guarantee:

| Limit | Value |
| --- | --- |
| Entries | At most 16. |
| Key | 1–64 bytes from `A-Za-z0-9._-`, case-sensitive. |
| Value | Valid UTF-8. |
| Combined Size | At most 4096 bytes of keys and values together. |

The constants are `MaxMetadataEntries`, `MaxMetadataKeyLength`, and
`MaxMetadataSize` in `broadcast`. Beyond these limits a driver may reject a
message. Nil and empty metadata are equivalent. A message published without
metadata arrives with a nil map; existing messages without metadata remain
decodable.

External drivers carry metadata in the JSON envelope body, not MQTT user
properties, AMQP headers, or SNS propagation attributes. Those protocols' header
limits therefore do not define this map's limits; broker message-size limits
still apply.

## Reading Spans

A publish that reaches the driver records producer span `send order.created`.
A valid message routed by the consumer records `process order.created` around
all matching listeners. When propagation is present and extracted, the process
span is a child of the send span in the same trace. Listener contexts carry that
span, allowing context-aware logs and downstream calls to join the trace.

Three independent recipients can therefore produce three child process spans.
Redelivery adds more process spans; a process span is not an exactly-once
receipt. A valid unmatched delivery also records processing even though no
listener effect occurs.

| Attribute | Meaning |
| --- | --- |
| `messaging.system` | Driver system, or an explicit override. |
| `messaging.operation.name` | `send` or `process`. |
| `messaging.operation.type` | `send` or `process`; spans only. |
| `messaging.destination.name` | Logical broadcast name. |
| `messaging.message.id` | Nonempty delivery ID; spans only. |
| `error.type` | Error class on failure. |

Driver systems are `memory`, `redis`, `nats`, `rabbitmq`, `mqtt`, `aws_sns`, and
`aws_sqs`. SNS publication followed by SQS consumption correctly shows different
systems on the two sides. `PublisherBuilder.System` and `ConsumerBuilder.System`
override the driver name. If neither supplies one, the value is `_OTHER`.
Broadcast spans do not include queue job or attempt attributes.

Invalid publication names and payload-encoding errors occur before send
instrumentation. Invalid delivery names, or broker-driver rejection of malformed
or routing-mismatched envelopes, occur before facade process instrumentation.
They produce no corresponding facade span or metric. Use provider diagnostics
for transport drops; absence of a process span alone does not identify the cause.

## Reading Metrics

All four instruments use scope `arkoren.dev/core/service/broadcast`, distinct
from queues even though their metric names are shared:

| Metric | What It Measures |
| --- | --- |
| `messaging.client.sent.messages` | Publish attempts that reach the driver, including failures. |
| `messaging.client.consumed.messages` | Valid routed deliveries, including failures and repeats. |
| `messaging.client.operation.duration` | Publish-call duration, in seconds. |
| `messaging.process.duration` | All matching listeners' processing duration per delivery, in seconds. |

Metric attributes are `messaging.system`, `messaging.operation.name`,
`messaging.destination.name`, and `error.type` on failure. Counts are neither
unique application events nor successful recipient effects. One facade call
with an internal RabbitMQ publication retry still produces one send observation.

A failed publish or listener delivery marks its span as erroneous and records
an error class, never the error text. Cancellation and deadline errors use
`context.Canceled` and `context.DeadlineExceeded`. Otherwise classification follows
the single-cause unwrap chain; a multi-cause error has no single root and uses
its aggregate type. [Listener failure events](message-delivery.md#observing-failures)
provide original errors to your application when you need sensitive diagnostics.

## Protecting Data

Recorded broadcast names, systems, durations, error classes, and optional message
IDs are still operational data. Keep names stable and nonsecret: do not embed
customer identifiers or credentials in routing names. Many distinct names also
create metric cardinality; use
[bounded metric retention](../observability/telemetry-drivers.md#prometheus) when
publishers can generate many destinations.

The facade does not record payloads, metadata values, error messages, or panic
stack traces into its telemetry. That boundary does not sanitize your logs,
event observers, native SDK errors, or custom spans. Nor does it encrypt trace
context inside broker messages. Restrict broker access and use transport
encryption; never put credentials in metadata. See
[Telemetry Privacy](../observability/telemetry-privacy.md) for deployment-wide
controls.

## Testing Signals

Use `telemetrytest` with the broadcast recording fake to check instrumentation
without a broker. In a test with `t *testing.T`, import the two fake packages
and configure both facades with the same recorder:

```go
import (
	"arkoren.dev/core/service/broadcast/broadcasttest"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

signals := telemetrytest.New()
fake := broadcasttest.New()
recorder := signals.Telemetry(t)
publisher := broadcast.NewPublisher(fake, func(sender *broadcast.PublisherBuilder) {
	sender.Telemetry(recorder)
})
consumer := broadcast.NewConsumer(fake, func(receiver *broadcast.ConsumerBuilder) {
	receiver.Telemetry(recorder)
})
```

With the [order topic](overview.md#defining-topics), publish and route one
notification, then assert the two observations:

```go
if err := publisher.Publish(t.Context(), OrderCreatedTopic, OrderCreated{OrderID: "o-1042"}); err != nil {
	t.Fatal(err)
}
if err := fake.Drain(t.Context(), consumer); err != nil {
	t.Fatal(err)
}
signals.AssertSpan(t, "send order.created")
signals.AssertSpan(t, "process order.created")
```

No listener is needed to record a valid routed delivery; register one separately
when checking its effect. The fake has no system name, so its spans report
`_OTHER` unless overridden. These assertions prove instrumentation ran, not
network propagation, parentage, or recipient success. See
[Capture Telemetry](../testing/fakes.md#capture-telemetry) for inspecting recorded
spans and metrics more precisely.

For every function and option, see the
[`broadcast` API](https://pkg.go.dev/arkoren.dev/core/service/broadcast) and
[`telemetry` API](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
