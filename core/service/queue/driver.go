package queue

import (
	"context"
	"time"
)

// Message is the transport delivery envelope for a job. Retry settings are
// intentionally excluded because the consumer takes them from the job
// registered for Name.
// Raw callers own validation and encoding; Dispatcher creates these envelopes
// from immutable Job definitions. Metadata limits describe portable delivery.
//
//	message := queue.Message{Queue: queue.DefaultQueue, Name: "send", Payload: []byte(`"hello"`)}
//	if err := driver.Dispatch(ctx, message); err != nil {
//		return err
//	}
type Message struct {
	// ID identifies the message. [Dispatcher.Dispatch] assigns a random ID;
	// drivers that deduplicate, such as JetStream, use it as the message ID.
	ID string `json:"id"`

	// Name identifies the application job definition.
	Name string `json:"name"`

	// Queue identifies the queue that received the job.
	Queue string `json:"queue"`

	// Payload is the JSON encoding of the job value.
	Payload []byte `json:"payload"`

	// Attempts is the number of delivery attempts when the driver provides that
	// information. A value of zero means it is unavailable.
	Attempts int `json:"attempts"`

	// Metadata carries string key-value pairs beside the payload, such as the
	// trace context [Dispatcher.Dispatch] injects. Drivers deliver it to
	// consumers unchanged: nil and empty are equivalent, and a message
	// dispatched without metadata arrives with nil Metadata. The guarantee
	// covers at most MaxMetadataEntries entries whose keys are 1 to
	// MaxMetadataKeyLength bytes of [A-Za-z0-9._-] (case-sensitive) and whose
	// values are valid UTF-8, with at most MaxMetadataSize bytes of keys and
	// values together. Beyond those limits a driver may reject the message.
	// Metadata is not part of the payload and never affects routing.
	Metadata map[string]string `json:"metadata,omitzero"`
}

// Limits of the [Message.Metadata] round-trip guarantee.
const (
	// MaxMetadataEntries is the most metadata entries a driver must carry.
	// Larger maps fall outside the portable round-trip guarantee.
	//
	//	if len(message.Metadata) > queue.MaxMetadataEntries {
	//		return errors.New("too many metadata entries")
	//	}
	MaxMetadataEntries = 16

	// MaxMetadataKeyLength is the longest metadata key a driver must carry,
	// in bytes.
	//
	//	if len(metadataKey) > queue.MaxMetadataKeyLength {
	//		return errors.New("metadata key too long")
	//	}
	MaxMetadataKeyLength = 64

	// MaxMetadataSize is the most bytes of keys and values together a driver
	// must carry.
	// This counts key and value bytes together, not just encoded payload bytes.
	//
	//	if metadataBytes > queue.MaxMetadataSize {
	//		return errors.New("metadata too large")
	//	}
	MaxMetadataSize = 4096
)

// DispatcherDriver dispatches raw [Message] values. A successful
// dispatch confirms only that the driver accepted the message, not that a job
// handler processed it.
//
//	var transport queue.DispatcherDriver = memoryqueue.New()
//	dispatcher := queue.NewDispatcher(transport)
//	if err := dispatcher.Dispatch(ctx, queue.NewJob[string]("send"), "hello"); err != nil {
//		return err
//	}
type DispatcherDriver interface {
	// Dispatch adds message to its named queue. It returns an error when
	// message.Queue is empty and the context error when ctx is already
	// canceled. The driver delivers Name, Queue, Payload, and Metadata
	// unchanged to consumers (see [Message.Metadata]). Message.ID is stable across redeliveries of one message, but a
	// driver may replace the dispatched value with a broker identifier.
	// Attempts is reported by the consumer side and ignored here.
	//
	//	if err := driver.Dispatch(ctx, message); err != nil {
	//		return err
	//	}
	Dispatch(ctx context.Context, message Message) error
}

// Delivery is a broker-reserved job message. Its settlement methods hide
// transport details such as SQS receipt handles and AMQP delivery tags. A
// delivery is settled exactly once: a second settlement call, concurrent or
// not, returns an error and changes nothing.
// Settlement failures reach the consumer driver; an unsuccessful settlement
// does not confirm the requested outcome. Contexts bound settlement operations.
//
//	if err := delivery.Acknowledge(ctx); err != nil {
//		return err
//	}
type Delivery interface {
	// Message returns the job message being delivered.
	//
	//	message := delivery.Message()
	//	fmt.Println(message.Name, message.Attempts)
	Message() Message

	// Acknowledge permanently completes this delivery.
	//
	//	if err := delivery.Acknowledge(ctx); err != nil {
	//		return err
	//	}
	Acknowledge(ctx context.Context) error

	// Retry requests another delivery of the same message, with Attempts
	// incremented when the driver reports it. A zero delay delegates timing to
	// the driver's configured default. A positive delay is a lower bound: the
	// message is not delivered again earlier, though drivers may round it up.
	//
	//	if err := delivery.Retry(ctx, time.Minute); err != nil {
	//		return err
	//	}
	Retry(ctx context.Context, delay time.Duration) error

	// Reject permanently rejects this delivery. The driver's broker may
	// dead-letter rejected deliveries according to its configured policy;
	// brokers that implement rejection as redrive may deliver the message
	// again until that policy moves it.
	//
	//	if err := delivery.Reject(ctx); err != nil {
	//		return err
	//	}
	Reject(ctx context.Context) error

	// Fail requests terminal failure handling for this delivery: the message
	// is not delivered again. The driver's broker may archive or dead-letter
	// it according to its configured policy.
	//
	//	if err := delivery.Fail(ctx, cause); err != nil {
	//		return err
	//	}
	Fail(ctx context.Context, err error) error
}

// DeliveryHandler handles one broker-reserved [Delivery]. Returning an
// error reports a consumer or settlement failure to the consumer driver.
// Successful handlers must settle the delivery; returning nil alone is not an
// acknowledgement. Use Consumer for typed execution and settlement policy.
//
//	handler := queue.DeliveryHandler(func(ctx context.Context, delivery queue.Delivery) error {
//		return delivery.Acknowledge(ctx)
//	})
//	if err := driver.Consume(ctx, "work", handler); err != nil {
//		return err
//	}
type DeliveryHandler = func(context.Context, Delivery) error

// ConsumerDriver receives deliveries from a named queue. It must not
// acknowledge a delivery before handler returns successfully.
// The handler, not the driver's return value, selects delivery settlement.
//
//	consumer := queue.NewConsumer(driver)
//	if err := consumer.Handle(job, send); err != nil {
//		return err
//	}
//	compose.Run(consumer.Run)
type ConsumerDriver interface {
	// Consume receives deliveries from queue until ctx is canceled or an error
	// occurs. The handler is responsible for settling every accepted delivery.
	// Cancellation ends Consume with an error matching the context error under
	// [errors.Is], and a context that is already canceled is rejected the same
	// way. Concurrent Consume calls on one queue compete for messages. The
	// driver reports [Message.Attempts] on every delivery or on none.
	//
	//	if err := driver.Consume(ctx, "work", handler); err != nil {
	//		return err
	//	}
	Consume(ctx context.Context, queue string, handler DeliveryHandler) error
}
