package engine

import (
	"errors"
)

// ErrStopped is returned by Start after [Engine.Stop].
// A stopped instance cannot be restarted; construct another instance instead.
//
//	if errors.Is(err, memoryworkflow.ErrStopped) {
//		return fmt.Errorf("workflow worker is shutting down: %w", err)
//	}
var ErrStopped = errors.New("memory workflow driver is stopped")

// ErrQueueFull is returned by Signal when a run already buffers the maximum
// number of undelivered values of one signal.
// It does not discard the existing queue; the rejected value is not enqueued.
//
//	if errors.Is(err, memoryworkflow.ErrQueueFull) {
//		return fmt.Errorf("signal was not accepted: %w", err)
//	}
var ErrQueueFull = errors.New("memory workflow signal queue is full")
