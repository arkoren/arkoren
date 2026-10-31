package event

import "errors"

var (
	// ErrInvalidName reports an event name that [Bus.ListenEvent] rejects.
	ErrInvalidName = errors.New("invalid event name")

	// ErrNilFunc reports a registration without a function.
	ErrNilFunc = errors.New("nil function")
)
