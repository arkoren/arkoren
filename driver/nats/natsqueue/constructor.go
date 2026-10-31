package natsqueue

import (
	"fmt"
	"maps"

	"arkoren.dev/driver/nats/internal/natsconfig"
)

// New creates a JetStream driver with required stream and queue topology.
// Callbacks run once in order before completed validation; nil entries panic.
// Construction copies routing and native option slices without dialing or
// executing native callbacks. Start later owns connection acquisition.
//
// Example:
//
//	driver, err := natsqueue.New("JOBS", queues)
//	if err != nil {
//		return err
//	}
//	_ = driver.Connection()
func New(stream string, queues map[string]Queue, configure ...BuilderFunc) (*Driver, error) {
	queues = maps.Clone(queues)
	config := configureDriver(configure...)
	config.Stream = stream
	config.Queues = queues

	if err := natsconfig.URLs(config.URLs); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	config, err := normalizeConfig(config)

	if err != nil {
		return nil, err
	}

	return newDriver(nil, config), nil
}
