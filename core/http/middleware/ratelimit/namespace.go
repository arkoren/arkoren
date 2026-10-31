package ratelimit

import (
	"strconv"
	"time"

	"arkoren.dev/core/service/cache"
)

// Namespace selects the counter namespace and separator. Window indices are
// joined after the client key using the same separator, which also joins the
// denied-event marker suffix. Counter and marker retention is
// always twice Window, independently of the namespace TTL. Middleware panics
// for a zero or invalid namespace after running every construction callback.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.Namespace(cache.NewNamespace[int64]("app.rate"))
//	}))
func (limiter *Builder) Namespace(space cache.Namespace[int64]) *Builder {
	limiter.settings.Namespace = space

	return limiter
}

// counterKey joins the literal client and epoch index with the namespace's
// separator. Counter retention is supplied atomically by the window operation.
func counterKey(space cache.Namespace[int64], client string, index int64) cache.Key[int64] {
	return space.Key(client + space.Separator() + strconv.FormatInt(index, 10))
}

// deniedMarker shares the counter's literal identity and two-window retention.
func deniedMarker(current cache.Key[int64], separator string, length time.Duration) cache.Key[bool] {
	return cache.NewKey[bool](current.Name()+separator+"denied", func(entry *cache.KeyBuilder) {
		entry.TTL(2 * length)
	})
}
