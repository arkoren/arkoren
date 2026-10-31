package storage

import (
	"bytes"
	"context"
	"time"

	"arkoren.dev/core/service/cache"
)

// CompareAndPut compares the existing guard and writes entry under one mutex.
// Missing, expired, or unequal guards return false without changing logical
// values or lifetimes. A matching guard permits a copied replacement with the
// requested TTL; a different guard key retains its content and expiry.
// Cancellation is checked both before waiting and after acquiring the mutex.
//
// Example:
//
//	driver := memorycache.New()
//	if err := driver.Put(ctx, "document", []byte("draft"), time.Minute); err != nil {
//		return err
//	}
//	applied, err := driver.CompareAndPut(ctx,
//		cache.Match{Key: "document", Value: []byte("draft")},
//		cache.Entry{Key: "document", Value: []byte("revised"), TTL: time.Minute})
//	if err != nil {
//		return err
//	}
//	_ = applied
func (driver *Store) CompareAndPut(ctx context.Context, match cache.Match, entry cache.Entry) (bool, error) {
	if err := validate(ctx, entry.TTL); err != nil {
		return false, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	if err := ctx.Err(); err != nil {
		return false, err
	}

	now := time.Now()
	stored, found := driver.live(match.Key, now)

	if !found || !bytes.Equal(stored.value, match.Value) {
		return false, nil
	}

	driver.sweepDue(now)

	driver.Entries[entry.Key] = newEntry(entry.Value, entry.TTL, now)

	return true, nil
}
