---
title: Message Delivery
description: Own subscriptions and consumer cancellation, and handle failed or repeated notifications.
order: 30
summary:
  - Own listener registrations separately from transport subscriptions and running callbacks.
  - Cancel and join consumers without assuming all handler work has ended.
  - Interpret ordered callbacks, joined errors, and permanent failure classification.
  - Observe listener failures and design effects for transport-specific loss or redelivery.
---

# Message Delivery

A registered listener and a broker subscription have different lifetimes.
The consumer owns local listener routing; its driver owns receiving, retention,
and settlement. Understanding that boundary lets you stop a service without
mistaking cancellation for completed work, or a callback error for a retry.

This page assumes a configured [driver](drivers.md) and the
[`OrderCreated` topic](overview.md#defining-topics). Publication acceptance remains
separate from recipient handling: the publisher never receives listener errors.

## Owning Registrations

Use the unsubscribe function when a component no longer needs notifications.
With an existing consumer and application `search` indexer, retain the function
only after successful registration:

```go
unsubscribe, err := consumer.Listen(OrderCreatedTopic,
	func(ctx context.Context, order OrderCreated) error {
		return search.Index(ctx, order.OrderID)
	})
if err != nil {
	return err
}
```

The registration now belongs to your component. On its shutdown, remove it:

```go
unsubscribe()
unsubscribe()
```

Both calls are safe; only the first removes the registration. Removal affects
future delivery snapshots. A delivery that already captured the listener can
still invoke it, so unsubscribe does not join or cancel an in-flight callback.
Do not release callback dependencies until their active users have finished.

Registration and removal are concurrent-safe, and callbacks run outside the
registration lock. Each registration is independent, even if two use the same
topic or function. Removing one does not remove the others or close the driver's
subscription. Conversely, closing a transport does not remove the consumer's
local registrations.

## Stopping Consumers

Prefer an [application runner](../application/lifecycle-and-ownership.md) for a
service-wide consumer. If a smaller component directly owns consumption, give it
a cancelable context and retain the result so its owner can join the runner:

```go
import "context"

runCtx, cancel := context.WithCancel(ctx)
result := make(chan error, 1)
go func() {
	result <- consumer.Run(runCtx)
}()
```

This starts consumption, not a portable readiness handshake. Do not use goroutine
startup as proof that a non-retaining subscription can receive messages.
When the component ends, cancel and wait:

```go
import "errors"

cancel()
err := <-result
if !errors.Is(err, context.Canceled) {
	return err
}
```

The driver returns the context error when cancellation ends consumption;
`Run` preserves driver errors. An earlier transport failure can therefore be the
result instead. Listener failures are delivery outcomes and do not by themselves
end this loop.

Joining `Run` does not establish that every callback has finished. In particular,
memory deliveries already admitted retain context values without the consumer's
cancellation or deadline. NATS cancellation unsubscribes without waiting for a
running callback. Bound work inside the callback when its dependencies need
predictable cleanup:

```go
import "time"

unsubscribe, err := consumer.Listen(OrderCreatedTopic,
	func(ctx context.Context, order OrderCreated) error {
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return search.Index(work, order.OrderID)
	})
```

The timeout only bounds operations that honor their context. If resource release
requires proof that callbacks ended, track and join those callbacks in the owning
component too. Driver `Stop` has provider-specific draining limits; follow
[driver ownership](drivers.md#driver-lifecycle) rather than assuming all drivers
have the same shutdown contract. NATS and MQTT connection shutdown does not end
their `Consume` context waits: cancel consumers separately.

## Listener Ordering

For one delivery, the facade takes a snapshot and invokes every matching listener
sequentially in registration order. An exact listener and a wildcard listener
can both match. A failure does not skip later listeners. A delivery with no
matching listeners succeeds; on SQS that permits deletion of the message.

Typed listeners decode the payload independently. A malformed payload can fail
one listener's decoding without preventing a different listener from running.
Topics sharing a name must still agree on their intended JSON contract; separate
decoding is not schema negotiation.

The facade joins all listener failures and returns the delivery result to the
driver. Listener panics are recovered as errors, including a stack trace.
One delivery's ordering does not order different messages: transports can invoke
several deliveries concurrently. Synchronize shared application state and do not
assume publication order is a portable callback-order guarantee.

Pattern listeners share the envelope's payload bytes and metadata map. Treat
them as read-only; copy data that needs mutation or independent ownership.

## Permanent Failures

If retrying the same input cannot help, mark that failure permanent. For example,
reject an order notification without its required identifier before calling the
indexer:

```go
import "errors"

unsubscribe, err := consumer.Listen(OrderCreatedTopic,
	func(ctx context.Context, order OrderCreated) error {
		if order.OrderID == "" {
			return broadcast.Permanent(errors.New("missing order identifier"))
		}
		return search.Index(ctx, order.OrderID)
	})
```

The returned error matches `broadcast.ErrPermanent` under `errors.Is` and retains
its original cause. `Permanent(nil)` returns nil. Invalid delivery names and
typed payload-decoding failures are permanent automatically.

A joined delivery failure is permanent only if every failed listener is
permanent. Successful listeners do not change that decision. If one failure is
transient and another permanent, the delivery remains retryable. A redelivery
runs every matching listener again, including listeners that previously succeeded.
Use stable application identifiers and idempotent effects; a transport-generated
ID is not a universal application deduplication key.

Permanent does not create retention, a retry policy, or a dead-letter destination.
Non-retaining drivers do not retry either error class. The SQS broadcast driver
deletes permanent failures rather than sending them to a dead-letter queue.

## Observing Failures

To observe callback failures independently of transport policy, give the consumer
your existing `*event.Bus` named `bus`:

```go
consumer := broadcast.NewConsumer(driver, func(receiver *broadcast.ConsumerBuilder) {
	receiver.Events(bus)
})
```

Attach an observer on that bus. With an application `logger`, record the routing
name without exporting the original error:

```go
unsubscribe, err := bus.Listen(broadcast.ListenerFailed,
	func(ctx context.Context, failure broadcast.ListenerFailedEvent) error {
		logger.WarnContext(ctx, "broadcast listener failed", "name", failure.Name)
		return nil
	})
if err != nil {
	return err
}
defer unsubscribe()
```

The topic is `broadcast.listener.failed`. Each failed listener emits one
`ListenerFailedEvent` in listener order, with `ID`, `Name`, and `Err`. ID is empty
when the transport supplies none. Decode failures and recovered panics also
emit events; successful deliveries emit none. Without `Events`, no bus
notification is published. Publishers and drivers do not emit this event.

The event omits message payload and metadata, but `Err` retains application
causes and recovered panic details. Treat it as sensitive diagnostic data, not
safe external output. Observing the event cannot change delivery settlement,
and it is not evidence of broker retention or acknowledgement. See
[built-in service events](../events/built-in-events.md#broadcasting) and
[monitoring privacy](metadata-and-telemetry.md#protecting-data).

## Transport Outcomes

Memory, Redis Pub/Sub, and Core NATS do not retry returned listener errors or
retain offline notifications. Memory and NATS log handler failures; Redis does
not turn those failures into retries. Subscribers must recover missed state
another way, including after a process restart.

RabbitMQ uses auto-acknowledged, ephemeral consumer queues. Listener failure does
not requeue a notification. Publication retries after a lost broker confirmation
can nevertheless duplicate it. MQTT's owned clean sessions likewise have no
offline retention; callback errors do not request retries or change native
acknowledgement. Protocol QoS does not guarantee exactly-once effects.

SQS deletes successfully handled messages, malformed envelopes, and deliveries
whose failures are all permanent. Other failures remain for redelivery after
visibility expires, subject to queue retention and redrive policies. Deletion
failure can repeat an already successful effect. The driver does not renew
visibility; size the timeout for the entire sequential batch. SNS acceptance
does not prove delivery to every subscribed queue.

For complete transport configuration and cancellation limits, use the
[driver comparison](drivers.md#compatibility-matrix). For every facade function,
see the [`broadcast` API](https://pkg.go.dev/arkoren.dev/core/service/broadcast).
