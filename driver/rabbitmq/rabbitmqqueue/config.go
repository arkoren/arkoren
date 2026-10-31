package rabbitmqqueue

import (
	"log/slog"
	"time"
)

// Builder configures optional connection-independent policy. New and NewFrom
// invoke each callback directly once, in order, before validation. A nil entry
// panics; an expanded nil or empty slice makes no edits. Retaining the draft
// cannot change the published driver. Loggers remain shared services.
//
// Example:
//
//	queues := map[string]rabbitmqqueue.Queue{"work": {Name: "jobs.work", RoutingKey: "work"}}
//	driver, err := rabbitmqqueue.New(url, queues, func(broker *rabbitmqqueue.Builder) {
//		broker.Prefetch(16)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
type Builder struct {
	// config holds draft settings, never the driver's published root.
	config settings
}

// BuilderFunc is a construction callback shared by New and NewFrom. Callbacks
// edit optional policy on one draft, run once in order, and cannot replace the
// required routing map. Nil entries panic; expanded nil slices omit edits.
//
// Example:
//
//	var policy rabbitmqqueue.BuilderFunc = func(broker *rabbitmqqueue.Builder) {
//		broker.Prefetch(16)
//	}
//	driver, err := rabbitmqqueue.New(url, queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// configureDriver applies optional policies to a fresh default configuration.
// Required topology is captured separately by constructors, so callbacks cannot
// replace it. Publication copies the completed settings root.
func configureDriver(configure ...BuilderFunc) settings {
	broker := &Builder{config: defaultConfig()}

	for _, configure := range configure {
		configure(broker)
	}

	return broker.config
}

// Exchange replaces the durable direct exchange and returns the same draft.
// Empty selects DefaultExchange; whitespace-only names fail construction.
// This does not rename existing queues, bindings, or retry destinations.
//
// Example:
//
//	policy := func(broker *rabbitmqqueue.Builder) {
//		broker.Exchange("billing.jobs")
//	}
//	driver, err := rabbitmqqueue.New(url, queues, policy)
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (broker *Builder) Exchange(exchange string) *Builder {
	broker.config.Exchange = exchange

	return broker
}

// Prefetch limits unsettled deliveries per Consume call and returns the draft.
// Zero selects one; negative values and counts above 65535 fail construction
// instead of being truncated to AMQP's uint16 field. Handlers run sequentially
// within one Consume call; use additional consumers for handler concurrency.
//
// Example:
//
//	driver, err := rabbitmqqueue.New(url, queues, func(broker *rabbitmqqueue.Builder) {
//		broker.Prefetch(16)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (broker *Builder) Prefetch(prefetch int) *Builder {
	broker.config.Prefetch = prefetch

	return broker
}

// RetryDelay sets the delay used by Delivery.Retry(ctx, 0) and returns the draft.
// Zero selects one second; negative or unrepresentable TTL values fail
// construction. The normalized default must not exceed MaxRetryDelay.
// Positive retry delays round upward into broker queue buckets; expiry and
// forwarding can take longer. See the package's dead-letter durability caveats.
//
// Example:
//
//	driver, err := rabbitmqqueue.New(url, queues, func(broker *rabbitmqqueue.Builder) {
//		broker.RetryDelay(5 * time.Second)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (broker *Builder) RetryDelay(delay time.Duration) *Builder {
	broker.config.RetryDelay = delay

	return broker
}

// MaxRetryDelay bounds requested retry delays and returns the draft. Zero
// permits the AMQP TTL maximum, 4294967295 milliseconds (about 49 days).
// Negative or larger limits fail construction. This bounds the requested
// delay, not upward bucketing or the time the broker takes to forward a job.
// A retry beyond the limit returns ErrUnsupportedRetryDelay without settlement.
//
// Example:
//
//	driver, err := rabbitmqqueue.New(url, queues, func(broker *rabbitmqqueue.Builder) {
//		broker.MaxRetryDelay(time.Hour)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (broker *Builder) MaxRetryDelay(delay time.Duration) *Builder {
	broker.config.MaxRetryDelay = delay

	return broker
}

// FailureQueue selects a durable failure archive and returns the draft. Empty
// disables the archive: Fail rejects without requeue, leaving dead-lettering to
// broker policy. The archive must not also be a configured work queue. A failed
// archive publication leaves the source unsettled and returns an error.
// Failure envelopes contain message, error, and raw_message (the original body,
// even for valid jobs); apply access controls and retention for sensitive data.
//
// Example:
//
//	driver, err := rabbitmqqueue.New(url, queues, func(broker *rabbitmqqueue.Builder) {
//		broker.FailureQueue("jobs.failed")
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (broker *Builder) FailureQueue(name string) *Builder {
	broker.config.FailureQueue = name

	return broker
}

// Logger retains a shared logger and returns the same draft. Nil selects
// slog.Default() at construction. Handler failures are logged and returned to
// the Consume caller; they do not leave the consumer silently stalled.
//
// Example:
//
//	driver, err := rabbitmqqueue.New(url, queues, func(broker *rabbitmqqueue.Builder) {
//		broker.Logger(slog.Default())
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (broker *Builder) Logger(logger *slog.Logger) *Builder {
	broker.config.Logger = logger

	return broker
}
