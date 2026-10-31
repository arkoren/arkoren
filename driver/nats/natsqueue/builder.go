package natsqueue

import "slices"

// configureDriver invokes policies before validation and detaches owned slices.
// Native option captures remain application-owned and run only during Start.
func configureDriver(configure ...BuilderFunc) settings {
	consumer := &Builder{config: defaultConfig()}

	for _, configure := range configure {
		configure(consumer)
	}

	config := consumer.config
	config.URLs = slices.Clone(config.URLs)
	config.Options = slices.Clone(config.Options)

	return config
}
