package cache

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

var (
	// ErrNotFound is returned when a key does not exist in the cache.
	// Expired entries are also missing; callers can compute a replacement.
	//
	// Example: _ = errors.Is(err, cache.ErrNotFound)
	ErrNotFound = errors.New("cache key not found")

	// ErrUnsupported is returned when an operation is unsupported.
	// A caller must not emulate an unsupported atomic operation with unsafe I/O.
	//
	// Example: _ = errors.Is(err, cache.ErrUnsupported)
	ErrUnsupported = errors.New("cache unsupported operation")

	// ErrInvalidTTL is returned when a TTL is negative, or not positive for
	// operations that require an expiry.
	//
	// Example: _ = errors.Is(err, cache.ErrInvalidTTL)
	ErrInvalidTTL = errors.New("cache invalid ttl")

	// ErrInvalidKey identifies an invalid typed key or namespace definition.
	// It never includes the supplied identity; inspect definitions with Err.
	//
	//	if errors.Is(key.Err(), cache.ErrInvalidKey) {
	//		// Correct the declaration before using the key.
	//	}
	ErrInvalidKey = errors.New("cache invalid key")

	// ErrEmptyLockName is returned when a lock name is empty.
	// Ordinary raw cache entries can still have an explicit empty identity.
	//
	// Example: _ = errors.Is(err, cache.ErrEmptyLockName)
	ErrEmptyLockName = errors.New("cache lock name cannot be empty")

	// ErrLockLost is returned by [Lease.Unlock] and [Lease.Extend] when the
	// lease no longer holds the lock: it expired, and possibly another holder
	// took it, before the call. The work done under the lease may have
	// overlapped with another holder's.
	//
	// Example: lost := errors.Is(err, cache.ErrLockLost)
	ErrLockLost = errors.New("cache lock lost")
)

