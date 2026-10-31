package awsqueue

import (
	"log/slog"
	"time"
)

// Builder holds optional SQS construction settings. Callbacks share one fresh
// draft initialized with polling defaults; publication does not retain it.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.MaxMessages(5)
//	}
//	driver, err := New(ctx, map[string]string{"emails": "queue-url"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Builder struct {
	// config holds settings separately from the constructed driver.
	config settings
}

// BuilderFunc edits one fresh SQS construction draft in argument order.
// Nil entries panic; publication detaches completed settings, not native clients.
//
// Example:
//
//	var configure awsqueue.BuilderFunc = func(consumer *awsqueue.Builder) {
//		consumer.VisibilityTimeout(time.Minute)
//	}
//	driver, err := awsqueue.New(ctx, map[string]string{"emails": "queue-url"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// FailureQueueURL replaces the terminal-report destination. Empty disables
// reports. The same draft is returned; no queue is created.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.FailureQueueURL("")
//	}
//	driver, err := New(ctx, map[string]string{"emails": "queue-url"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) FailureQueueURL(url string) *Builder {
	consumer.config.FailureQueueURL = url

	return consumer
}

// WaitTimeSeconds replaces the long-poll duration in seconds. Zero selects 20;
// construction rejects values outside 1–20. It returns the same draft.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.WaitTimeSeconds(1)
//	}
//	driver, err := New(ctx, map[string]string{"emails": "queue-url"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) WaitTimeSeconds(seconds int32) *Builder {
	consumer.config.WaitTimeSeconds = seconds

	return consumer
}

// MaxMessages replaces the pull batch limit. Zero selects one; construction
// rejects values outside 1–10. It returns the same construction draft.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.MaxMessages(5)
//	}
//	driver, err := New(ctx, map[string]string{"emails": "queue-url"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) MaxMessages(count int32) *Builder {
	consumer.config.MaxMessages = count

	return consumer
}

// Logger replaces the shared delivery logger. Nil selects the driver's existing
// default logging policy. It returns the draft without changing a live logger.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.Logger(slog.New(slog.DiscardHandler))
//	}
//	driver, err := New(ctx, map[string]string{"emails": "queue-url"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) Logger(logger *slog.Logger) *Builder {
	consumer.config.Logger = logger

	return consumer
}

// VisibilityTimeout replaces the requested lease duration. Zero selects the
// 30-second default, not immediate visibility or disabled renewal. [New] and
// [NewFrom] reject nonzero values outside one second through 12 hours inclusive
// with [ErrInvalidConfig] before loading AWS configuration or making requests.
// Fractional seconds round up for SQS; heartbeat ticks use half the configured
// duration, not half the rounded value. Each renewal SDK operation is bounded
// by that half duration or ten seconds, whichever is shorter. Renewal errors
// are logged and can allow redelivery; see [Driver.Consume]. It returns the same
// draft.
//
// Example:
//
//	configure := func(consumer *Builder) {
//		consumer.VisibilityTimeout(time.Minute)
//	}
//	driver, err := New(ctx, map[string]string{"emails": "queue-url"}, configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) VisibilityTimeout(timeout time.Duration) *Builder {
	consumer.config.VisibilityTimeout = timeout

	return consumer
}

// configured runs policies directly once in order on a fresh default draft.
// Nil entries panic; empty or nil slice expansion supplies no policies.
func configured(configure ...BuilderFunc) settings {
	queue := &Builder{
		config: defaultConfig(),
	}

	for _, configure := range configure {
		configure(queue)
	}

	return queue.config
}
