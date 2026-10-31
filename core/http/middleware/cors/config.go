package cors

import "slices"

// BuilderFunc edits one CORS construction draft. Policies run once, synchronously
// in order, before final origin and credential validation. A supplied nil policy
// panics; an expanded nil slice supplies no policies. Publication detaches allow
// lists, so retained drafts cannot change the middleware.
//
// Example:
//
//	var allowApp cors.BuilderFunc = func(policy *cors.Builder) {
//		policy.AllowedOrigins("https://app.example.com")
//	}
//	routes.Use(cors.Middleware(allowApp))
type BuilderFunc = func(*Builder)

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	policy := &Builder{
		settings: defaultConfig(),
	}

	for _, configure := range configure {
		configure(policy)
	}

	config := policy.settings

	config.AllowedOrigins = slices.Clone(config.AllowedOrigins)
	config.AllowedMethods = slices.Clone(config.AllowedMethods)
	config.AllowedHeaders = slices.Clone(config.AllowedHeaders)
	config.ExposedHeaders = slices.Clone(config.ExposedHeaders)

	return config.withDefaults()
}
