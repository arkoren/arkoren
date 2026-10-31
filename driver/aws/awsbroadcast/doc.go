// Package awsbroadcast provides Amazon SNS and SQS implementations of the
// broadcast contracts.
//
// [SNSDriver] publishes broadcasts to SNS topics, which fan out to
// subscribers such as SQS queues. [SQSDriver] publishes to and consumes from
// SQS queues; pair it with SNS for one queue per consuming service.
// Both publish full JSON broadcast envelopes, not raw application payloads.
// FIFO destinations group by name and use an explicit message ID, or a fresh
// native ID when omitted, for deduplication. Broadcast consumption does not renew
// visibility: queue policy and idempotent listeners must accommodate a full batch.
//
// This package holds two driver types because both are the AWS broadcast
// implementation; it is the documented exception to the one-Driver rule.
//
// SNS has required routing only, with no construction builder. SQS constructors
// accept ordered SQSBuilder callbacks invoked directly once on one fresh draft.
// Nil entries panic; nil/empty slice expansion supplies no edits. Queues copies
// its input and publication detaches routing again. Native clients stay shared.
//
// Example:
//
//	driver, err := awsbroadcast.NewSQS(ctx, func(broker *awsbroadcast.SQSBuilder) {
//		broker.QueueURL("https://sqs.eu-west-1.amazonaws.com/123456789012/events")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
package awsbroadcast
