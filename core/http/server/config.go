package server

import "cmp"

// BuilderFunc edits the construction draft shared by [New]'s callbacks.
// Callbacks run synchronously in order; a supplied nil callback panics, while
// expanding a nil or empty callback slice makes no edits. Published settings
// are independent of the draft; native TLS configurations remain shared.
//
// Example:
//
//	configure := server.BuilderFunc(func(listener *server.Builder) {
//		listener.Addr("127.0.0.1:8080")
//	})
//	srv := server.New(http.NotFoundHandler(), configure)
//	fmt.Println(srv.Addr())
type BuilderFunc = func(*Builder)

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	listener := &Builder{
		settings: defaultConfig,
	}

	for _, configure := range configure {
		configure(listener)
	}

	config := listener.settings
	config.Addr = cmp.Or(config.Addr, defaultConfig.Addr)
	config.ReadHeaderTimeout = cmp.Or(config.ReadHeaderTimeout, defaultConfig.ReadHeaderTimeout)
	config.ReadTimeout = cmp.Or(config.ReadTimeout, defaultConfig.ReadTimeout)
	config.WriteTimeout = cmp.Or(config.WriteTimeout, defaultConfig.WriteTimeout)
	config.IdleTimeout = cmp.Or(config.IdleTimeout, defaultConfig.IdleTimeout)
	config.MaxHeaderBytes = cmp.Or(config.MaxHeaderBytes, defaultConfig.MaxHeaderBytes)
	config.ShutdownTimeout = cmp.Or(config.ShutdownTimeout, defaultConfig.ShutdownTimeout)

	return config
}
