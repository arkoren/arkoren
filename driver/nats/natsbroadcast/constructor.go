package natsbroadcast

import (
	"cmp"
	"fmt"
	"strings"
)

// New creates a NATS driver with custom configuration. It applies sensible
// defaults for unspecified fields and validates the connection options, but
// performs no network IO: call [Driver.Start] to connect.
// Callbacks run directly once in order on one draft; nil entries panic.
//
// Example:
//
//	driver, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
//		broker.Prefix("billing")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver.Connection()
func New(configure ...BuilderFunc) (*Driver, error) {
	config := configureDriver(configure...)

	if err := validateConfig(config); err != nil {
		return nil, err
	}

	connectOptions, err := natsOptions(config)

	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	return &Driver{
		urls: strings.Join(natsURLs(config.URLs), ","), connectOptions: connectOptions,
		owned: true, prefix: cmp.Or(config.Prefix, defaultPrefix),
		queueGroup: config.QueueGroup, logger: eventLogger(config.Logger),
	}, nil
}
