# Arkoren Job AMQP

`arkoren.dev/driver/rabbitmq/rabbitmqqueue` provides durable RabbitMQ work queues with manual
acknowledgement and publisher confirms. Delayed retries publish to durable TTL
queues before acknowledging the original delivery. Mandatory routing and confirms
protect that client-side handoff; they do not confirm later broker dead-letter
forwarding. Default RabbitMQ dead-lettering can lose retries if the work
destination is unavailable. Do not treat the default setup as an unconditional
at-least-once queue.

Install with `go get arkoren.dev/driver/rabbitmq` and import
`"arkoren.dev/driver/rabbitmq/rabbitmqqueue"`. The
[RabbitMQ queue guide](../../../cmd/arkoren/skills/arkoren/references/guides/queues/drivers.md#rabbitmq)
shows the required queue map, lifecycle hooks, failure handling, and local broker.

## Supply trusted routing

Pass the required queue map to `New(url, queues, configure...)` or
`NewFrom(connection, queues, configure...)`. Optional callbacks tune the exchange,
prefetch, retry limits, archive, and logger; there is no `Builder.Queues` setting.
For example, given your secret-loaded `url`:

```go
queues := map[string]rabbitmqqueue.Queue{
	"emails": {Name: "jobs.emails", RoutingKey: "emails"},
}
driver, err := rabbitmqqueue.New(url, queues, func(broker *rabbitmqqueue.Builder) {
	broker.Prefetch(16)
	broker.FailureQueue("jobs.failed")
})
```

Construction does no I/O. Start the driver before dispatching or consuming.
Callbacks run once in order before validation; nil entries panic and expanded nil
slices omit edits. Queue maps are copied before callbacks and at publication.
Physical queue names and routing keys must each be unique; a failure archive
cannot also be a work queue. Prefetch zero selects one; the valid configured range
is 1–65535. Retry delay zero selects one second, and that default must fit any
configured maximum. Retry bucketing can round requested delays upward.

An unroutable dispatch, retry, or failure archive returns
`ErrPublishNotConfirmed`. Failed transfers leave the source unsettled. A handler
or settlement error ends `Consume` and closes its channel so prefetch-one consumers
do not remain idle forever; the runner must handle the returned error. A lost
confirmation or failure after publishing but before acknowledging can duplicate
work, so handlers must be idempotent. Failure archives retain the original body
even for valid jobs, in addition to the decoded envelope and error text.
Publications on one driver serialize through confirmation because AMQP returns
have no delivery tag. Waiting callers retain their own cancellation. A canceled
confirmation is drained before another publication, preventing a late return
from being attributed to a different job.

## Establish broker durability

Stronger TTL retry forwarding requires quorum retry queues provisioned through
your broker's queue-type configuration, an at-least-once dead-letter strategy,
`reject-publish` overflow policy, available target queues and bindings, appropriate
permissions, and sufficient storage. The driver neither enables nor verifies
these prerequisites. Existing queue types require an application-owned migration;
this change does not rename queues, exchanges, headers, or envelopes.

Retry queues expire after an unused interval of twice their bucket delay plus an
hour. Queue expiry, deletion, or retention policies can discard pending work,
including messages awaiting protected forwarding. Review those policies before
relying on retries during a prolonged outage. See RabbitMQ's
[dead-letter safety requirements](https://www.rabbitmq.com/docs/dlx#safety).

## Release owned and borrowed connections

Owned setup and readiness RPC cancellation closes the socket and interrupts all
channels sharing it; reconstruct the driver afterward. Normal consumer
cancellation attempts a channel-only close, with five seconds for owned cleanup
before aborting an unresponsive socket. `Stop` releases an owned connection
without first waiting for a publisher-channel close or confirmation. Its graceful
close is bounded by the earlier of its context ending and five seconds; an
already-expired context still releases the socket and returns the context error.
Serialize `Start` and `Stop`, and cancel and join handlers before stopping.

`NewFrom` borrows the connection and rejects enabled SDK automatic recovery
without closing or changing it. Its native accessor returns the supplied pointer
even before `Start`. Borrowed SDK channel-open, topology, write, and channel-close
operations have no in-flight context cancellation: the caller must ensure a
responsive peer or manage socket deadlines/connection close. Driver cancellation
does not override borrowed ownership. Nil loggers select `slog.Default()`.
