package requestlog

// BuilderFunc edits the construction draft shared by [Middleware]'s callbacks.
// Callbacks run synchronously in order; a supplied nil callback panics, while
// expanding a nil or empty callback slice makes no edits. The logger remains a
// shared service, but retaining the draft cannot replace the published logger.
//
// Example:
//
//	configure := requestlog.BuilderFunc(func(log *requestlog.Builder) {
//		log.Logger(slog.Default())
//	})
//	routes := router.New()
//	routes.Use(requestlog.Middleware(configure))
type BuilderFunc = func(*Builder)

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	log := &Builder{}

	for _, configure := range configure {
		configure(log)
	}

	config := log.settings

	return config
}
