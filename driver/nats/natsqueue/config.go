package natsqueue

import (
	"log/slog"
	"slices"
	"time"

	"github.com/nats-io/nats.go"
)

// Builder configures optional connection and delivery policy for [New] and
// [NewFrom]. Callbacks run once, synchronously in order on one fresh draft,
// including invalid topology. Nil entries panic; expanded nil slices omit edits.
// Publication detaches owned slices, not loggers, functions, or native captures.
// Required stream and queue mappings are constructor arguments, not draft edits.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.Batch(4)
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver.Connection()
type Builder struct {
	// config is a fresh draft copied before publication; it never aliases a driver root.
	config settings
}

// BuilderFunc edits one fresh draft synchronously in argument order. Nil entries
// panic; native option captures remain shared and execute only during Start.
type BuilderFunc = func(*Builder)

// URLs replaces the server list, copying input and publication slices. Empty
// selects [DefaultNATSURL]. New validates native URLs without connecting;
// NewFrom ignores this setting because its connection is borrowed.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.URLs("nats://localhost:4222")
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) URLs(urls ...string) *Builder {
	consumer.config.URLs = slices.Clone(urls)

	return consumer
}

// FailureSubject replaces the terminal failure destination. Empty logs the
// failure through Logger and acknowledges the original. A configured literal
// subject must differ from every work subject and requires application-owned
// JetStream storage; publication must succeed before the original is acknowledged.
// Failure envelopes contain job data and error text, so restrict broker access.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.FailureSubject("jobs.failed")
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) FailureSubject(subject string) *Builder {
	consumer.config.FailureSubject = subject

	return consumer
}

// Provision permits Start to create missing stream and durable consumers.
// The default is false: Start checks existing resources read-only. Existing
// incompatible resources are never changed, even when provisioning is enabled.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.Provision(true)
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) Provision(provision bool) *Builder {
	consumer.config.Provision = provision

	return consumer
}

// Batch replaces the positive pull batch size; zero uses one. Handlers process
// a fetched batch sequentially while heartbeats retain all fetched messages.
// MaxAckPending must accommodate the completed batch size.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.Batch(4)
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) Batch(batch int) *Builder {
	consumer.config.Batch = batch

	return consumer
}

// MaxAckPending replaces the per-consumer acknowledgement limit. Zero uses
// Batch; positive values must be at least Batch. Start requires matching native
// consumer limits so independently constructed workers agree on reservations.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.MaxAckPending(8)
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) MaxAckPending(pending int) *Builder {
	consumer.config.MaxAckPending = pending

	return consumer
}

// AckWait replaces the native acknowledgement deadline. Zero uses 30 seconds;
// values below two nanoseconds are invalid. Heartbeats run every half AckWait
// until settlement or handler completion. It does not bound pull-request expiry
// or guarantee exclusive processing during connection failures.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.AckWait(time.Minute)
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) AckWait(wait time.Duration) *Builder {
	consumer.config.AckWait = wait

	return consumer
}

// MaxDeliver replaces the positive native delivery cap. Zero leaves the server
// default in effect and accepts an existing cap; negative values are invalid.
// Exhaustion follows JetStream policy, not an automatic failure-subject transfer.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.MaxDeliver(10)
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) MaxDeliver(deliveries int) *Builder {
	consumer.config.MaxDeliver = deliveries

	return consumer
}

// Options replaces and copies the native connection-option slice. Start invokes
// these hooks only for New-owned connections; NewFrom ignores them. Functions
// and captured native objects remain shared and must be safe for native use.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.Options(nats.Timeout(time.Second))
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) Options(options ...nats.Option) *Builder {
	consumer.config.Options = slices.Clone(options)

	return consumer
}

// Logger replaces the shared delivery/heartbeat error logger. Nil selects
// slog.Default at construction; use slog.DiscardHandler to suppress records.
// The driver does not clone the logger or control its lifetime.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.Logger(slog.New(slog.DiscardHandler))
//	}
//	driver, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (consumer *Builder) Logger(logger *slog.Logger) *Builder {
	consumer.config.Logger = logger

	return consumer
}
