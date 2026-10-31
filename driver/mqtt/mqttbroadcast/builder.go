package mqttbroadcast

// configureDriver applies construction policies before validation. It returns
// a detached value so a retained draft cannot mutate published settings.
func configureDriver(configure ...BuilderFunc) Config {
	broadcast := &Builder{config: defaultConfig()}

	for _, configure := range configure {
		configure(broadcast)
	}

	return broadcast.config
}
