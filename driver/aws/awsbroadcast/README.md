# AWS Broadcast

`SNSDriver` in `arkoren.dev/driver/aws/awsbroadcast` publishes complete `broadcast.Message` envelopes
to the configured SNS topic selected by `Message.Name`. SNS subscriptions can
fan out durably to endpoints such as SQS.

Install with `go get arkoren.dev/driver/aws` and import
`"arkoren.dev/driver/aws/awsbroadcast"`. Provision topics, subscriptions, and
queue permissions before publishing; constructors do not create them. See the
[SNS broadcast guide](../../../cmd/arkoren/skills/arkoren/references/guides/broadcasting/drivers.md#sns)
for facade wiring and a consumer-owned local emulator setup.

The `topics` map is caller-owned and copied by `NewSNS` or `NewSNSFrom`.
Every routing key must be a valid broadcast name. SNS sends the complete JSON
envelope without a Subject, not a raw application payload; payload bytes are
base64 inside that envelope. FIFO SNS topics group by broadcast name and use
the message ID for deduplication, or a fresh ID when the envelope omits one.
For custom AWS client settings, build an `sns.Client` and pass it to
`NewSNSFrom`.

## SQS delivery

`SQSDriver` in `arkoren.dev/driver/aws/awsbroadcast` publishes complete broadcast envelopes to
configured queues and consumes direct SQS messages or standard SNS wrappers.
Direct SQS delivery is durable point-to-point delivery. For durable fan-out,
publish with `SNSDriver` and subscribe each SQS queue to the SNS topic. Failed
handlers leave messages for SQS redelivery after the visibility timeout;
successful handlers and permanent failures delete their delivery. This driver
does not extend visibility while listeners run: configure the queue's visibility
timeout to cover the whole received batch, and make listeners idempotent.
Direct FIFO publishing groups by broadcast name and deduplicates by message ID,
or a fresh native ID when omitted. Supplying the same ID on retries is the
caller's responsibility; a send without an ID does not deduplicate later calls.

`NewSQS` and `NewSQSFrom` accept trailing `func(*awsbroadcast.SQSBuilder)`
callbacks. Use `Queues` for publisher routing and `QueueURL` for the consumer:

```go
driver, err := awsbroadcast.NewSQS(ctx, func(broker *awsbroadcast.SQSBuilder) {
	broker.QueueURL("https://sqs.eu-west-1.amazonaws.com/123456789012/events")
	broker.WaitTimeSeconds(20)
})
```

Callbacks run directly once, synchronously in order on one fresh draft. Nil
entries panic; expanded nil/empty slices omit edits. Settings return the same
draft; `Queues` copies its input and publication copies it again. Retained
drafts cannot change a driver. All SQS settings apply to `NewSQSFrom`; native
clients remain caller-owned. SNS has no optional settings and no builder.
