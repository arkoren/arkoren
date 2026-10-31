package memorycache

import (
	"time"

	"arkoren.dev/core/service/cache/memorycache/internal/storage"
)

// settings configures the in-memory cache.
type settings struct {
	// Cleanup is the minimum interval between sweeps that remove expired
	// entries nobody reads again. Writes ([Driver.Put], [Driver.Add],
	// [Driver.Increment], [Driver.Decrement], [Driver.IncrementWithTTL],
	// [Driver.TryLock])
	// sweep inline when the last sweep is older than Cleanup, and the
	// janitor started by [Driver.Start] sweeps on the same interval. Zero
	// uses the default of one minute; negative disables all sweeping, so
	// expired entries are then removed only when their key is accessed.
	Cleanup time.Duration
}

// defaultConfig returns the default in-memory cache configuration.
func defaultConfig() settings {
	return settings{Cleanup: time.Minute}
}

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	memorycache.New(func(store *memorycache.Builder) {
//		store.Cleanup(-time.Second)
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...BuilderFunc) settings {
	cache := &Builder{settings: defaultConfig()}

	for _, configure := range configure {
		configure(cache)
	}

	return cache.settings
}

// Cleanup sets the minimum interval between inline expiry sweeps and, when
// started, janitor sweeps. Zero resolves to one minute; negative disables both
// kinds of sweeping. Expired entries are still removed when accessed.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	memorycache.New(func(store *memorycache.Builder) {
//		store.Cleanup(-time.Second)
//	})
func (store *Builder) Cleanup(cleanup time.Duration) *Builder {
	store.settings.Cleanup = cleanup

	return store
}

// BuilderFunc configures a Builder.
// Example:
//
//	var policy memorycache.BuilderFunc = func(store *memorycache.Builder) {
//		store.Cleanup(time.Minute)
//	}
//	_ = memorycache.New(policy)
type BuilderFunc = func(*Builder)

// New creates a Driver with the completed construction policies. It starts no
// goroutines; unless Cleanup is negative, [Driver.Start] launches a janitor
// that [Driver.Stop] ends, and writes sweep expired entries inline either
// way. Expired entries are never returned, whether or not a sweep ran.
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
//
// Example:
//
//	memorycache.New(func(store *memorycache.Builder) {
//		store.Cleanup(-time.Second)
//	})
func New(configure ...BuilderFunc) *Driver {
	config := newSettings(configure...)

	return &Driver{store: storage.NewStore(config.Cleanup)}
}
