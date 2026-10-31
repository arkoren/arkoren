package search

import (
	"context"
	"errors"
	"time"

	"arkoren.dev/core/event"
)

// MutationEvent observes a Sync or Drop driver boundary. It contains no index
// names, document IDs, documents, queries, or native error text. A successful
// operation may be a no-op; a failure does not establish that nothing changed.
//
// Example:
//
//	_, err := events.Listen(search.Synced, func(ctx context.Context, observed search.MutationEvent) error {
//		log.Print(observed.Operation, observed.Duration)
//		return nil
//	})
//	if err != nil { return err }
type MutationEvent struct {
	// Operation is sync or drop, never an index or application-supplied name.
	// For example: log.Print(observed.Operation).
	Operation string

	// Duration measures the driver call, excluding event listener execution.
	// For example: log.Print(observed.Duration).
	Duration time.Duration

	// ErrorClass is empty on success, a search sentinel name, canceled,
	// deadline_exceeded, or error. For example: log.Print(observed.ErrorClass).
	ErrorClass string
}

// Synced reports that the driver Sync call succeeded. It may have changed no
// schemas; it does not establish an atomic commit across indexes. Notifications
// are best effort, in process, not an index consistency or durable audit system.
//
// Example:
//
//	_, err := events.Listen(search.Synced, func(ctx context.Context, observed search.MutationEvent) error {
//		return nil
//	})
//	if err != nil { return err }
var Synced = event.NewTopic[MutationEvent]("search.index.synced")

// Dropped reports that a driver Drop call succeeded, including an absent index.
// It reports observed completion, not proof that documents existed beforehand.
//
// Example:
//
//	_, err := events.Listen(search.Dropped, func(ctx context.Context, observed search.MutationEvent) error {
//		return nil
//	})
//	if err != nil { return err }
var Dropped = event.NewTopic[MutationEvent]("search.index.dropped")

// Failed reports an error from an administrative Sync or Drop driver call.
// Changes may already have occurred, especially during multi-index Sync; no
// rollback or atomicity is implied. Invalid registrations emit no events.
//
// Example:
//
//	_, err := events.Listen(search.Failed, func(ctx context.Context, observed search.MutationEvent) error {
//		log.Print(observed.ErrorClass)
//		return nil
//	})
//	if err != nil { return err }
var Failed = event.NewTopic[MutationEvent]("search.index.failed")

// emit publishes administrative outcomes only after a driver boundary. A nil
// bus is inert; listener errors and panics cannot replace the operation result.
func (engine *Engine) emit(ctx context.Context, operation string, duration time.Duration, err error) {
	if engine.config.Events == nil {
		return
	}

	observed := MutationEvent{Operation: operation, Duration: duration}

	if err != nil {
		observed.ErrorClass = mutationErrorClass(err)

		engine.config.Events.Emit(ctx, Failed, observed)

		return
	}

	topic := Synced

	if operation == operationDrop {
		topic = Dropped
	}

	engine.config.Events.Emit(ctx, topic, observed)
}

// mutationErrorClass restricts failure data to contract and context classes.
// Unknown errors use error, never native type names or error messages.
func mutationErrorClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}

	for _, sentinel := range errorSentinels {
		if errors.Is(err, sentinel.Err) {
			return sentinel.Name
		}
	}

	return "error"
}
