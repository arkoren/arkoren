package natsbroadcast

import "slices"

// configureDriver applies construction policies before validation. It returns
// a detached value so a retained draft cannot mutate published settings.
func configureDriver(configure ...BuilderFunc) Config {
	broker := &Builder{config: defaultConfig()}

	for _, configure := range configure {
		configure(broker)
	}

	config := broker.config
	config.URLs = slices.Clone(config.URLs)
	config.RootCAs = slices.Clone(config.RootCAs)

	return config
}
