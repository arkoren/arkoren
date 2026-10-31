package secureheaders

// BuilderFunc edits the construction draft shared by [Middleware]'s callbacks.
// Callbacks run synchronously in order; a supplied nil callback panics, while
// expanding a nil or empty callback slice makes no edits. Construction begins
// with recommended headers and no HSTS; explicit empty values stay omitted.
//
// Example:
//
//	configure := secureheaders.BuilderFunc(func(headers *secureheaders.Builder) {
//		headers.FrameOptions("")
//	})
//	routes := router.New()
//	routes.Use(secureheaders.Middleware(configure))
type BuilderFunc = func(*Builder)

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	headers := &Builder{
		settings: defaultConfig(),
	}

	for _, configure := range configure {
		configure(headers)
	}

	config := headers.settings

	return config
}
