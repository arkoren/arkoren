package broadcast

import (
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// PublisherBuilderFunc configures one producer draft during NewPublisher.
// Policies run directly once in order; nil entries panic. Retaining the draft
// cannot change published settings, while services and captures remain shared.
//
//	policy := broadcast.PublisherBuilderFunc(func(publisher *broadcast.PublisherBuilder) {
//		publisher.System("custom")
//	})
//	publisher := broadcast.NewPublisher(driver, policy)
type PublisherBuilderFunc = func(*PublisherBuilder)

// ConsumerBuilderFunc configures one subscription facade draft, not its driver.
// Policies run once in order and publication detaches their settings. Nil
// entries panic; expanded nil or empty callback slices supply no edits.
//
//	policy := broadcast.ConsumerBuilderFunc(func(consumer *broadcast.ConsumerBuilder) {
//		consumer.Events(events)
//	})
//	consumer := broadcast.NewConsumer(driver, policy)
type ConsumerBuilderFunc = func(*ConsumerBuilder)

// settings configures a [Publisher] or a [Consumer].
type settings struct {
	// Telemetry receives the spans and metrics of every publish and
	// delivery. Nil uses [telemetry.Default], resolved each time one runs, so
	// calling [telemetry.SetDefault] after construction still takes effect.
	Telemetry *telemetry.Telemetry

	// System is the messaging.system telemetry attribute. Empty asks the
	// driver: a driver, or a driver it wraps, that has a
	// MessagingSystem() string method names its system, such as "redis".
	// Otherwise the attribute is "_OTHER".
	System string

	// Events receives [ListenerFailed] from a [Consumer]. The default, nil,
	// publishes nothing. A [Publisher] ignores it.
	Events *event.Bus
}

// PublisherBuilder configures NewPublisher before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	broadcast.NewPublisher(driver, func(publisher *broadcast.PublisherBuilder) {
//		publisher.System("custom")
//	})
type PublisherBuilder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// ConsumerBuilder configures NewConsumer before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	broadcast.NewConsumer(driver, func(consumer *broadcast.ConsumerBuilder) {
//		consumer.System("custom")
//	})
type ConsumerBuilder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newPublisherSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newPublisherSettings(configure ...PublisherBuilderFunc) settings {
	publisher := &PublisherBuilder{}

	for _, configure := range configure {
		configure(publisher)
	}

	return publisher.settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...ConsumerBuilderFunc) settings {
	consumer := &ConsumerBuilder{}

	for _, configure := range configure {
		configure(consumer)
	}

	return consumer.settings
}

// Telemetry sets the telemetry that records publishes and deliveries.
// Without it the component follows [telemetry.Default].
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	broadcast.NewPublisher(driver, func(publisher *broadcast.PublisherBuilder) {
//		publisher.Telemetry(nil)
//	})
func (publisher *PublisherBuilder) Telemetry(t *telemetry.Telemetry) *PublisherBuilder {
	publisher.settings.Telemetry = t

	return publisher
}

// System sets the messaging.system telemetry attribute, overriding the
// name the driver reports.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	broadcast.NewPublisher(driver, func(publisher *broadcast.PublisherBuilder) {
//		publisher.System("custom")
//	})
func (publisher *PublisherBuilder) System(system string) *PublisherBuilder {
	publisher.settings.System = system

	return publisher
}

// Telemetry sets the telemetry that records publishes and deliveries.
// Without it the component follows [telemetry.Default].
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	broadcast.NewConsumer(driver, func(consumer *broadcast.ConsumerBuilder) {
//		consumer.Telemetry(nil)
//	})
func (consumer *ConsumerBuilder) Telemetry(t *telemetry.Telemetry) *ConsumerBuilder {
	consumer.settings.Telemetry = t

	return consumer
}

// System sets the messaging.system telemetry attribute, overriding the
// name the driver reports.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	broadcast.NewConsumer(driver, func(consumer *broadcast.ConsumerBuilder) {
//		consumer.System("custom")
//	})
func (consumer *ConsumerBuilder) System(system string) *ConsumerBuilder {
	consumer.settings.System = system

	return consumer
}

// Events selects the shared bus receiving ListenerFailed after a failed
// consumer listener. Nil publishes nothing. Publishers have no event setting;
// listener failures cannot change the delivery result through this bus.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	broadcast.NewConsumer(driver, func(consumer *broadcast.ConsumerBuilder) {
//		consumer.Events(nil)
//	})
func (consumer *ConsumerBuilder) Events(bus *event.Bus) *ConsumerBuilder {
	consumer.settings.Events = bus

	return consumer
}
