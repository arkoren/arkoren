package queue

import (
	"errors"
)

// ErrPermanent matches, under [errors.Is], errors created by [Permanent].
// It classifies a handler outcome, not a confirmed broker settlement.
//
//	err := queue.Permanent(errors.New("unsupported template"))
//	fmt.Println(errors.Is(err, queue.ErrPermanent)) // true
var ErrPermanent = errors.New("permanent job failure")

// ErrUnknownName reports a job name the consumer has no handler for.
// The consumer retries such deliveries later, for example while a rolling
// deploy registers the handler on other workers.
//
//	if errors.Is(err, queue.ErrUnknownName) {
//		fmt.Println("check worker registration and rollout")
//	}
var ErrUnknownName = errors.New("unknown job name")

// ErrDuplicateName reports a second handler for one job name.
// The original registration remains unchanged.
//
//	if err := consumer.Handle(job, send); errors.Is(err, queue.ErrDuplicateName) {
//		return err
//	}
var ErrDuplicateName = errors.New("duplicate job name")

// ErrInvalidName reports an invalid job. [Consumer.Handle] and
// [Dispatcher.Dispatch] return it, wrapping [Job.Err], for a job whose name or
// settings are invalid.
//
//	err := dispatcher.Dispatch(ctx, queue.NewJob[string]("Bad Name"), "hello")
//	if errors.Is(err, queue.ErrInvalidName) {
//		return err
//	}
var ErrInvalidName = errors.New("invalid job name")

// ErrNoQueues reports that [Consumer.Run] has no queue to consume: no handler
// is registered, or [ConsumerBuilder.Queues] excludes every handled queue.
//
//	consumer := queue.NewConsumer(driver)
//	err := consumer.Run(ctx)
//	fmt.Println(errors.Is(err, queue.ErrNoQueues)) // true
var ErrNoQueues = errors.New("no queues to consume")
