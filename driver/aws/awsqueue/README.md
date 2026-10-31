# Arkoren Queue SQS

`arkoren.dev/driver/aws/awsqueue` provides an Amazon SQS queue driver. It publishes
and consumes complete `queue.Message` envelopes. Consumers accept both direct
SQS envelopes and standard SNS notification wrappers, so one publisher can
fan out to multiple SQS worker queues through SNS.

Install with `go get arkoren.dev/driver/aws` and import
`"arkoren.dev/driver/aws/awsqueue"`. Provision the SQS queues first. The
[SQS queue guide](../../../cmd/arkoren/skills/arkoren/references/guides/queues/drivers.md#amazon-sqs)
covers AWS permissions, facade wiring, visibility limits, and local emulation.

The configured logical queue is authoritative on delivery. SQS supplies the
message ID and receive count; untrusted envelope values do not select a worker
queue or override broker metadata. Retry rounds positive delays up to whole
seconds and rejects negative delays or delays above 12 hours without settlement.
SQS also enforces the time remaining since receipt, so even a 12-hour request
can fail. The driver returns that native error; it never shortens the requested
delay to claim success. Zero requests immediate visibility.

Fail optionally sends a failure envelope before
deleting the original message; this favors redelivery when failure publication
does not succeed. Ordinary dispatch and failure queues support FIFO destinations:
messages group by logical queue and deduplicate by message ID. Malformed failure
reports without an envelope use the `failed` group and a fresh deduplication ID.
Failure archives contain job payloads and error text; secure them like the source
queue rather than treating them as ordinary diagnostic logs.

Pass optional settings as trailing `func(*awsqueue.Builder)` callbacks:

```go
driver, err := awsqueue.New(ctx, map[string]string{
	"emails": "https://sqs.eu-west-1.amazonaws.com/123456789012/emails",
}, func(consumer *awsqueue.Builder) {
	consumer.MaxMessages(5)
})
```

Callbacks run directly once, synchronously in order on one fresh draft. Nil
entries panic; expanded nil/empty slices omit edits. Settings return that draft;
publication copies the completed settings and required queue map. Retaining a
draft or changing the original map cannot change a driver. All settings also
apply to `NewFrom(client, queues, configure...)`; the client is not copied or closed.

`VisibilityTimeout(0)` selects 30 seconds. Other values must be from one second
through 12 hours inclusive; native requests round fractions upward, while the
heartbeat runs at half the configured duration. Invalid settings fail before
AWS configuration loading. Nil Logger selects `slog.Default()`.

Each renewal has a complete SDK budget of at most ten seconds and at most half
the configured lease. Renewal continues while a canceled consumer drains a
handler. When the handler returns, its pending renewal is canceled and joined.
Settlement waits behind pending renewal using its own context budget. This is
cooperative cancellation: native transports, credentials, middleware, and handlers
must honor their contexts. Renewal failure or SQS's remaining-visibility limit
can still permit duplicate delivery; handlers must be idempotent.
