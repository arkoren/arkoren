package broadcast

import "arkoren.dev/core/event"

// ListenerFailedEvent is the payload of [ListenerFailed]. It omits the
// message payload and metadata.
// Errors retain their original causes for application observers, not safe
// external rendering. Several matching listeners can each produce one value.
//
//	unsubscribe, err := bus.Listen(broadcast.ListenerFailed,
//		func(_ context.Context, outcome broadcast.ListenerFailedEvent) error {
//			fmt.Println(outcome.Name)
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
type ListenerFailedEvent struct {
	// ID is the message identifier, empty when the driver sets none.
	ID string

	// Name is the broadcast name of the message.
	Name string

	// Err is the reason: the listener's error, a [Permanent] error, a
	// payload decode error, or the error recovered from a listener panic.
	Err error
}

// ListenerFailed is published by a [Consumer] built with [ConsumerBuilder.Events] once
// for each listener that fails a delivery: the listener returned an error,
// the payload did not decode into its type, or it panicked. A delivery that
// several listeners fail publishes one event per failing listener, in
// listener order. Successful deliveries publish nothing.
// This event reports listener execution, not transport settlement or retention.
//
//	unsubscribe, err := bus.Listen(broadcast.ListenerFailed, recordFailure)
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
var ListenerFailed = event.NewTopic[ListenerFailedEvent]("broadcast.listener.failed")
