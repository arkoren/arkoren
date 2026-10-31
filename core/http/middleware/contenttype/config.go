package contenttype

import "slices"

// BuilderFunc edits the construction draft shared by [Middleware]'s callbacks.
// Callbacks run synchronously in order; a supplied nil callback panics, while
// expanding a nil or empty callback slice makes no edits. Publication detaches
// the allowed-type list so a retained draft cannot change the policy.
//
// Example:
//
//	configure := contenttype.BuilderFunc(func(content *contenttype.Builder) {
//		content.Allowed("application/json")
//	})
//	routes := router.New()
//	routes.Use(contenttype.Middleware(configure))
type BuilderFunc = func(*Builder)

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	content := &Builder{
		settings: settings{},
	}

	for _, configure := range configure {
		configure(content)
	}

	config := content.settings

	config.Allowed = slices.Clone(config.Allowed)

	return config.withDefaults()
}
