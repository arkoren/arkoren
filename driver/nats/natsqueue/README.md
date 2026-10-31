# Arkoren Job JetStream

`arkoren.dev/driver/nats/natsqueue` uses durable JetStream pull consumers for
at-least-once job delivery. Reject terminates the delivery with JetStream Term;
it is not redelivered. Fail can publish an application failure envelope before it
acknowledges; that subject is not a broker DLQ guarantee.

Install with `go get arkoren.dev/driver/nats` and import
`"arkoren.dev/driver/nats/natsqueue"`. The
[JetStream queue guide](../../../cmd/arkoren/skills/arkoren/references/guides/queues/drivers.md#nats)
shows the required stream and queue mapping, worker lifecycle, and local server.
`Start` validates existing resources by default; opt in to `Provision(true)`
when the application should create missing development resources.

Pass the required stream and queue map directly to `New` or
`NewFrom(connection, stream, queues, ...)`. For an existing `queues` map:

```go
driver, err := natsqueue.New("JOBS", queues, func(consumer *natsqueue.Builder) {
	consumer.AckWait(time.Minute)
})
```

Construction validates and copies that topology without connecting. Work subjects
must be distinct literal NATS subjects, and durable names must be distinct native
asset names. A configured failure subject must not feed a work consumer.
`URLs` defaults to `DefaultNATSURL` (`nats://127.0.0.1:4222`). Pass native TLS or
credential options to `Builder.Options`; native hooks run only when `Start`
creates an owned connection.

Callbacks run once in order before validation. Nil entries panic; expanded nil
slices make no changes. Queue maps, URL slices and native-option outer slices
are copied on input and publication. SDK internals and captures remain shared.
`NewFrom` borrows its connection; connection options do not reconfigure it.

By default, Batch is one, MaxAckPending follows Batch, AckWait is 30 seconds,
and MaxDeliver follows the server default. Nil Logger uses `slog.Default()`.
Start checks complete message delivery, matching acknowledgement settings, and
pull limits. Provisioning sets a ten-second pull expiry independently of AckWait;
existing consumers must allow at least ten seconds or leave that limit unset.
Existing incompatible resources are never silently changed.

Handlers must tolerate duplicate processing during connection or heartbeat
failures. Delivery metadata and payload getters return independent copies.
Failure envelopes retain application data and error text: restrict their broker
access. An empty failure destination logs the failure and acknowledges it.

Cancel and wait for consumers before stopping the driver. Owned Stop waits for
native draining subject to the shutdown context and native drain timeout. An
expired context still closes the owned connection and returns its context error.
Borrowed connections remain open for their caller to release.

See the [package API](https://pkg.go.dev/arkoren.dev/driver/nats/natsqueue) for
constructor, policy, delivery, and error examples.
