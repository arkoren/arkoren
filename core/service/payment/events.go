package payment

import (
	"context"
	"errors"

	"arkoren.dev/core/event"
)

// OperationEvent reports the observed result of one validated facade call that
// reached the driver. It contains only bounded operation and error classes, not
// resource identities, amounts, requests, handoff secrets, or native error text.
// Completion includes reads and idempotent replays; neither topic proves money
// movement, settlement, or fulfillment eligibility. These synchronous in-process
// observations are best effort, not durable reconciliation or fulfillment triggers.
//
// Example:
//
//	bus := event.New()
//	stop, err := bus.Listen(payment.OperationCompleted, func(ctx context.Context, observed payment.OperationEvent) error {
//		fmt.Println(observed.Operation)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer stop()
type OperationEvent struct {
	// Operation is the fixed snake_case facade operation, such as get_payment
	// or refund_payment. It never incorporates request or provider values.
	//
	// Example:
	//
	//	fmt.Println(observed.Operation) // "get_payment" for Client.GetPayment.
	Operation string

	// ErrorType is empty for completion; otherwise it is a payment sentinel name,
	// context.Canceled, context.DeadlineExceeded, or "unknown". ErrOutcomeUnknown
	// takes precedence over all joined causes and does not mean failed movement.
	// No arbitrary Go type names, error messages, or native statuses are copied.
	//
	// Example:
	//
	//	if observed.ErrorType == "payment.ErrOutcomeUnknown" {
	//		fmt.Println("reconciliation remains application-owned")
	//	}
	ErrorType string
}

var (
	// OperationCompleted reports a nil driver error after one facade call.
	// Reads, pending responses, and idempotent replays also complete; this is not
	// a payment-succeeded, refund-completed, or subscription-activated event.
	//
	// Example:
	//
	//	stop, err := bus.Listen(payment.OperationCompleted, func(ctx context.Context, observed payment.OperationEvent) error {
	//		fmt.Println(observed.Operation)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer stop()
	OperationCompleted = event.NewTopic[OperationEvent]("payment.operation.completed")

	// OperationFailed reports a non-nil driver error, not failed money movement.
	// ErrorType payment.ErrOutcomeUnknown means the provider may have applied a
	// mutation. Reconcile using application-owned state and provider guarantees;
	// never infer that retrying with a new key is safe from this notification.
	// Validation, missing drivers, and pre-canceled calls emit neither topic.
	//
	// Example:
	//
	//	stop, err := bus.Listen(payment.OperationFailed, func(ctx context.Context, observed payment.OperationEvent) error {
	//		fmt.Println(observed.Operation, observed.ErrorType)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer stop()
	OperationFailed = event.NewTopic[OperationEvent]("payment.operation.failed")
)

// emit reports exactly one returned provider result without retaining its data.
// A disabled bus avoids classification; Emit isolates listener errors and panics
// from the API result. The operation context carries existing trace metadata.
func (client *Client) emit(ctx context.Context, operation string, err error) {
	if client.events == nil {
		return
	}

	if err != nil {
		client.events.Emit(ctx, OperationFailed, OperationEvent{
			Operation: operation,
			ErrorType: operationErrorType(err),
		})

		return
	}

	client.events.Emit(ctx, OperationCompleted, OperationEvent{Operation: operation})
}

// operationErrorType bounds observation classes to known identities. Uncertain
// outcomes outrank context and transport causes without modifying the API error.
// Unrecognized failures use a literal fallback rather than native text or types.
func operationErrorType(err error) string {
	if errors.Is(err, ErrOutcomeUnknown) {
		return "payment.ErrOutcomeUnknown"
	}

	if errors.Is(err, context.Canceled) {
		return "context.Canceled"
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "context.DeadlineExceeded"
	}

	for _, sentinel := range errorSentinels {
		if errors.Is(err, sentinel.Err) {
			return sentinel.Name
		}
	}

	return "unknown"
}
