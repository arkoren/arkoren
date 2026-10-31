// Package lazy caches the latest value built for a comparable owner.
package lazy

import (
	"sync"
	"sync/atomic"
)

// Cache serializes misses with a construction mutex. Hits read one atomic
// pointer without locking. It must not be copied after first use, and build
// must not reenter Get on the same cache. Panics propagate without publication.
type Cache[K comparable, T any] struct {
	// build creates the value for an owner.
	build func(K) *T

	// current is the most recently built value.
	current atomic.Pointer[entry[K, T]]

	// construction serializes callbacks independently of any caller's virtual
	// clock. Unlike channels and condition waits, mutex waits can cross synctest
	// bubbles without associating shared cache state with one caller's bubble.
	construction sync.Mutex
}

// entry pairs a built value with the owner it was built for.
type entry[K comparable, T any] struct {
	// owner is the identity the value was built for.
	owner K

	// value is the built value.
	value *T
}

// NewCache returns an empty cache using build for each owner transition. Only
// the latest value is retained; switching back to an earlier owner rebuilds it.
func NewCache[K comparable, T any](build func(K) *T) Cache[K, T] {
	return Cache[K, T]{build: build}
}

// Get returns the latest value for owner, building it on a cache miss.
func (cache *Cache[K, T]) Get(owner K) *T {
	if entry := cache.current.Load(); entry != nil && entry.owner == owner {
		return entry.value
	}

	cache.construction.Lock()
	defer cache.construction.Unlock()

	if entry := cache.current.Load(); entry != nil && entry.owner == owner {
		return entry.value
	}

	entry := &entry[K, T]{
		owner: owner,
		value: cache.build(owner),
	}
	cache.current.Store(entry)

	return entry.value
}
