// Package topology owns RabbitMQ job declarations and deployed retry identities.
package topology

import (
	"fmt"
	"math"
	"strconv"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// Exchange declares the durable direct job exchange.
func Exchange(channel *amqp091.Channel, exchange string) error {
	if err := channel.ExchangeDeclare(exchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare AMQP job exchange: %w", err)
	}

	return nil
}

// Queue declares and binds one durable queue without changing its native flags.
func Queue(channel *amqp091.Channel, exchange, name, routingKey string) error {
	if _, err := channel.QueueDeclare(name, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare AMQP job queue: %w", err)
	}

	if err := channel.QueueBind(name, routingKey, exchange, false, nil); err != nil {
		return fmt.Errorf("bind AMQP job queue: %w", err)
	}

	return nil
}

// Failure declares the optional durable failure queue.
func Failure(channel *amqp091.Channel, failureQueue string) error {
	if failureQueue == "" {
		return nil
	}

	if _, err := channel.QueueDeclare(failureQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare AMQP job failure queue: %w", err)
	}

	return nil
}

// Bucket rounds upward to bounded retry-queue buckets, capped at the AMQP TTL.
func Bucket(delay time.Duration) time.Duration {
	step := time.Hour

	switch {
	case delay <= time.Minute:
		step = time.Second
	case delay <= time.Hour:
		step = 15 * time.Second
	case delay <= 24*time.Hour:
		step = 5 * time.Minute
	}

	rounded := (delay + step - 1) / step * step

	return min(rounded, math.MaxUint32*time.Millisecond)
}

// Name preserves the deployed durable queue identity for a rounded delay.
func Name(consumer string, delay time.Duration) string {
	return consumer + ".retry." + strconv.FormatInt(Milliseconds(delay), 10)
}

// Arguments routes expired retries back to work and expires unused queues.
func Arguments(exchange, routingKey string, delay time.Duration) amqp091.Table {
	return amqp091.Table{
		"x-dead-letter-exchange":    exchange,
		"x-dead-letter-routing-key": routingKey,
		"x-expires":                 Milliseconds(2*delay + time.Hour),
	}
}

// Milliseconds rounds delay up to the original AMQP-compatible TTL.
func Milliseconds(delay time.Duration) int64 {
	return int64(math.Ceil(float64(delay) / float64(time.Millisecond)))
}
