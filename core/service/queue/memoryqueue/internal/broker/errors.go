package broker

import (
	"errors"
)

var (
	// ErrDriverClosed rejects startup or queue admission after shutdown begins.
	// Already-canceled contexts can take precedence at admission boundaries.
	//
	//	if errors.Is(err, memoryqueue.ErrDriverClosed) {
	//		return err
	//	}
	ErrDriverClosed = errors.New("memory queue driver is closed")

	// ErrInvalidQueue is returned when a queue name is empty.
	//
	//	err := driver.Dispatch(ctx, queue.Message{})
	//	fmt.Println(errors.Is(err, memoryqueue.ErrInvalidQueue))
	ErrInvalidQueue = errors.New("invalid job queue")

	// ErrInvalidHandler is returned when Consume is given a nil handler.
	//
	//	err := driver.Consume(ctx, "work", nil)
	//	fmt.Println(errors.Is(err, memoryqueue.ErrInvalidHandler))
	ErrInvalidHandler = errors.New("invalid job delivery handler")

	// ErrDeliverySettled is returned when a delivery is settled more than once.
	// It also rejects a concurrent settlement already in progress.
	//
	//	if err := delivery.Acknowledge(ctx); errors.Is(err, memoryqueue.ErrDeliverySettled) {
	//		return err
	//	}
	ErrDeliverySettled = errors.New("job delivery already settled")

	// ErrDeliveryUnsettled is returned when a handler returns without settling a delivery.
	// The driver attempts terminal cleanup before ending Consume with this error.
	//
	//	if errors.Is(err, memoryqueue.ErrDeliveryUnsettled) {
	//		return err
	//	}
	ErrDeliveryUnsettled = errors.New("job delivery was not settled")
)
