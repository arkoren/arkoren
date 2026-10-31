package secret

import (
	"context"

	"arkoren.dev/core/event"
)

// MutationEvent reports one observed facade mutation outcome without secret
// identity or payload. It is not proof of global rotation, rollout, durability,
// or an authoritative audit; raw driver and external writes are not observed.
//
// Example:
//
//	unsubscribe, err := bus.Listen(secret.MutationFailed, func(ctx context.Context, outcome secret.MutationEvent) error {
//		logger.WarnContext(ctx, "secret mutation failed", "operation", outcome.Operation, "error_type", outcome.ErrorType)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type MutationEvent struct {
	// Operation is "put", "update", or "delete", never a secret name.
	// These bounded observations describe facade calls, not external rotation.
	//
	// Example:
	//
	//	logger.InfoContext(ctx, "secret mutation observed", "operation", outcome.Operation)
	Operation string

	// ErrorType is empty on success; otherwise it is "secret.ErrNotFound",
	// "context.Canceled", "context.DeadlineExceeded", or "other". No provider
	// error text or secret identity is retained in this classification.
	//
	// Example:
	//
	//	if outcome.ErrorType != "" {
	//		logger.WarnContext(ctx, "secret mutation failed", "error_type", outcome.ErrorType)
	//	}
	ErrorType string
}

// MutationSucceeded reports a nil driver result after Put, Update, or Delete.
// It is emitted only through a store configured with Builder.Events. It does
// not prove that credential consumers have adopted a new value.
//
// Example:
//
//	observe := func(ctx context.Context, outcome secret.MutationEvent) error {
//		logger.InfoContext(ctx, "secret mutation observed", "operation", outcome.Operation)
//		return nil
//	}
//	unsubscribe, err := bus.Listen(secret.MutationSucceeded, observe)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var MutationSucceeded = event.NewTopic[MutationEvent]("secret.mutation.succeeded")

// MutationFailed reports a non-nil driver result, including a missing secret.
// Failure does not prove the provider made no partial change. Its payload
// carries a bounded ErrorType, not the potentially sensitive returned error.
//
// Example:
//
//	unsubscribe, err := bus.Listen(secret.MutationFailed, func(ctx context.Context, outcome secret.MutationEvent) error {
//		logger.WarnContext(ctx, "secret mutation failed", "error_type", outcome.ErrorType)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var MutationFailed = event.NewTopic[MutationEvent]("secret.mutation.failed")

// mutated publishes one best-effort notification after the provider returns.
// With no bus it avoids error classification and payload construction.
func (store *Store) mutated(ctx context.Context, operation string, err error) {
	if store.config.events == nil {
		return
	}

	topic := MutationSucceeded

	if err != nil {
		topic = MutationFailed
	}

	store.config.events.Emit(ctx, topic, MutationEvent{Operation: operation, ErrorType: failureClass(err)})
}
