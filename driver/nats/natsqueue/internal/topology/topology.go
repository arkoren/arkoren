// Package topology owns JetStream work-stream and durable pull-consumer policy.
package topology

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// Config carries completed topology policy without public queue definitions.
type Config struct {
	// Stream is the unchanged native stream name.
	Stream string
	// Queues maps application routes to native consumer declarations.
	Queues map[string]nats.ConsumerConfig
	// Provision permits creation of missing resources.
	Provision bool
	// Missing preserves the adapter's provisioning error identity.
	Missing error
}

// Consumer builds the native pull declaration with exact request limits.
func Consumer(durable, subject string, wait time.Duration, deliveries, pending, batch int, expiry time.Duration) nats.ConsumerConfig {
	return nats.ConsumerConfig{
		Durable: durable, DeliverPolicy: nats.DeliverAllPolicy, AckPolicy: nats.AckExplicitPolicy,
		AckWait: wait, MaxDeliver: deliveries, FilterSubject: subject,
		MaxAckPending: pending, MaxRequestBatch: batch, MaxRequestExpires: expiry,
	}
}

// Ensure validates the stream before inspecting each durable consumer.
func Ensure(ctx context.Context, jetStream nats.JetStreamContext, config Config) error {
	if err := ensureStream(ctx, jetStream, config); err != nil {
		return err
	}

	for _, expected := range config.Queues {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := ensureConsumer(ctx, jetStream, config, expected); err != nil {
			return err
		}
	}

	return nil
}

// ensureStream provisions only native not-found, never another lookup failure.
func ensureStream(ctx context.Context, jetStream nats.JetStreamContext, config Config) error {
	stream, err := jetStream.StreamInfo(config.Stream, nats.Context(ctx))

	if err == nil {
		return ValidateStream(config, stream.Config)
	}

	if !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("inspect JetStream stream: %w", err)
	}

	if !config.Provision {
		return fmt.Errorf("%w: stream %q: %w", config.Missing, config.Stream, err)
	}

	subjects := make([]string, 0, len(config.Queues))

	for _, expected := range config.Queues {
		subjects = append(subjects, expected.FilterSubject)
	}

	_, err = jetStream.AddStream(&nats.StreamConfig{
		Name: config.Stream, Subjects: subjects, Retention: nats.WorkQueuePolicy,
	}, nats.Context(ctx))

	if err != nil {
		return fmt.Errorf("provision JetStream stream %q: %w", config.Stream, err)
	}

	return nil
}

// ValidateStream requires literal membership without normalizing native subjects.
func ValidateStream(config Config, stream nats.StreamConfig) error {
	subjects := make(map[string]struct{}, len(stream.Subjects))

	for _, subject := range stream.Subjects {
		subjects[subject] = struct{}{}
	}

	for _, expected := range config.Queues {
		if _, ok := subjects[expected.FilterSubject]; !ok {
			return fmt.Errorf("%w: stream %q does not include subject %q", config.Missing, config.Stream, expected.FilterSubject)
		}
	}

	return nil
}

// ensureConsumer validates an existing durable or provisions native not-found.
func ensureConsumer(ctx context.Context, jetStream nats.JetStreamContext, config Config, expected nats.ConsumerConfig) error {
	consumer, err := jetStream.ConsumerInfo(config.Stream, expected.Durable, nats.Context(ctx))

	if err == nil {
		return ValidateConsumer(expected, consumer.Config, config.Missing)
	}

	if !errors.Is(err, nats.ErrConsumerNotFound) {
		return fmt.Errorf("inspect JetStream consumer: %w", err)
	}

	if !config.Provision {
		return fmt.Errorf("%w: consumer %q: %w", config.Missing, expected.Durable, err)
	}

	_, err = jetStream.AddConsumer(config.Stream, &expected, nats.Context(ctx))

	if err != nil {
		return fmt.Errorf("provision JetStream consumer %q: %w", expected.Durable, err)
	}

	return nil
}

// ValidateConsumer preserves validation order, optional limits, and native names.
func ValidateConsumer(expected, consumer nats.ConsumerConfig, missing error) error {
	if consumer.HeadersOnly {
		return fmt.Errorf("%w: consumer %q must deliver message bodies", missing, expected.Durable)
	}

	if consumer.Durable != expected.Durable || consumer.DeliverSubject != "" ||
		consumer.AckPolicy != nats.AckExplicitPolicy || consumer.FilterSubject != expected.FilterSubject {
		return fmt.Errorf("%w: consumer %q does not match the required durable pull configuration", missing, expected.Durable)
	}

	if consumer.AckWait != expected.AckWait || consumer.MaxAckPending != expected.MaxAckPending ||
		(consumer.MaxRequestExpires > 0 && consumer.MaxRequestExpires < expected.MaxRequestExpires) ||
		(consumer.MaxRequestBatch != 0 && consumer.MaxRequestBatch < expected.MaxRequestBatch) {
		return fmt.Errorf("%w: consumer %q does not match the required acknowledgement or batch configuration", missing, expected.Durable)
	}

	if expected.MaxDeliver != 0 && consumer.MaxDeliver != expected.MaxDeliver {
		return fmt.Errorf("%w: consumer %q does not match the required maximum deliveries", missing, expected.Durable)
	}

	return nil
}
