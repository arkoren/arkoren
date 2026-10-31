---
title: Broadcasting Overview
nav: Overview
description: Publish typed facts to other processes and listen by name or wildcard, with delivery semantics, listener errors, wiring, and testing.
order: 10
---

# Broadcasting Overview

## Introduction

A broadcast tells other processes that something happened. An order was created,
a document was edited, a sensor reported a reading. The publisher announces the
fact once and does not know who listens: the billing service, a search indexer,
and a dashboard can all subscribe, and each receives its own copy. Adding a
listener changes nothing for the publisher.

Arkoren's broadcasting has two parts. The **facade** is what your code uses: a
typed `broadcast.Topic` that names a fact and fixes its payload, a
`*broadcast.Publisher` that sends it, and a `*broadcast.Consumer` that routes
each received message to the listeners you registered. A **driver** carries the
message to consumers: [memory](drivers.md#memory) inside one process,
[Redis](drivers.md#redis), [NATS](drivers.md#nats), [RabbitMQ](drivers.md#rabbitmq),
or [MQTT](drivers.md#mqtt) over a broker, and [SNS](drivers.md#sns) when listeners
must receive messages that were sent while they were down.

Focused examples show the ordinary success path and leave error results visible
without routine checks. Handle those errors in your application; failure
examples and tests keep the relevant checks.

### Broadcasts, events, and queues

Three tools in Arkoren move a message, and they answer different questions.
Choose by who must act and where they run.

An [event](../events/overview.md) stays in one process. It is delivered by a direct
function call, synchronously. `Emit` logs listener failures and lets the publisher
continue; `Dispatch` returns the error so the publisher can stop its operation.
Use events to decouple parts of one program.

A [queue](../queues/overview.md) holds **work**. Workers compete for each job,
and failures follow a retry policy. Durable drivers keep pending work across
worker restarts, but deliveries can repeat. Use a queue for something that must
happen, with a plan for jobs that exhaust their retries.

A broadcast carries **facts** to interested services. In the ordinary fan-out
setup, each subscribing service receives a copy, but the publisher receives no
confirmation of listener processing. Most drivers lose messages for offline
subscribers. Use broadcasts to keep services informed or caches fresh, not as a
substitute for a job queue's work and retry policy.

By default, three connected replicas each receive the notification and each runs
its listeners. To share notifications among replicas, use a
[NATS queue group](drivers.md#nats) or have the replicas consume the same
[SQS queue](drivers.md#sns). With SNS, give each independent service its own SQS
queue for fan-out; replicas of that service share its queue. A listener can also
publish a job when the resulting work needs a queue's retry policy.

## Topics

To share the wire contract with other services, follow
[the AsyncAPI topic walkthrough](../asyncapi/overview.md#generating-your-first-document). It describes your existing topics
and physical destinations without publishing messages or configuring a broker.

A **topic** ties a name to a payload type. Declare it once, as a package-level
variable, next to the payload, and let publishers and listeners share it:

```go
import "arkoren.dev/core/service/broadcast"

type OrderCreated struct {
	OrderID string `json:"order_id"`
}

var OrderCreatedTopic = broadcast.NewTopic[OrderCreated]("order.created")
```

A name is dot-separated tokens, each 1 to 64 characters from `a-z`, `0-9`, and `-`
with no hyphen at either end, and at most 255 bytes in all. Use the dots to
build a hierarchy, such as `order.created` and `order.canceled`, because
listeners can match on it. An invalid name is not a panic. `Publish` and `Listen`
return `broadcast.ErrInvalidName`.

## Publishing

Once an order is committed, tell other services its identifier so they can load
the current record. Start locally with the bundled memory driver; the consumer
below shares that same instance. Use [Broadcast Drivers](drivers.md) when
notifications must cross processes. Wrap the driver in a publisher;
`Publish` encodes the payload as JSON and sends it with the topic's name:

```go
import "arkoren.dev/core/service/broadcast/memorybroadcast"

driver := memorybroadcast.New()
publisher := broadcast.NewPublisher(driver)

err := publisher.Publish(ctx, OrderCreatedTopic, OrderCreated{OrderID: "o-1"})
```

A nil error means the driver accepted the message, not that a listener processed
it. Whether anyone receives it, and what happens if nobody listens, depends on
the driver. With the memory driver
and the fire-and-forget brokers, a message published while no subscriber is
connected is lost.
Publish after the change is committed, and decide what a failed publish means
for your caller: usually the work is done and the error is logged. Committing
and publishing are separate operations; if a crash between them must not lose
the notification, persist the intent to publish alongside the change.

## Listening

A search service can react to new orders without the order service knowing
about the search index. A consumer routes received messages to matching
listeners. Its driver must use the publisher's transport scope, such as the same
Redis prefix or RabbitMQ exchange; SNS uses a subscribed SQS queue instead of
the publishing driver. Here `search` is your index service; `Listen` registers
its callback for the shared order topic and decodes the payload:

```go
consumer := broadcast.NewConsumer(driver)

_, err := consumer.Listen(OrderCreatedTopic,
	func(ctx context.Context, order OrderCreated) error {
		return search.Index(ctx, order.OrderID)
	})
```

Registration alone does not receive messages. `Run` blocks until its context
ends or the driver fails, so register it as an
[application runner](#wiring-into-the-application). Listeners can be added while
it runs. `Listen` also returns a function that removes the registration;
calling that function more than once is safe.

To send all order notifications to your application's `audit` recorder, use
`ListenPattern`. It takes a pattern and hands you a `broadcast.Message`, with the name, the
JSON payload, and the metadata:

```go
_, err := consumer.ListenPattern("order.#", func(ctx context.Context, m broadcast.Message) error {
	audit.Record(ctx, m.Name, m.Payload)

	return nil
})
```

In a pattern, `*` matches exactly one token and `#`, only as the last token,
matches zero or more. `order.*` matches `order.created` but not `order.created.eu`
or `order`; `order.#` matches all three. A wildcard must fill a whole token, so
`order.cre*` is invalid. One message can match several listeners, and each
decodes the payload on its own, so two listeners may use different types for the
same topic. Drivers that map names onto their own wildcard syntax, such as
RabbitMQ and MQTT, give patterns the same meaning.

## Delivery

The facade does not add durability; the driver decides what a published message
survives. Redis, Core NATS, and RabbitMQ do not retain messages for offline
subscribers. MQTT's owned connections use clean sessions, so offline subscribers
miss messages at every QoS. SNS with SQS retains messages in subscribed queues
for offline consumers, with at-least-once delivery subject to queue policies.
[Broadcast Drivers](drivers.md) states the
guarantee of each. Design for the weaker case. If a missed message would cause
harm, the receiver should be able to recover state another way, such as reloading
from the database at startup, and the broadcast should only speed things up.

Listener errors do not reach the publisher. When a listener returns an error, the
consumer collects the errors of all matching listeners, joins them, and returns
them to the driver. A listener that panics is recovered and counted as an error. What the
driver does next differs: memory, NATS, and RabbitMQ log failures without retrying;
Redis and MQTT do not retry returned listener errors. SQS leaves nonpermanent
failures for another delivery. A redelivery goes to every listener, not only the
failed one, so make listeners
idempotent.

A listener can tell the driver that retrying cannot help by returning
`broadcast.Permanent(err)`. Drivers that redeliver then drop or dead-letter the
message. The consumer applies the same rule itself: a message whose payload does not
decode for a listener, or whose name is invalid, is permanent. A delivery error that
joins several failures counts as permanent only when every failure was.

To see listener failures in your own process, whatever the driver does with
them, give the consumer the application's event bus:

```go
consumer := broadcast.NewConsumer(driver, func(consumer *broadcast.ConsumerBuilder) {
	consumer.Events(app.Events())
})
```

It publishes `broadcast.listener.failed` once for each listener that fails a
delivery, with the message ID, the broadcast name, and the error, but never the
payload; [Built-in Events](../events/built-in-events.md#broadcasting) describes
the payload.

## Wiring into the application

A driver for a broker connects when the application starts. Register its hooks,
a health check, and the consumer as a runner:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	composer.Check("broadcast", driver.Ping)
	composer.Run(consumer.Run)
}
```

Starting a broker driver establishes its connection, not the consumer's
subscription. `consumer.Run` starts consumption; registering a listener or a
runner alone is not a subscription-ready signal. For non-retaining drivers,
wait until consumption is active before publishing a notification you need to
observe.

For the local memory setup, register `driver.Stop` for cleanup and
`consumer.Run` as a runner; `Start` is a no-op and there is no `Ping`.
Cancel and join consumers before cleanup. `Stop` closes admission and waits for
admitted handlers only until its context ends; those handlers must bound their
own work. The SNS and SQS
drivers have none of them, so register only what the driver has. A process that
only publishes builds a publisher and no consumer. Register listeners before the
application runs, so the first message already has someone to receive it.
To trace both sides, set `PublisherBuilder.Telemetry` and
`ConsumerBuilder.Telemetry` in their respective construction callbacks; see
[Broadcast Metadata and Telemetry](metadata-and-telemetry.md).
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md) explains the hooks.

## Testing

Test code that publishes with the fake from `broadcasttest`. It records every
published message and lets you assert on it by topic and payload. In a test with
`t *testing.T`, call your application's `CreateOrder` function with this publisher:

```go
import "arkoren.dev/core/service/broadcast/broadcasttest"

fake := broadcasttest.New()
publisher := broadcast.NewPublisher(fake)

if err := CreateOrder(t.Context(), publisher, "o-1"); err != nil {
	t.Fatal(err)
}

fake.AssertPublished(t, OrderCreatedTopic, func(order OrderCreated) bool {
	return order.OrderID == "o-1"
})
```

To test a listener through a real consumer, register it and call `Drain`, which
routes the recorded messages through your listeners synchronously and returns the
joined listener errors. The fake does not redeliver, so a failing listener is
reported once. [Fakes](../testing/fakes.md) covers the fakes of all services. For a
test across a real consumer without a broker, use `memorybroadcast`: it starts
delivering only to consumers that are already running, so call `Run` before you
publish.

For every function and option, see [the `broadcast` API reference](https://pkg.go.dev/arkoren.dev/core/service/broadcast)
and [`memorybroadcast`](https://pkg.go.dev/arkoren.dev/core/service/broadcast/memorybroadcast).
