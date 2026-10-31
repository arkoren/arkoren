// Package queue provides transport-neutral asynchronous queue dispatch and execution.
package queue

import (
	"context"
	"time"

	"arkoren.dev/core/service/queue/internal/execution"
)

// notifications translates confirmed pipeline outcomes into public events.
func (consumer *Consumer) notifications(delivery Delivery, original Message, timeout time.Duration) execution.Notifications {
	var retry Message

	return execution.Notifications{
		RetryAttempts: func() int {
			retry = delivery.Message()

			return retry.Attempts
		},
		Failed: func(ctx context.Context, err error) {
			message := delivery.Message()

			consumer.config.Events.Emit(ctx, JobFailed, JobFailedEvent{ID: message.ID, Name: message.Name, Queue: message.Queue, Attempts: message.Attempts, Err: err})
		},
		Rejected: func(ctx context.Context, err error) {
			message := delivery.Message()

			consumer.config.Events.Emit(ctx, JobRejected, JobRejectedEvent{ID: message.ID, Name: message.Name, Queue: message.Queue, Attempts: message.Attempts, Err: err})
		},
		Retried: func(ctx context.Context, delay time.Duration, err error) {
			message := retry

			consumer.config.Events.Emit(ctx, JobRetried, JobRetriedEvent{ID: message.ID, Name: message.Name, Queue: message.Queue, Attempts: message.Attempts, Delay: delay, Err: err})
		},
		TimedOut: func(ctx context.Context) {
			consumer.config.Events.Emit(ctx, JobTimedOut, JobTimedOutEvent{ID: original.ID, Name: original.Name, Queue: original.Queue, Attempts: original.Attempts, Timeout: timeout})
		},
	}
}
