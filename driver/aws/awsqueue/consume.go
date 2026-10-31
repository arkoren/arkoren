package awsqueue

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"arkoren.dev/core/service/queue"
)

// Consume receives messages from queue until ctx is canceled or SQS returns an
// error. Handler errors are logged and left unsettled for SQS redelivery.
// It waits for handlers to return; cancellation does not abandon them. Renewal
// continues during handler drain, with each complete SDK call limited to the
// lesser of ten seconds and half the configured visibility. Failed renewals are
// logged and can permit redelivery; handlers must tolerate duplicate delivery.
//
// Once a handler returns, its heartbeat I/O is canceled before its worker is
// joined. Application settlement serializes behind any pending renewal, but
// waiting honors the settlement context. Native dependencies must honor request
// contexts for canceled I/O and joining to finish; see [NewFrom].
//
// Example:
//
//	driver, err := awsqueue.NewFrom(sqs.NewFromConfig(cfg), map[string]string{
//		"emails": "https://sqs.eu-west-1.amazonaws.com/123456789012/emails",
//	})
//	if err != nil {
//		return err
//	}
//	err = driver.Consume(ctx, "emails", func(ctx context.Context, received queue.Delivery) error {
//		settleContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
//		defer cancel()
//		return received.Acknowledge(settleContext)
//	})
//	if err != nil && !errors.Is(err, context.Canceled) {
//		return err
//	}
func (driver *Driver) Consume(ctx context.Context, queueName string, handler queue.DeliveryHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return driver.consume(ctx, queueName, handler)
}

// consume validates handler and queue before receiving messages.
func (driver *Driver) consume(ctx context.Context, queueName string, handler queue.DeliveryHandler) error {
	if handler == nil {
		return ErrInvalidHandler
	}

	queueURL, err := driver.queueURL(queueName)

	if err != nil {
		return err
	}

	return driver.receiveMessages(ctx, queueName, queueURL, handler)
}

// receiveMessages repeatedly receives and dispatches SQS messages.
func (driver *Driver) receiveMessages(
	ctx context.Context,
	consumer string,
	queueURL string,
	handler queue.DeliveryHandler,
) error {
	for {
		output, err := driver.receiveMessage(ctx, queueURL)

		if err != nil {
			return err
		}

		driver.handleMessages(ctx, consumer, queueURL, output.Messages, handler)
	}
}

// receiveMessage receives one SQS polling batch and wraps polling errors.
func (driver *Driver) receiveMessage(ctx context.Context, queueURL string) (*sqs.ReceiveMessageOutput, error) {
	output, err := driver.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queueURL),
		WaitTimeSeconds:     driver.waitTimeSeconds,
		MaxNumberOfMessages: driver.maxMessages,
		VisibilityTimeout:   visibilitySeconds(driver.visibility),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
		},
	})

	if err == nil {
		return output, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return nil, fmt.Errorf("receive job messages: %w", err)
}
