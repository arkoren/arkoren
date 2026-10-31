package ratelimit

import (
	"cmp"
	"net/http"
	"reflect"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/foundation/problem"
)

// BuilderFunc edits one rate-limit construction draft. Policies run once,
// synchronously in order, before final dependency, numeric, and namespace
// validation. A supplied nil policy panics; an expanded nil slice supplies none.
// Retained drafts cannot change the published middleware; services and function
// captures remain shared.
//
// Example:
//
//	var perMinute ratelimit.BuilderFunc = func(limiter *ratelimit.Builder) {
//		limiter.Limit(60)
//		limiter.Window(time.Minute)
//	}
//	routes.Use(ratelimit.Middleware(store, perMinute))
type BuilderFunc = func(*Builder)

// settings configures a sliding-window rate limiter.
type settings struct {
	// Limit is the number of requests allowed within Window.
	Limit int

	// Window is the length of the sliding window over which Limit applies.
	Window time.Duration

	// KeyFunc extracts the client-specific portion of the cache key.
	KeyFunc func(*http.Request) string

	// Namespace names counters. Window policy overrides its retention TTL.
	Namespace cache.Namespace[int64]

	// ErrorResponse is returned when a request exceeds Limit. Only a completely
	// zero problem selects ErrRateLimited; other absent members remain for HTTP
	// rendering to complete without discarding the configured problem.
	ErrorResponse problem.Details

	// Events receives [Exceeded]. Nil, the default, publishes nothing.
	Events *event.Bus
}

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	limiter := &Builder{
		settings: defaultConfig(),
	}

	for _, configure := range configure {
		configure(limiter)
	}

	config := limiter.settings

	return config.withDefaults()
}

// defaultConfig returns the standard per-client rate-limit policy.
func defaultConfig() settings {
	return settings{
		Limit:   60,
		Window:  time.Minute,
		KeyFunc: clientIP,
		Namespace: cache.NewNamespace[int64]("arkoren:ratelimit", func(space *cache.NamespaceBuilder) {
			space.Separator(":")
		}),
		ErrorResponse: ErrRateLimited,
	}
}

// withDefaults replaces unset numeric and key settings and only a completely
// zero ErrorResponse. Status zero alone does not mean an absent problem: headers,
// causes, extensions, or other members can still be intentional configuration.
// The HTTP renderer owns completion of those nonzero problems' absent members.
func (config settings) withDefaults() settings {
	defaults := defaultConfig()
	config.Limit = cmp.Or(config.Limit, defaults.Limit)
	config.Window = cmp.Or(config.Window, defaults.Window)

	if config.KeyFunc == nil {
		config.KeyFunc = defaults.KeyFunc
	}

	if reflect.ValueOf(config.ErrorResponse).IsZero() {
		config.ErrorResponse = defaults.ErrorResponse
	}

	return config
}
