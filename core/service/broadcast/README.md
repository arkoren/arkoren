# Broadcast

Use `arkoren.dev/core/service/broadcast` to publish typed facts to independent
consumers. Install with `go get arkoren.dev/core`. Choose a transport by its
[retention and replica-sharing behavior](../../../cmd/arkoren/skills/arkoren/references/guides/broadcasting/drivers.md),
then wire its lifecycle as described in the
[broadcast guide](../../../cmd/arkoren/skills/arkoren/references/guides/broadcasting/overview.md).

For an existing publisher, declare a shared topic and publish its typed value:

```go
topic := broadcast.NewTopic[int]("orders.created")
err := publisher.Publish(ctx, topic, 42)
```

Register your `observeOrder(context.Context, int) error` callback on an existing
consumer during composition. Keep the returned registration owned by the app:

```go
unsubscribe, err := consumer.Listen(topic, observeOrder)
compose.Require(err)
compose.Defer(unsubscribe)
compose.Run(consumer.Run)
```

Publication success is acceptance, not confirmation that a listener ran.
Listener failures never reach the publisher. A redelivering transport can invoke
all matching listeners again, so application effects must be idempotent.
Unsubscribe affects future snapshots; an already-admitted callback can finish.
Raw pattern listeners must not mutate shared payload or metadata containers.

The memory broker closes delivery admission before draining. Cancellation can
interrupt a wait for its shared concurrency slot; admitted handlers instead keep
detached contexts and can finish after Consume returns. Stop closes admission
even with an expired context, waits for admitted handlers only until its own
context ends, and does not forcibly cancel them or join Consume calls. A later
Stop can wait again. Memory fan-out is not durable or atomic across subscribers.

See the [`broadcast` API](https://pkg.go.dev/arkoren.dev/core/service/broadcast)
for routing patterns, construction policies, and event payloads.