// Driver defines the raw-byte operations a cache implementation provides.
// A zero TTL means an entry does not expire; a negative TTL is invalid and
// fails with an error wrapping ErrInvalidTTL. Expired entries behave exactly
// like missing ones. Values handed to or returned from a driver are
// independent copies: callers may mutate them freely.
// Every operation fails with an error wrapping the context's error when ctx is
// already canceled, and then leaves the cache unchanged.
//
// Example:
//
//	var driver cache.Driver = memorycache.New()
//	store := cache.New(driver)
//	if err := store.Put(ctx, cache.NewKey[string]("version"), "v1"); err != nil {
//		return err
//	}
type Driver interface {
	// Get returns the raw value stored for key, or an error wrapping
	// ErrNotFound when key is missing or expired.
	//
	// Example:
	//
	//	raw, err := driver.Get(ctx, "document")
	//	if err != nil {
	//		return err
	//	}
	//	_ = raw
	Get(ctx context.Context, key string) ([]byte, error)

	// Put stores value for key with ttl, replacing any existing value and TTL.
	//
	// Example:
	//
	//	if err := driver.Put(ctx, "document", []byte("draft"), time.Minute); err != nil {
	//		return err
	//	}
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// Delete removes the value stored for key. Deleting a missing key succeeds.
	//
	// Example:
	//
	//	if err := driver.Delete(ctx, "document"); err != nil {
	//		return err
	//	}
	Delete(ctx context.Context, key string) error

	// Has reports whether key exists in the cache.
	// It is an observation, not a guard for a later write.
	//
	// Example:
	//
	//	found, err := driver.Has(ctx, "document")
	//	if err != nil {
	//		return err
	//	}
	//	_ = found
	Has(ctx context.Context, key string) (bool, error)

	// Add stores value only when key is absent or expired. It reports whether
	// the value was stored and leaves an existing entry and its TTL untouched.
	//
	// Example:
	//
	//	added, err := driver.Add(ctx, "receipt", []byte("claimed"), time.Hour)
	//	if err != nil {
	//		return err
	//	}
	//	_ = added
	Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)

	// CompareAndPut atomically checks match and writes entry. See [Match] and
	// [Entry]: the guard must exist, be unexpired, and equal match.Value byte
	// for byte. A mismatch returns false, nil without changing either entry
	// or its TTL. A match replaces entry.Key's value and TTL and returns true.
	// The keys may be equal; otherwise the guard and its TTL remain unchanged.
	// Negative entry.TTL fails even on a mismatch. Implementations must not
	// emulate this with separate reads and writes; unsupported atomicity
	// returns an error wrapping ErrUnsupported. Network errors can leave the
	// outcome unknown, as with Put; callers must not fall back to Put.
	//
	// Example:
	//
	//	raw, err := driver.Get(ctx, "document")
	//	if err != nil {
	//		return err
	//	}
	//	applied, err := driver.CompareAndPut(ctx,
	//		cache.Match{Key: "document", Value: raw},
	//		cache.Entry{Key: "document", Value: []byte("revised"), TTL: time.Minute})
	//	if err != nil {
	//		return err
	//	}
	//	_ = applied // false means the observed value no longer matches.
	CompareAndPut(ctx context.Context, match Match, entry Entry) (bool, error)

	// Increment increases the integer value stored for key by delta and
	// returns the new value. A missing or expired key is created from zero
	// with no expiry; an existing key keeps its remaining TTL. A stored value
	// that is not a base-10 integer fails and is left unchanged. Integers are
	// stored as their decimal text, so Get returns it.
	// A mathematical result outside int64 must fail without changing the
	// target's value or TTL; an absent target must remain absent. Overflow
	// error identities are backend-native, not a portable cache sentinel.
	//
	// Example:
	//
	//	count, err := driver.Increment(ctx, "views", 1)
	//	if err != nil {
	//		return err
	//	}
	//	_ = count
	Increment(ctx context.Context, key string, delta int64) (int64, error)

	// Decrement decreases the integer value stored for key by delta and
	// returns the new value. It follows the same rules as Increment, so a
	// missing key is created from zero and becomes -delta when representable.
	// Subtraction must be checked without overflowing negation of delta:
	// MinInt64 - MinInt64 is zero, but 0 - MinInt64 must fail. Overflow leaves
	// the target value and TTL unchanged; a missing target remains absent.
	// Its error identity remains backend-native, as with Increment.
	//
	// Example:
	//
	//	count, err := driver.Decrement(ctx, "available", 1)
	//	if err != nil {
	//		return err
	//	}
	//	_ = count
	Decrement(ctx context.Context, key string, delta int64) (int64, error)

	// Pull atomically returns the raw value stored for key and removes it, so
	// when several callers pull the same key exactly one receives the value
	// and the others get an error wrapping ErrNotFound. It also returns that
	// error when key is missing or expired.
	//
	// Example:
	//
	//	raw, err := driver.Pull(ctx, "one-time-token")
	//	if err != nil {
	//		return err
	//	}
	//	_ = raw
	Pull(ctx context.Context, key string) ([]byte, error)

	// TryLock stores token under name, expiring after ttl, only when name
	// is absent or expired. It reports whether the lock is now held by token
	// and leaves an existing holder and its expiry untouched. The token
	// identifies the holder for [Driver.Unlock] and [Driver.ExtendLock]
	// and must be unique to it and non-empty. A ttl that is not positive
	// fails with an error wrapping ErrInvalidTTL. Lock names share the key
	// space with entries, so the facade prefixes them with "lock:".
	//
	// Example:
	//
	//	token := rand.Text() // crypto/rand: unique to this attempt.
	//	held, err := driver.TryLock(ctx, "lock:report", token, time.Minute)
	//	if err != nil {
	//		return err
	//	}
	//	if held {
	//		_, err = driver.Unlock(ctx, "lock:report", token)
	//	}
	//	return err
	TryLock(ctx context.Context, name, token string, ttl time.Duration) (bool, error)

	// Unlock removes the lock name only while it still holds token, in
	// one atomic step, and reports whether it did. It reports false, without
	// error, when the lock expired or another holder took it, and then leaves
	// that holder's lock untouched.
	//
	// Example:
	//
	//	released, err := driver.Unlock(ctx, "lock:report", token)
	//	if err != nil {
	//		return err
	//	}
	//	_ = released
	Unlock(ctx context.Context, name, token string) (bool, error)

	// ExtendLock sets the expiry of the lock name to ttl from now, only while
	// it still holds token, in one atomic step, and reports whether it did.
	// It reports false, without error, when the lock expired or another
	// holder took it. A ttl that is not positive fails with an error
	// wrapping ErrInvalidTTL.
	//
	// Example:
	//
	//	extended, err := driver.ExtendLock(ctx, "lock:report", token, time.Minute)
	//	if err != nil {
	//		return err
	//	}
	//	_ = extended
	ExtendLock(ctx context.Context, name, token string, ttl time.Duration) (bool, error)

	// IncrementWithTTL atomically increases key by delta and returns the new
	// value with the remaining lifetime. A missing key is created from zero;
	// a key without an expiry receives ttl; an existing expiry is kept. A ttl
	// that is not positive fails with an error wrapping ErrInvalidTTL. The
	// key therefore has an expiry after a successful write, which makes the
	// operation suitable for rate-limit windows.
	// A mathematical result outside int64 fails without changing the target's
	// value or TTL, including leaving a persistent target without expiry.
	// An absent target remains absent; overflow errors remain backend-native.
	//
	// Example:
	//
	//	count, remaining, err := driver.IncrementWithTTL(ctx, "requests", 1, time.Minute)
	//	if err != nil {
	//		return err
	//	}
	//	_, _ = count, remaining
	IncrementWithTTL(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, time.Duration, error)

	// TTL returns the remaining lifetime for key, or zero when the entry
	// never expires. It returns an error wrapping ErrNotFound when key is
	// missing or expired.
	//
	// Example:
	//
	//	remaining, err := driver.TTL(ctx, "document")
	//	if err != nil {
	//		return err
	//	}
	//	_ = remaining
	TTL(ctx context.Context, key string) (time.Duration, error)
}

