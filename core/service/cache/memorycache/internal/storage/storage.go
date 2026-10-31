// Package storage owns atomic cache values, counters, expiry and sweeping.
package storage

import (
	"cmp"
	"context"
	"sync"
	"time"
)

// Store implements [cache.Driver] in process memory.
//
// Store is suitable for single-process applications, local development, and
// tests where persistence across restarts is not required. It is safe for
// concurrent use. Expiry is checked on access, so correctness never depends
// on sweeping. Writes sweep expired entries inline at most once per
// [Builder.Cleanup], so memory stays bounded without calling [Store.Start].
//
// Example:
//
//	driver := memorycache.New()
//	if err := driver.Put(ctx, "version", []byte("v1"), time.Minute); err != nil {
//		return err
//	}
type Store struct {
	// Mu guards entries.
	Mu sync.Mutex

	// Entries maps keys to stored values, including expired ones the janitor
	// or a later access has not removed yet.
	Entries map[string]entry

	// Cleanup is the interval between sweeps, inline and janitor. Negative
	// disables sweeping.
	Cleanup time.Duration

	// LastSweep is when expired entries were last removed in bulk. Guarded
	// by mu.
	LastSweep time.Time

	// Lifecycle guards cancel and done.
	Lifecycle sync.Mutex

	// Cancel ends the janitor goroutine. It is nil until Start runs.
	Cancel context.CancelFunc

	// Done closes when the janitor goroutine exits.
	Done chan struct{}
}

// NewStore constructs the atomic store without starting background work.
func NewStore(cleanup time.Duration) *Store {
	return &Store{Entries: make(map[string]entry), Cleanup: cmp.Or(cleanup, time.Minute)}
}
