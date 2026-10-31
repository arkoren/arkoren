package csrf

import "slices"

// BuilderFunc edits the construction draft shared by [Middleware]'s callbacks.
// Callbacks run synchronously in order; a supplied nil callback panics, while
// expanding a nil or empty callback slice makes no edits. Trusted-origin lists
// are copied; a supplied native protection engine remains shared.
//
// Example:
//
//	configure := csrf.BuilderFunc(func(protection *csrf.Builder) {
//		protection.TrustedOrigins("https://partner.example")
//	})
//	routes := router.New()
//	routes.Use(csrf.Middleware(configure))
type BuilderFunc = func(*Builder)

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	protection := &Builder{
		settings: settings{},
	}

	for _, configure := range configure {
		configure(protection)
	}

	config := protection.settings

	config.TrustedOrigins = slices.Clone(config.TrustedOrigins)

	return config
}
