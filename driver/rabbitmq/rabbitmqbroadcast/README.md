# AMQP Event Bus

`arkoren.dev/driver/rabbitmq/rabbitmqbroadcast` implements `broadcast.PublisherDriver`
and `broadcast.ConsumerDriver`
with a RabbitMQ topic exchange for messages between processes.

Install with `go get arkoren.dev/driver/rabbitmq` and import
`"arkoren.dev/driver/rabbitmq/rabbitmqbroadcast"`. The
[RabbitMQ broadcast guide](../../../cmd/arkoren/skills/arkoren/references/guides/broadcasting/drivers.md#rabbitmq)
shows lifecycle hooks, facade wiring, and a standalone local broker. Subscriber
queues are ephemeral; use `rabbitmqqueue` when work must survive disconnection.

Configure Exchange and Logger with `func(broker *rabbitmqbroadcast.Builder)`
callbacks after the required URL or borrowed connection. Callbacks run once in
order before validation; nil entries panic and expanded nil slices omit edits.
`NewFrom` retains caller ownership of the connection.
An empty exchange selects `arkoren:events`; a nil logger selects `slog.Default()`.

`Consume` drops malformed envelopes, invalid concrete names (including wildcards),
and envelopes whose name does not exactly equal the actual AMQP delivery routing
key, before core listeners or telemetry run. Raw `Subscribe` callbacks still
receive bytes without envelope validation. Rejected messages remain auto-acked.
Those bytes are the complete JSON `broadcast.Message` envelope, not just its
application `Payload`. A publish with no active subscribers is successful;
mandatory work-queue routing requirements do not apply to ephemeral fan-out.

Routing is not authorization or payload authentication. Use broker permissions
and separate exchanges or virtual hosts for mutually untrusted publishers. On a
shared topic exchange, configure topic permissions to restrict publication keys;
the name check preserves those restrictions only when the broker enforces them.

Owned TCP, TLS, AMQP setup, and readiness cancellation closes the socket and
interrupts every channel sharing it; reconstruct the driver afterward. Normal
consumer cancellation attempts a channel-only close, allowing five seconds before
aborting an unresponsive owned socket. `Stop` does not wait for confirmations or
handlers before releasing its owned connection. Graceful close permits the earlier
of five seconds and its context ending; an expired context still releases the
socket and returns the context error. Serialize `Start` and `Stop`.

Borrowed connections remain open after `Stop`, and `Connection()` exposes the
supplied pointer even before `Start`. The SDK's channel-open, topology, write, and
channel-close operations cannot be interrupted in flight without closing that
connection. Ensure a responsive peer or caller-managed socket deadlines/close;
the driver never closes a borrowed connection to enforce a context deadline.
