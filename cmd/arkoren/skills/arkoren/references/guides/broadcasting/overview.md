---
title: Broadcasting Overview
nav: Overview
description: Announce typed facts and route received notifications to independent listeners.
order: 10
summary:
  - Distinguish fan-out notifications from queued work and in-process events.
  - Publish typed order facts and register exact or wildcard listeners.
  - Separate transport acceptance from active subscriptions and recipient effects.
  - Assert publication requests without claiming broker delivery.
---

# Broadcasting Overview

When an order is created, a search index and a dashboard can both need to hear
about it. A **broadcast** announces that fact without making the publisher know
which services listen. The publisher sends once; the transport determines which
subscribers receive a copy.

Arkoren separates publishing from receiving. A `broadcast.Topic[T]` names the
fact and fixes its payload type. A `Publisher` JSON-encodes that payload; a
`Consumer` routes received messages to registered listeners. A
[driver](drivers.md) connects these facades to an in-process broker or an external
service. Constructing a facade neither starts nor takes ownership of its driver.

## Choosing Broadcasts

Use broadcasts for notifications whose recipients can recover current state,
such as refreshing an order index. Most broadcast drivers do not retain messages
for offline subscribers. A successful publish confirms transport acceptance,
not that any recipient handled the notification.

Use a [queue](../queues/overview.md) to dispatch work to competing workers with
a retry policy, such as generating an invoice. Use an
[event](../events/overview.md) to notify listeners within the same process.
Broadcasting is not a substitute for either a durable work queue or a direct
in-process call.

Independent subscribers ordinarily each receive a copy. Replicas do not
automatically share that copy: use a [NATS queue group](drivers.md#nats), or
an [SNS subscription with one SQS queue per service](drivers.md#sns). Replicas
of a service then compete on its queue; independent services retain separate
subscriptions.

## Defining Topics

Publish an order identifier rather than a complete private order record when
recipients can load the authorized data themselves. Share the topic declaration
and JSON contract between publisher and listener:

```go
import "arkoren.dev/core/service/broadcast"

type OrderCreated struct {
	OrderID string `json:"order_id"`
}

var OrderCreatedTopic = broadcast.NewTopic[OrderCreated]("order.created")
```

A name contains dot-separated tokens, each 1–64 lowercase letters, digits, or
hyphens, without a leading or trailing hyphen. The complete name is at most
255 bytes. Topic construction retains the name without validation; publication
and registration return an error matching `broadcast.ErrInvalidName` for invalid
names. They do not normalize names.

Topics sharing a name must agree on JSON payload semantics, even when services
declare them separately. [AsyncAPI](../asyncapi/overview.md) can describe this
wire contract; generating a document does not configure subscriptions.

## Publishing Facts

With an existing `driver` from [Drivers](drivers.md) and operation context `ctx`,
wrap the publishing side and announce the committed order:

```go
publisher := broadcast.NewPublisher(driver)

err := publisher.Publish(ctx, OrderCreatedTopic, OrderCreated{
	OrderID: "o-1042",
})
```

The envelope carries name `order.created` and JSON payload
`{"order_id":"o-1042"}`. A nil error means acceptance by the driver, not listener
completion or retention. Encoding errors occur before transport access; driver
errors retain their causes. Focused snippets leave routine error results visible;
your application must handle them.

With non-retaining drivers, publishing before subscribers are active loses the
notification. Driver setup and listener registration alone do not establish an
active subscription.

Commit the order before publishing it. The commit and publish are separate
operations: a crash between them can lose the notification. If that loss is
unacceptable, persist the intent to publish alongside the order and arrange
recovery. Retrying an uncertain publication can duplicate a notification.

## Listening

In a receiving service, use a driver connected to the publisher's scope, such as
the same Redis prefix or RabbitMQ exchange. SNS instead uses a subscribed SQS
queue. With your application's `search` indexer, register a typed listener:

```go
consumer := broadcast.NewConsumer(driver)

unsubscribe, err := consumer.Listen(OrderCreatedTopic,
	func(ctx context.Context, order OrderCreated) error {
		return search.Index(ctx, order.OrderID)
	})
if err != nil {
	return err
}
defer unsubscribe()
```

An accepted delivery decodes into `OrderCreated`, then calls the indexer with
`o-1042`. Keep the registration alive for as long as you need it; the deferred
unsubscribe belongs to a scope that outlives consumption, not a short-lived
setup function.

Registration does not subscribe to the broker. Run the consumer in your
application's supervised scope:

```go
err := consumer.Run(ctx)
```

`Run` blocks until cancellation or a driver error. Starting a goroutine or
registering an application runner is not a subscription-ready signal. For
non-retaining drivers, establish active consumption before publishing a
notification you need to observe. [Drivers](drivers.md#driver-lifecycle) owns
connection and cleanup setup.

Listener errors do not reach the publisher, and they do not by themselves end
`Run`. The driver decides whether to retry. A retry can invoke successful
listeners again, so make repeated notifications safe. See
[Message Delivery](message-delivery.md) for cancellation, ordering, and failure
handling.

## Matching Names

To observe several order facts, register a pattern listener. It receives the
complete envelope rather than a decoded application value. With an existing
`logger`, record only its routing name:

```go
unsubscribe, err := consumer.ListenPattern("order.#",
	func(ctx context.Context, message broadcast.Message) error {
		logger.InfoContext(ctx, "order notification", "name", message.Name)
		return nil
	})
if err != nil {
	return err
}
defer unsubscribe()
```

`order.#` matches `order`, `order.created`, and `order.created.eu`. A final `#`
matches zero or more tokens; `*` matches exactly one. Thus `order.*` matches only
the middle example. Wildcards must fill whole tokens, and `#` must be last:
`order.cre*` is invalid.

Every matching listener runs, including an exact listener and a pattern listener
on the same consumer. Raw payload bytes and metadata are shared between these
listeners; do not mutate them. Avoid logging their contents. The
[monitoring guide](metadata-and-telemetry.md) explains metadata and privacy.

## Testing Publications

To check that application code announces an order without running a broker, use
the recording fake. In a test with `t *testing.T`, assume `CreateOrder` accepts
the publisher and publishes the topic above after saving the order:

```go
import "arkoren.dev/core/service/broadcast/broadcasttest"

fake := broadcasttest.New()
publisher := broadcast.NewPublisher(fake)

if err := CreateOrder(t.Context(), publisher, "o-1042"); err != nil {
	t.Fatal(err)
}
fake.AssertPublished(t, OrderCreatedTopic, func(order OrderCreated) bool {
	return order.OrderID == "o-1042"
})
```

This proves the publication request and payload, not broker delivery. To exercise
registered listeners, `fake.Drain(t.Context(), consumer)` routes recorded
messages synchronously and returns joined listener errors without redelivery.
The [testing guide](../testing/fakes.md#separate-execution) explains why requesting
work and executing its effects need separate assertions.

For every function and option, see the
[`broadcast` API reference](https://pkg.go.dev/arkoren.dev/core/service/broadcast).
