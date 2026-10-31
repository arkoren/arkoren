package awsbroadcast

import (
	"log/slog"
	"maps"
)

// SQSBuilder configures SQS routing and polling on a fresh construction draft.
// Callbacks share the draft in order; published drivers never retain it. SNS
// has no optional settings and intentionally has no builder.
//
// Example:
//
//	configure := func(broker *SQSBuilder) {
//		broker.QueueURL("https://sqs.eu-west-1.amazonaws.com/123456789012/events")
//	}
//	driver, err := NewSQS(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type SQSBuilder struct {
	// config owns routing settings until publication.
	config sqsSettings
}

// SQSBuilderFunc edits the shared construction draft once in argument order.
// Nil entries panic; native clients and loggers remain shared after publication.
//
// Example:
//
//	var configure awsbroadcast.SQSBuilderFunc = func(broker *awsbroadcast.SQSBuilder) {
//		broker.QueueURL("https://sqs.eu-west-1.amazonaws.com/123456789012/events")
//	}
//	driver, err := awsbroadcast.NewSQS(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type SQSBuilderFunc = func(*SQSBuilder)

// Queues replaces and copies the name-to-URL map. Empty removes publisher
// routes; a consumer QueueURL can still be supplied. It returns the same draft.
//
// Example:
//
//	configure := func(broker *SQSBuilder) {
//		broker.Queues(map[string]string{"users.created": "queue-url"})
//	}
//	driver, err := NewSQS(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *SQSBuilder) Queues(queues map[string]string) *SQSBuilder {
	broker.config.Queues = maps.Clone(queues)

	return broker
}

// QueueURL replaces the consumer queue URL. Empty removes the consumer binding;
// publisher routes can still be supplied. It returns the same draft.
//
// Example:
//
//	configure := func(broker *SQSBuilder) {
//		broker.QueueURL("queue-url")
//	}
//	driver, err := NewSQS(ctx, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *SQSBuilder) QueueURL(url string) *SQSBuilder {
	broker.config.QueueURL = url

	return broker
}

// WaitTimeSeconds replaces long polling in seconds. Zero selects 20; validation
// rejects values outside 1–20. It returns the same construction draft.
//
// Example:
//
//	configure := func(broker *SQSBuilder) {
//		broker.WaitTimeSeconds(1)
//	}
//	driver, err := NewSQS(ctx, func(broker *SQSBuilder) {
//		broker.QueueURL("queue-url")
//	}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *SQSBuilder) WaitTimeSeconds(seconds int32) *SQSBuilder {
	broker.config.WaitTimeSeconds = seconds

	return broker
}

// MaxMessages replaces the pull batch size. Zero selects one; validation rejects
// values outside 1–10. It returns the same draft without polling SQS.
//
// Example:
//
//	configure := func(broker *SQSBuilder) {
//		broker.MaxMessages(5)
//	}
//	driver, err := NewSQS(ctx, func(broker *SQSBuilder) {
//		broker.QueueURL("queue-url")
//	}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *SQSBuilder) MaxMessages(count int32) *SQSBuilder {
	broker.config.MaxMessages = count

	return broker
}

// Logger replaces the shared delivery logger. Nil selects slog.Default at
// construction. It returns the same draft and does not mutate the logger.
//
// Example:
//
//	configure := func(broker *SQSBuilder) {
//		broker.Logger(slog.New(slog.DiscardHandler))
//	}
//	driver, err := NewSQS(ctx, func(broker *SQSBuilder) {
//		broker.QueueURL("queue-url")
//	}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *SQSBuilder) Logger(logger *slog.Logger) *SQSBuilder {
	broker.config.Logger = logger

	return broker
}

// configuredSQS applies policies directly once in order and returns completed
// settings for newSQSFrom to validate and detach at driver publication. Nil
// entries panic; empty or nil slice expansion supplies no policies.
func configuredSQS(configure ...SQSBuilderFunc) sqsSettings {
	broker := &SQSBuilder{}

	for _, configure := range configure {
		configure(broker)
	}

	return broker.config
}
