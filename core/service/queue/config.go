package queue

import (
	"slices"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// settings holds construction-only state. Service pointers remain shared;
// queue restrictions are copied at capture and publication boundaries.
type settings struct {
	// Telemetry is resolved per operation when nil.
	Telemetry *telemetry.Telemetry
	// System overrides the driver's messaging system when nonempty.
	System string
	// Queues restricts consumption; empty means all handled queues.
	Queues []string
	// UnknownNameAttempts defaults to ten at use; negative means unlimited.
	UnknownNameAttempts int
	// Events receives consumer outcomes; nil disables publication.
	Events *event.Bus
}

// ConsumerBuilder configures one consumer before publication. Constructors
// invoke callbacks directly, once and in order on the same draft. Retaining
// the draft does not permit changing the published consumer. Service pointers
// and function captures remain shared; concurrent draft mutation is unsupported.
//
//	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
//		consumer.Queues("emails").UnknownNameAttempts(20)
//	})
type ConsumerBuilder struct {
	// settings contains the mutable construction draft.
	settings settings
}

// DispatcherBuilder configures dispatcher telemetry before publication. It
// deliberately exposes no consumer-only settings. Callbacks share one draft
// and run directly once in order; retained drafts cannot change a dispatcher.
//
//	dispatcher := queue.NewDispatcher(driver, func(dispatcher *queue.DispatcherBuilder) {
//		dispatcher.System("custom")
//	})
type DispatcherBuilder struct {
	// settings contains the mutable construction draft.
	settings settings
}

// Telemetry chooses the shared telemetry service and returns consumer. Nil
// follows telemetry.Default at each operation, including later default changes.
//
//	queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
//		consumer.Telemetry(nil)
//	})
func (consumer *ConsumerBuilder) Telemetry(service *telemetry.Telemetry) *ConsumerBuilder {
	consumer.settings.Telemetry = service

	return consumer
}

// System overrides messaging.system and returns consumer. Empty restores
// driver discovery, falling back to "_OTHER" when the driver has no system.
//
//	queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
//		consumer.System("custom")
//	})
func (consumer *ConsumerBuilder) System(system string) *ConsumerBuilder {
	consumer.settings.System = system

	return consumer
}

// Queues replaces the restriction and returns consumer. It copies the input;
// publication copies it again. Empty removes the restriction. Run skips queues
// without handled jobs, even when named here.
//
//	queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
//		consumer.Queues("emails")
//	})
func (consumer *ConsumerBuilder) Queues(queues ...string) *ConsumerBuilder {
	consumer.settings.Queues = slices.Clone(queues)

	return consumer
}

// UnknownNameAttempts sets the unknown-job retry cap and returns consumer.
// Zero uses ten, negative retries indefinitely, with a 30-second delay.
// Drivers must report Message.Attempts for the cap to take effect.
//
//	queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
//		consumer.UnknownNameAttempts(20)
//	})
func (consumer *ConsumerBuilder) UnknownNameAttempts(attempts int) *ConsumerBuilder {
	consumer.settings.UnknownNameAttempts = attempts

	return consumer
}

// Events selects the shared bus for job outcomes and returns consumer. Nil
// publishes nothing; listeners never change the settlement outcome.
//
//	queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
//		consumer.Events(bus)
//	})
func (consumer *ConsumerBuilder) Events(bus *event.Bus) *ConsumerBuilder {
	consumer.settings.Events = bus

	return consumer
}

// Telemetry selects the shared telemetry service and returns dispatcher. Nil
// follows telemetry.Default dynamically at each dispatch, not at construction.
//
//	queue.NewDispatcher(driver, func(dispatcher *queue.DispatcherBuilder) {
//		dispatcher.Telemetry(nil)
//	})
func (dispatcher *DispatcherBuilder) Telemetry(service *telemetry.Telemetry) *DispatcherBuilder {
	dispatcher.settings.Telemetry = service

	return dispatcher
}

// System overrides messaging.system and returns dispatcher. Empty restores
// driver discovery, falling back to "_OTHER" for unnamed drivers.
//
//	queue.NewDispatcher(driver, func(dispatcher *queue.DispatcherBuilder) {
//		dispatcher.System("custom")
//	})
func (dispatcher *DispatcherBuilder) System(system string) *DispatcherBuilder {
	dispatcher.settings.System = system

	return dispatcher
}

// ConsumerBuilderFunc edits one fresh consumer draft. Queue restrictions are
// detached at publication; telemetry and event services stay shared. Supplied
// nil callbacks panic, while expanded empty callback slices make no edits.
//
//	policy := queue.ConsumerBuilderFunc(func(consumer *queue.ConsumerBuilder) {
//		consumer.Queues("work")
//	})
//	consumer := queue.NewConsumer(driver, policy)
type ConsumerBuilderFunc = func(*ConsumerBuilder)

// DispatcherBuilderFunc edits a dispatcher draft once during construction.
// It configures only producer settings and cannot change consumer policy;
// publication detaches the draft without cloning service dependencies.
//
//	policy := queue.DispatcherBuilderFunc(func(dispatcher *queue.DispatcherBuilder) {
//		dispatcher.System("custom")
//	})
//	dispatcher := queue.NewDispatcher(driver, policy)
type DispatcherBuilderFunc = func(*DispatcherBuilder)
