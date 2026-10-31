package bodylimit

import "arkoren.dev/core/http/request"

// BuilderFunc edits the construction draft shared by [Middleware]'s callbacks.
// Callbacks run synchronously in order; a supplied nil callback panics, while
// expanding a nil or empty callback slice makes no edits. Zero or negative
// limits select the default ceiling after all callbacks have completed.
//
// Example:
//
//	configure := bodylimit.BuilderFunc(func(limiter *bodylimit.Builder) {
//		limiter.Limit(1 << 20)
//	})
//	routes := router.New()
//	routes.Use(bodylimit.Middleware(configure))
type BuilderFunc = func(*Builder)

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	limit := &Builder{
		settings: settings{
			Limit: request.DefaultMaxBodySize,
		},
	}

	for _, configure := range configure {
		configure(limit)
	}

	config := limit.settings

	if config.Limit <= 0 {
		config.Limit = request.DefaultMaxBodySize
	}

	return config
}