// Cache provides typed JSON caching over a driver.
// Definition validation and telemetry belong to this facade; raw Driver access
// intentionally bypasses both. Construct one with New and share immutable keys.
//
// Example:
//
//	store := cache.New(memorycache.New())
//	if err := store.Put(ctx, cache.NewKey[string]("version"), "v1"); err != nil {
//		return err
//	}
type Cache struct {
	// driver stores the raw values.
	driver Driver

	// config holds the telemetry settings.
	config settings

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[instruments]
}

// New creates a Cache that delegates storage to driver. Construction policies
// configure its telemetry. Each cache operation emits one client span and the
// arkoren.cache.operations and arkoren.cache.operation.duration metrics when
// telemetry is enabled. Disabled telemetry adds no allocations; JSON encoding
// and driver operations may still allocate. Keys and values are never recorded.
//
//	store := cache.New(driver, func(store *cache.Builder) {
//		store.Telemetry(app.Telemetry())
//		store.Name("sessions")
//	})
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
func New(driver Driver, configure ...BuilderFunc) *Cache {
	return &Cache{
		driver:      driver,
		config:      newSettings(configure...),
		instruments: telemetry.NewInstruments(newInstruments),
	}
}

// Driver returns the underlying Driver.
// Calls through it use raw bytes and do not emit facade telemetry.
//
// Example: raw := store.Driver()
func (cache *Cache) Driver() Driver {
	return cache.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() Driver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver.
//
// Example:
//
//	driver, ok := store.DriverAs[*memorycache.Driver]()
//	if ok {
//		_ = driver // Access the concrete driver's lifecycle, if owned here.
//	}
func (cache *Cache) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](cache.Driver())
}

// Get retrieves and JSON-decodes the value for a typed key. Cancellation and
// declaration errors are checked before driver I/O or decoding. A decode error
// leaves the stored entry unchanged and still counts as a read hit.
//
//	value, err := store.Get(ctx, cache.NewKey[string]("version"))
//	if err != nil {
//		return err
//	}
//	_ = value
func (cache *Cache) Get[T any](ctx context.Context, key Key[T]) (res T, err error) {
	ctx, op := cache.begin(ctx, operationGet)
	hit := hitUnknown
	defer func() { op.end(ctx, err, hit) }()

	if err = validateKey(ctx, key); err != nil {
		return res, err
	}

	raw, err := cache.driver.Get(ctx, key.Name())
	hit = readHit(err)

	if err != nil {
		return res, err
	}

	err = json.Unmarshal(raw, &res)

	return res, err
}

// store encodes value and writes it without telemetry.
func (cache *Cache) store[T any](ctx context.Context, key string, value T, ttl time.Duration) error {
	raw, err := json.Marshal(value)

	if err != nil {
		return err
	}

	return cache.driver.Put(ctx, key, raw, ttl)
}

