// Package broadcast defines transport-neutral contracts for cross-process
// messages. Broadcast names use dot-separated tokens with portable wildcards
// for consumer registrations.
//
// [Message.Metadata] travels beside each payload; when telemetry is enabled
// [Publisher.Publish] injects the trace context into it and [Consumer]
// continues that trace in a consumer span per message. See [PublisherBuilder]
// and [ConsumerBuilder] for construction settings.
//
// # Events
//
// With [ConsumerBuilder.Events], a [Consumer] publishes [ListenerFailed]
// ("broadcast.listener.failed") with the message ID, the message name, and the
// error for each listener that fails a delivery. Payloads never carry the
// message payload or metadata. Publishers and successful deliveries publish
// nothing.
//
// NewPublisher takes PublisherBuilder policies (Telemetry/System); NewConsumer
// takes ConsumerBuilder policies, which additionally expose Events. Policies run
// directly once in order on a fresh draft. Nil entries panic; expanded nil/empty
// slices supply no edits. Publication detaches settings, not drivers or services.
//
// Example:
//
//	publisher := broadcast.NewPublisher(driver, func(publisher *broadcast.PublisherBuilder) {
//		publisher.Telemetry(app.Telemetry())
//	})
//	_ = publisher.Driver()
package broadcast
