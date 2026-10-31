// Package delivery owns one Cloudflare lease and its confirmed settlement.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/queues"

	"arkoren.dev/core/service/queue"
)

// maxRetryDelay is the longest delay Cloudflare accepts for a retry.
const maxRetryDelay = 24 * time.Hour

// Delivery is one leased Cloudflare message and the state to settle it once.
type Delivery struct {
	// client performs settlement through the shared native transport.
	client *cloudflare.Client
	// accountID identifies the owning native account.
	accountID string
	// queueID identifies the physical queue that leased this message.
	queueID string
	// leaseID is required on every native settlement request.
	leaseID string
	// message owns the handler's immutable envelope.
	message queue.Message
	// logger receives native warnings and application-supplied failure errors.
	logger *slog.Logger
	// repeated retains the public driver's error identity.
	repeated error
	// mutex covers the complete native settlement; contenders fail fast.
	mutex sync.Mutex
	// settled is published only after a successful matching confirmation.
	settled bool
}

// New captures the completed envelope and borrows native request dependencies.
// Malformed-envelope failure handling can retain partially decoded fields.
func New(client *cloudflare.Client, accountID, queueID, leaseID string, message queue.Message, logger *slog.Logger, repeated error) *Delivery {
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)

	return &Delivery{client: client, accountID: accountID, queueID: queueID, leaseID: leaseID,
		message: message, logger: logger, repeated: repeated}
}

// Message returns the job envelope with payload bytes and metadata copied.
func (delivery *Delivery) Message() queue.Message {
	message := delivery.message
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)

	return message
}

// Acknowledge deletes the lease only after one matching acknowledgment.
func (delivery *Delivery) Acknowledge(ctx context.Context) error {
	return delivery.settle(ctx, queues.MessageAckParams{
		Acks: cloudflare.F([]queues.MessageAckParamsAck{
			{
				LeaseID: cloudflare.F(delivery.leaseID),
			},
		}),
	})
}

// Retry rounds positive delays up to seconds and requires one confirmed retry.
// Negative delay means zero; cancellation precedes the native maximum check.
func (delivery *Delivery) Retry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if delay > maxRetryDelay {
		return fmt.Errorf("retry job message: delay exceeds %s", maxRetryDelay)
	}

	delay = max(delay, 0)
	seconds := float64((delay + time.Second - 1) / time.Second)

	return delivery.settle(ctx, queues.MessageAckParams{
		Retries: cloudflare.F([]queues.MessageAckParamsRetry{{
			LeaseID:      cloudflare.F(delivery.leaseID),
			DelaySeconds: cloudflare.F(seconds),
		}}),
	})
}

// Reject locally settles without changing the native lease or redrive policy.
func (delivery *Delivery) Reject(ctx context.Context) error {
	return delivery.settle(ctx, queues.MessageAckParams{})
}

// Fail logs the supplied error unchanged and acknowledges without an archive.
func (delivery *Delivery) Fail(ctx context.Context, err error) error {
	delivery.logger.ErrorContext(ctx, "Cloudflare job delivery failed",
		"error", err, "queue", delivery.message.Queue, "job_id", delivery.message.ID)

	return delivery.Acknowledge(ctx)
}

// settle admits only one operation at a time. Failure leaves settlement open;
// an empty request settles locally without contacting the provider.
func (delivery *Delivery) settle(ctx context.Context, params queues.MessageAckParams) error {
	if !delivery.mutex.TryLock() {
		return delivery.repeated
	}

	defer delivery.mutex.Unlock()

	if delivery.settled {
		return delivery.repeated
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if params.Acks.Present || params.Retries.Present {
		if err := delivery.sendSettlement(ctx, params); err != nil {
			return err
		}
	}

	delivery.settled = true

	return nil
}

// sendSettlement requires the count matching the requested operation. Warnings
// accompany success only when the native response confirms exactly one lease.
func (delivery *Delivery) sendSettlement(ctx context.Context, params queues.MessageAckParams) error {
	params.AccountID = cloudflare.F(delivery.accountID)

	result, err := delivery.client.Queues.Messages.Ack(ctx, delivery.queueID, params)

	if err != nil {
		return fmt.Errorf("settle job message: %w", err)
	}

	count := result.RetryCount

	if params.Acks.Present {
		count = result.AckCount
	}

	if count != 1 {
		return errors.New("settle job message: provider did not confirm the lease")
	}

	if warning := result.Warnings[delivery.leaseID]; warning != "" {
		delivery.logger.WarnContext(ctx, "Cloudflare job settlement warning",
			"warning", warning, "queue", delivery.message.Queue, "job_id", delivery.message.ID)
	}

	return nil
}