// Put JSON-encodes value and replaces the entry using key.TTL as write policy.
// Zero TTL means no expiry. Cancellation and key errors precede encoding and I/O.
//
//	if err := store.Put(ctx, cache.NewKey[string]("version"), "v1"); err != nil {
//		return err
//	}
func (cache *Cache) Put[T any](ctx context.Context, key Key[T], value T) (err error) {
	ctx, op := cache.begin(ctx, operationPut)
	defer func() { op.end(ctx, err, hitUnknown) }()

	if err = validateKey(ctx, key); err != nil {
		return err
	}

	err = cache.store(ctx, key.Name(), value, key.TTL())

	return err
}

// Delete removes the entry at key regardless of its value type or write policy.
// A missing entry succeeds. Cancellation and declaration errors precede I/O.
//
//	if err := store.Delete(ctx, cache.NewKey[string]("version")); err != nil {
//		return err
//	}
func (cache *Cache) Delete[T any](ctx context.Context, key Key[T]) (err error) {
	ctx, op := cache.begin(ctx, operationDelete)
	defer func() { op.end(ctx, err, hitUnknown) }()

	if err = validateKey(ctx, key); err != nil {
		return err
	}

	err = cache.driver.Delete(ctx, key.Name())

	return err
}

// Add JSON-encodes value and stores it with key.TTL only when the entry is
// absent. An existing entry and its expiry remain unchanged. Validation precedes
// encoding and driver I/O; zero TTL means no expiry.
//
//	added, err := store.Add(ctx, cache.NewKey[string]("version"), "v1")
//	if err != nil {
//		return err
//	}
//	_ = added
func (cache *Cache) Add[T any](ctx context.Context, key Key[T], value T) (added bool, err error) {
	ctx, op := cache.begin(ctx, operationAdd)
	defer func() { op.end(ctx, err, hitUnknown) }()

	if err = validateKey(ctx, key); err != nil {
		return false, err
	}

	raw, err := json.Marshal(value)

	if err != nil {
		return false, err
	}

	added, err = cache.driver.Add(ctx, key.Name(), raw, key.TTL())

	return added, err
}

// Has reports whether key exists and has not expired, without decoding its
// value. Its write policy has no effect. Failed reads have an unknown hit state.
//
//	found, err := store.Has(ctx, cache.NewKey[string]("version"))
//	if err != nil {
//		return err
//	}
//	_ = found
func (cache *Cache) Has[T any](ctx context.Context, key Key[T]) (found bool, err error) {
	ctx, op := cache.begin(ctx, operationHas)
	hit := hitUnknown
	defer func() { op.end(ctx, err, hit) }()

	if err = validateKey(ctx, key); err != nil {
		return false, err
	}

	found, err = cache.driver.Has(ctx, key.Name())

	if err == nil {
		hit = boolHit(found)
	}

	return found, err
}

// Pull retrieves, removes, and JSON-decodes the value for key. The read and
// the removal are one atomic driver operation, so when several callers pull
// the same key exactly one receives the value, which makes Pull safe for
// one-time tokens. The value is removed even when it no longer decodes as T.
// Cancellation and declaration errors precede the destructive driver read.
//
//	value, err := store.Pull(ctx, cache.NewKey[string]("one-time-token"))
//	if err != nil {
//		return err
//	}
//	_ = value
func (cache *Cache) Pull[T any](ctx context.Context, key Key[T]) (res T, err error) {
	ctx, op := cache.begin(ctx, operationPull)
	hit := hitUnknown
	defer func() { op.end(ctx, err, hit) }()

	if err = validateKey(ctx, key); err != nil {
		return res, err
	}

	raw, err := cache.driver.Pull(ctx, key.Name())
	hit = readHit(err)

	if err != nil {
		return res, err
	}

	err = json.Unmarshal(raw, &res)

	return res, err
}

// TTL returns the stored entry's remaining lifetime, not key.TTL. Zero means
// the entry never expires; missing entries return ErrNotFound.
//
//	remaining, err := store.TTL(ctx, cache.NewKey[string]("version"))
//	if err != nil {
//		return err
//	}
//	_ = remaining
func (cache *Cache) TTL[T any](ctx context.Context, key Key[T]) (remaining time.Duration, err error) {
	ctx, op := cache.begin(ctx, operationTTL)
	hit := hitUnknown
	defer func() { op.end(ctx, err, hit) }()

	if err = validateKey(ctx, key); err != nil {
		return 0, err
	}

	remaining, err = cache.driver.TTL(ctx, key.Name())
	hit = readHit(err)

	return remaining, err
}
