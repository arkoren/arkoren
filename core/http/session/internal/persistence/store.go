// Package persistence owns session records, guarded writes, and retirement.
// Cookies, events, and runtime session provenance remain with the caller.
package persistence

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/core/service/cache"
)

// Store persists session records in a cache driver.
type Store struct {
	// driver holds the session records.
	Driver cache.Driver

	// locks reduces conflicts between saves and tombstones. Atomic guarded
	// writes, not these expiring leases, enforce retirement correctness.
	Locks *cache.Cache

	// space names JSON records without changing the asymmetric session codec.
	Space cache.Namespace[jsontext.Value]
}

// lockTTL bounds best-effort save coordination after a holder crashes.
// A slow holder can outlive it; CompareAndPut still fences retired IDs.
const lockTTL = 10 * time.Second

// lockPrefix namespaces per-session lock names.
const lockPrefix = "session:"

// Lock waits until it holds the lock of session id or ctx ends.
func (store Store) Lock(ctx context.Context, id string) (*cache.Lease, error) {
	lease, err := store.Locks.Lock(ctx, lockPrefix+id, lockTTL)

	if err != nil {
		return nil, fmt.Errorf("lock session: %w", err)
	}

	return lease, nil
}

// Unlock attempts release independently of ctx's cancellation, but for at most
// one lockTTL interval. A cooperative backend therefore cannot hold response
// commitment indefinitely after persistence. Errors remain best effort because
// the lease expires by itself; the cleanup context retains request trace values.
func (store Store) Unlock(ctx context.Context, lease *cache.Lease) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), lockTTL)
	defer cancel()
	// Expiry is the fallback if release fails or its cleanup deadline elapses.
	_ = lease.Unlock(cleanup)
}

// Record is the stored form of a session.
type Record struct {
	// ID is the session identifier associated with this stored record.
	ID string `json:"id"`
	// CreatedAt anchors maximum-lifetime checks across session renewals.
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is the current idle-expiration deadline persisted with the
	// session.
	ExpiresAt time.Time `json:"expires_at"`
	// Storage contains session values; the decoding view retains raw JSON until
	// typed lookup.
	Storage map[string]any `json:"storage"`

	// Tombstone marks an ID that was retired by logout, destroy, or
	// regeneration. Records written before tombstones existed lack the field
	// and load as live sessions.
	Tombstone bool `json:"tombstone,omitempty"`
}

// Get loads the session stored under id. found is false when the cache has
// no record or only a tombstone, which starts a fresh session; other errors
// are storage failures.
func (store Store) Get(ctx context.Context, id string) (value Record, found bool, err error) {
	raw, err := store.Driver.Get(ctx, store.Space.Key(id).Name())

	if errors.Is(err, cache.ErrNotFound) {
		return Record{}, false, nil
	}

	if err != nil {
		return Record{}, false, fmt.Errorf("load session: %w", err)
	}

	value, found = Decode(raw)

	return value, found, nil
}

// Decode applies the session codec to one snapshot. Missing, malformed,
// and tombstoned records are unusable. Live values retain raw field JSON until
// typed lookup, preserving the existing persisted representation and precision.
func Decode(raw []byte) (Record, bool) {
	var stored struct {
		// Record supplies session metadata while the explicit Storage field captures
		// undecoded JSON values.
		Record
		// Storage contains session values; the decoding view retains raw JSON until
		// typed lookup.
		Storage map[string]jsontext.Value `json:"storage"`
	}

	if err := json.Unmarshal(raw, &stored); err != nil {
		// An unreadable record is treated as missing, like an unknown ID.
		return Record{}, false
	}

	if stored.Tombstone {
		return Record{}, false
	}

	storage := make(map[string]any, len(stored.Storage))

	for key, value := range stored.Storage {
		storage[key] = value
	}

	stored.Record.Storage = storage

	return stored.Record, true
}

// Save stores value under its current ID for ttl. A loaded session requires
// the exact original-record match obtained during verification; checking that
// record and writing the current ID are one atomic operation, even after
// regeneration. A stale match returns false without writing. Fresh sessions
// ignore match and use Put because no earlier record authorizes their creation.
// A nonpositive remaining lifetime is discarded rather than passing cache's
// zero-TTL/no-expiry policy through to a session record.
func (store Store) Save(ctx context.Context, value Record, loaded bool, ttl time.Duration, match cache.Match) (bool, error) {
	if ttl <= 0 {
		return false, nil
	}

	raw, err := json.Marshal(value)

	if err != nil {
		return false, fmt.Errorf("encode session: %w", err)
	}

	key := store.Space.Key(value.ID, func(entry *cache.KeyBuilder) {
		entry.TTL(ttl)
	})

	if loaded {
		return store.Driver.CompareAndPut(ctx, match, cache.Entry{Key: key.Name(), Value: raw, TTL: key.TTL()})
	}

	err = store.Driver.Put(ctx, key.Name(), raw, key.TTL())

	return err == nil, err
}

// Delete removes the session stored under id.
func (store Store) Delete(ctx context.Context, id string) error {
	return store.Driver.Delete(ctx, store.Space.Key(id).Name())
}

// Match captures the exact live record that authorizes a loaded-session save.
// Missing, unreadable, or tombstoned records return false; backend failures are
// verification errors. The returned bytes come from the driver's owned copy
// and are passed unchanged to CompareAndPut, never decoded and re-encoded.
func (store Store) Match(ctx context.Context, id string) (cache.Match, bool, error) {
	key := store.Space.Key(id).Name()

	raw, err := store.Driver.Get(ctx, key)

	if errors.Is(err, cache.ErrNotFound) {
		return cache.Match{}, false, nil
	}

	if err != nil {
		return cache.Match{}, false, fmt.Errorf("verify session: %w", err)
	}

	_, found := Decode(raw)

	return cache.Match{Key: key, Value: raw}, found, nil
}

// Tombstone retires id by overwriting its record with a tombstone for ttl, so
// a request that loaded the session earlier cannot save it back to life. ttl
// retains the marker for the configured absolute lifetime (idle TTL when that
// maximum is disabled). CompareAndPut rejects both tombstoned and missing
// original records, so safety does not depend on the marker outliving a pending
// writer. The lease reduces contention, not the atomic retirement guarantee.
func (store Store) Tombstone(ctx context.Context, id string, ttl time.Duration) error {
	raw, err := json.Marshal(Record{
		ID:        id,
		Tombstone: true,
	})

	if err != nil {
		return fmt.Errorf("encode session tombstone: %w", err)
	}

	lease, err := store.Lock(ctx, id)

	if err != nil {
		return err
	}

	defer store.Unlock(ctx, lease)

	key := store.Space.Key(id, func(entry *cache.KeyBuilder) {
		entry.TTL(ttl)
	})

	return store.Driver.Put(ctx, key.Name(), raw, key.TTL())
}
