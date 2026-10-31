package rabbitmqbroadcast

import (
	"log/slog"
)

// Builder configures one construction draft. New and NewFrom invoke supplied
// callbacks directly, once, in order; a nil callback panics. An expanded nil
// callback slice makes no changes. Retaining this draft does not permit changes
// to the constructed driver. The logger and a NewFrom connection remain shared;
// publication does not clone native connection internals or transfer ownership.
//
// Example:
//
//	var configure rabbitmqbroadcast.BuilderFunc = func(broker *rabbitmqbroadcast.Builder) {
//		broker.Exchange("billing.events")
//	}
//	driver, err := rabbitmqbroadcast.New("amqp://localhost:5672/", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Builder struct {
	// config holds draft settings, never the driver's published root.
	config settings
}

// BuilderFunc is a construction callback shared by New and NewFrom. Callbacks
// edit the same draft once in order; publication detaches its settings, not the
// shared logger. Nil entries panic; expanded nil slices omit edits.
//
// Example:
//
//	var policy rabbitmqbroadcast.BuilderFunc = func(broker *rabbitmqbroadcast.Builder) {
//		broker.Exchange("billing.events")
//	}
//	driver, err := rabbitmqbroadcast.New(url, policy)
//	if err != nil {
//		return err
//	}
//	_ = driver
type BuilderFunc = func(*Builder)

// configureDriver applies construction policies before validation. It returns
// a detached value so a retained draft cannot mutate published settings.
func configureDriver(configure ...BuilderFunc) settings {
	broadcast := &Builder{
		config: defaultConfig(),
	}

	for _, configure := range configure {
		configure(broadcast)
	}

	return broadcast.config
}

// Exchange replaces the draft's Exchange setting and returns the same draft.
// Empty selects DefaultAMQPExchange ("arkoren:events"). Start declares a durable
// topic exchange, so an existing exchange must have compatible properties.
// Replacing this name does not migrate existing bindings or subscribers.
//
// Example:
//
//	configure := func(broker *rabbitmqbroadcast.Builder) {
//		broker.Exchange("billing.events")
//	}
//	driver, err := rabbitmqbroadcast.New("amqp://localhost:5672/", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Exchange(exchange string) *Builder {
	broker.config.Exchange = exchange

	return broker
}

// Logger replaces the draft's Logger setting and returns the same draft.
// Nil selects slog.Default() at construction; an explicit logger remains shared.
// The driver logs handler errors and recovered panics without attaching bodies.
// Application error and panic text can still contain sensitive data.
// These reports do not change the auto-acknowledgement delivery policy.
//
// Example:
//
//	configure := func(broker *rabbitmqbroadcast.Builder) {
//		broker.Logger(slog.Default())
//	}
//	driver, err := rabbitmqbroadcast.New("amqp://localhost:5672/", configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Logger(logger *slog.Logger) *Builder {
	broker.config.Logger = logger

	return broker
}
