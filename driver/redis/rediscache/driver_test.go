package rediscache_test

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/cachetest"
	"arkoren.dev/driver/redis/internal/redistest"
	"arkoren.dev/driver/redis/rediscache"

	"github.com/redis/go-redis/v9"
)

// TestOwnedClientHonorsPingDeadline exercises the driver's actual socket read
// against a delayed RESP reply. Virtual time keeps this independent of wall time.
func TestOwnedClientHonorsPingDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := rediscache.New(func(store *rediscache.Builder) {
			store.Addr("127.0.0.1:1")
		})
		redistest.DelayPing(t, driver.Client())
		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		if err := driver.Ping(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Ping error = %v, want context.DeadlineExceeded", err)
		}
	})
}

// TestNewFromStopClosesTransferredClient verifies ownership at the public SDK
// seam. A closed client rejects further native work without dialing its address.
func TestNewFromStopClosesTransferredClient(t *testing.T) {
	t.Parallel()

	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	driver := rediscache.NewFrom(client)

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := client.Ping(t.Context()).Err(); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("transferred client after Stop = %v, want redis.ErrClosed", err)
	}
}

// TestPutCancellationPrecedesInvalidTTL keeps validation-only failures local:
// a pre-canceled operation must report cancellation even with a negative TTL.
func TestPutCancellationPrecedesInvalidTTL(t *testing.T) {
	t.Parallel()

	driver := rediscache.New(func(store *rediscache.Builder) {
		store.Addr("127.0.0.1:1")
	})
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Put(ctx, "key", []byte("value"), -time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put error = %v, want context.Canceled", err)
	}
}

// TestAddCancellationPrecedesInvalidTTL rejects cancellation before evaluating
// the conditional write's retention policy or contacting its backend.
func TestAddCancellationPrecedesInvalidTTL(t *testing.T) {
	t.Parallel()

	driver := rediscache.New(func(store *rediscache.Builder) {
		store.Addr("127.0.0.1:1")
	})
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	added, err := driver.Add(ctx, "key", []byte("value"), -time.Second)

	if added || !errors.Is(err, context.Canceled) {
		t.Fatalf("Add = %t, %v, want false, context.Canceled", added, err)
	}
}

// TestTryLockCancellationPrecedesInvalidTTL gives cancellation the same
// precedence for lease acquisition as for ordinary cache writes.
func TestTryLockCancellationPrecedesInvalidTTL(t *testing.T) {
	t.Parallel()

	driver := rediscache.New(func(store *rediscache.Builder) {
		store.Addr("127.0.0.1:1")
	})
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	acquired, err := driver.TryLock(ctx, "lock", "token", 0)

	if acquired || !errors.Is(err, context.Canceled) {
		t.Fatalf("TryLock = %t, %v, want false, context.Canceled", acquired, err)
	}
}

// TestExtendLockCancellationPrecedesInvalidTTL keeps an expired operation from
// being misclassified as a lease-policy error before any script is submitted.
func TestExtendLockCancellationPrecedesInvalidTTL(t *testing.T) {
	t.Parallel()

	driver := rediscache.New(func(store *rediscache.Builder) {
		store.Addr("127.0.0.1:1")
	})
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	extended, err := driver.ExtendLock(ctx, "lock", "token", 0)

	if extended || !errors.Is(err, context.Canceled) {
		t.Fatalf("ExtendLock = %t, %v, want false, context.Canceled", extended, err)
	}
}

// TestCounterCancellationPrecedesInvalidTTL rejects a canceled window operation
// before validating the window, without changing exact-count or expiry policy.
func TestCounterCancellationPrecedesInvalidTTL(t *testing.T) {
	t.Parallel()

	driver := rediscache.New(func(store *rediscache.Builder) {
		store.Addr("127.0.0.1:1")
	})
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	count, remaining, err := driver.IncrementWithTTL(ctx, "counter", 1, 0)

	if count != 0 || remaining != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("IncrementWithTTL = %d, %s, %v, want zero results, context.Canceled", count, remaining, err)
	}
}

// TestRedisTypedNamespaceRawInterop verifies that literal namespace names and
// JSON string bytes remain compatible with callers of the raw driver. Each
// direction uses the same Redis entry, without a facade-specific prefix.
func TestRedisTypedNamespaceRawInterop(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)
	store := cache.New(client)
	name := redisTestKey(t) + ":literal space"
	space := cache.NewNamespace[string](name, func(namespace *cache.NamespaceBuilder) {
		namespace.Separator("/")
	})
	key := space.Key("entry.with:punctuation")
	exact := name + "/entry.with:punctuation"
	cleanupTypedRedisKeys(t, client, exact)

	if err := store.Put(ctx, key, "typed value"); err != nil {
		t.Fatalf("typed Put() error = %v", err)
	}

	raw, err := client.Get(ctx, exact)

	if err != nil {
		t.Fatalf("raw Get() error = %v", err)
	}

	if string(raw) != `"typed value"` {
		t.Fatalf("raw Get() = %q, want JSON string bytes", raw)
	}

	if err := client.Put(ctx, exact, []byte(`"raw value"`), 0); err != nil {
		t.Fatalf("raw Put() error = %v", err)
	}

	value, err := store.Get(ctx, key)

	if err != nil {
		t.Fatalf("typed Get() error = %v", err)
	}

	if value != "raw value" {
		t.Fatalf("typed Get() = %q, want raw value", value)
	}
}

// TestRedisTypedKeyTTLInheritanceAndDerive checks actual Redis expiries for
// inherited and locally overridden write policies. Minute-scale TTLs avoid
// relying on Redis's whole-second TTL rounding or waiting for expiration.
func TestRedisTypedKeyTTLInheritanceAndDerive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)
	store := cache.New(client)
	space := cache.NewNamespace[string](redisTestKey(t), func(namespace *cache.NamespaceBuilder) {
		namespace.TTL(2 * time.Minute)
	})
	inherited := space.Key("inherited")

	if inherited.Name() != space.Name()+":inherited" {
		t.Fatalf("default namespace identity = %q", inherited.Name())
	}

	original := space.Key("derived")
	derived := original.Derive(func(entry *cache.KeyBuilder) {
		entry.TTL(5 * time.Minute)
	})
	cleanupTypedRedisKeys(t, client, inherited.Name(), derived.Name())

	if err := store.Put(ctx, inherited, "inherited"); err != nil {
		t.Fatalf("inherited Put() error = %v", err)
	}

	if err := store.Put(ctx, derived, "derived"); err != nil {
		t.Fatalf("derived Put() error = %v", err)
	}

	if original.TTL() != 2*time.Minute {
		t.Fatalf("original TTL() = %s, want 2m", original.TTL())
	}

	assertTypedRedisTTL(t, client, inherited.Name(), 2*time.Minute)
	assertTypedRedisTTL(t, client, derived.Name(), 5*time.Minute)
}

// TestRedisTypedCounterRawInterop verifies that typed int64 JSON writes are
// Redis decimal counters and that raw increments decode through the facade.
// Counter initialization uses its explicit window, not the key's write TTL.
func TestRedisTypedCounterRawInterop(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)
	store := cache.New(client)
	key := cache.NewKey[int64](redisTestKey(t))
	cleanupTypedRedisKeys(t, client, key.Name())

	if err := store.Put(ctx, key, int64(7)); err != nil {
		t.Fatalf("typed Put() error = %v", err)
	}

	count, err := client.Increment(ctx, key.Name(), 3)

	if err != nil {
		t.Fatalf("raw Increment() error = %v", err)
	}

	if count != 10 {
		t.Fatalf("raw Increment() = %d, want 10", count)
	}

	value, err := store.Get(ctx, key)

	if err != nil {
		t.Fatalf("typed Get() error = %v", err)
	}

	if value != 10 {
		t.Fatalf("typed Get() = %d, want 10", value)
	}

	count, _, err = store.IncrementWithTTL(ctx, key, 2, redisCounterTTL)

	if err != nil {
		t.Fatalf("typed IncrementWithTTL() error = %v", err)
	}

	if count != 12 {
		t.Fatalf("typed IncrementWithTTL() = %d, want 12", count)
	}

	assertTypedRedisTTL(t, client, key.Name(), redisCounterTTL)
}

// cleanupTypedRedisKeys registers deletion before writes, with a context that
// survives test cancellation. The shared fixture closes the client afterwards.
func cleanupTypedRedisKeys(t *testing.T, client *rediscache.Driver, names ...string) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), redisCleanupTimeout)
		err := client.Client().Del(ctx, names...).Err()
		cancel()

		if err != nil {
			t.Errorf("delete typed Redis keys: %v", err)
		}
	})
}

// assertTypedRedisTTL checks persisted expiry with enough slack for all bounded
// test operations. The upper bound distinguishes inherited and derived policies.
func assertTypedRedisTTL(t *testing.T, client *rediscache.Driver, name string, expected time.Duration) {
	t.Helper()

	remaining, err := client.TTL(t.Context(), name)

	if err != nil {
		t.Fatalf("raw TTL() error = %v", err)
	}

	if remaining < expected-redisOperationTimeout || remaining > expected {
		t.Fatalf("raw TTL() = %s, want within %s of %s", remaining, redisOperationTimeout, expected)
	}
}

// TestDefaultRedisConfig verifies the Redis connection defaults.
func TestDefaultRedisConfig(t *testing.T) {
	t.Parallel()

	driver := rediscache.New()
	t.Cleanup(func() {
		_ = driver.Client().Close()
	})

	config := driver.Client().Options()

	if config.Network != "tcp" {
		t.Errorf("Network = %q, want %q", config.Network, "tcp")
	}

	if config.Addr != "localhost:6379" {
		t.Errorf("Addr = %q, want %q", config.Addr, "localhost:6379")
	}
}

// TestNewRedisUsesDefaults verifies an empty configuration supplies connection defaults.
func TestNewRedisUsesDefaults(t *testing.T) {
	t.Parallel()

	client := rediscache.New()

	if client == nil {
		t.Fatal("New returned nil")
	}

	t.Cleanup(func() {
		if err := (*redis.Client)(client).Close(); err != nil {
			t.Errorf("close Redis client: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), redisOperationTimeout)
	t.Cleanup(cancel)

	_, _, err := client.IncrementWithTTL(ctx, "key", 1, 0)

	if !errors.Is(err, cache.ErrInvalidTTL) {
		t.Errorf("IncrementWithTTL error = %v, want ErrInvalidTTL", err)
	}
}

const (
	// redisOperationTimeout bounds connection and cache operations in tests.
	redisOperationTimeout = 10 * time.Second
	// redisCleanupTimeout bounds cleanup independently of the test context.
	redisCleanupTimeout = 10 * time.Second
	// redisCounterTTL gives counter expiry checks a stable positive lifetime.
	redisCounterTTL = time.Minute
	// redisCounterIncrement is the delta applied by the counter integration test.
	redisCounterIncrement = int64(3)
	// redisExpectedCacheData is the payload shared by cache round-trip checks.
	redisExpectedCacheData = "redis-cache-value"

	// redisTestKeyAllowedCharacters contains the characters permitted in test keys.
	redisTestKeyAllowedCharacters = "0123456789abcdefghijklmnopqrstuvwxyz"
)

// redisTestEndpoint selects infrastructure only for integration clients, without
// changing production defaults or dialing Redis. A nonempty
// ARKOREN_REDIS_TEST_ADDR is used verbatim; unset or empty preserves localhost:6379.
func redisTestEndpoint() string {
	return cmp.Or(os.Getenv("ARKOREN_REDIS_TEST_ADDR"), "localhost:6379")
}

// TestRedisPing verifies the driver reaches the shared Redis server.
func TestRedisPing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)

	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

// TestRedisPutGet verifies Redis byte-value round trips.
func TestRedisPutGet(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)
	key := redisTestKey(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), redisCleanupTimeout)
		err := client.Delete(cleanupCtx, key)
		cleanupCancel()

		if err != nil {
			t.Errorf("delete Redis key %q: %v", key, err)
		}
	})

	if err := client.Put(ctx, key, []byte(redisExpectedCacheData), redisCounterTTL); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	value, err := client.Get(ctx, key)

	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if string(value) != redisExpectedCacheData {
		t.Errorf("Get() = %q, want %q", value, redisExpectedCacheData)
	}
}

// TestRedisIncrementWithTTL verifies atomic counter creation and expiration.
func TestRedisIncrementWithTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)
	key := redisTestKey(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), redisCleanupTimeout)
		err := client.Delete(cleanupCtx, key)
		cleanupCancel()

		if err != nil {
			t.Errorf("delete Redis key %q: %v", key, err)
		}
	})

	count, ttl, err := client.IncrementWithTTL(ctx, key, redisCounterIncrement, redisCounterTTL)

	if err != nil {
		t.Fatalf("IncrementWithTTL() error = %v", err)
	}

	if count != redisCounterIncrement {
		t.Errorf("IncrementWithTTL() count = %d, want %d", count, redisCounterIncrement)
	}

	if !validRedisCounterTTL(ttl) {
		t.Errorf("IncrementWithTTL() TTL = %s, want duration in (0, %s]", ttl, redisCounterTTL)
	}
}

// TestRedisDelete verifies deletion removes a Redis cache value.
func TestRedisDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)
	key := redisTestKey(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), redisCleanupTimeout)
		err := client.Delete(cleanupCtx, key)
		cleanupCancel()

		if err != nil {
			t.Errorf("delete Redis key %q: %v", key, err)
		}
	})

	putRedisCacheValue(t, client, ctx, key)

	deleteRedisCacheValue(t, client, ctx, key)

	assertRedisKeyMissing(t, client, ctx, key)
}

// newRedisDriver creates a driver and bounded context for one Redis test.
// Its address comes from redisTestEndpoint, allowing integration infrastructure
// to move without changing production defaults or the pure configuration tests.
func newRedisDriver(t *testing.T) (*rediscache.Driver, context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), redisOperationTimeout)
	t.Cleanup(cancel)

	client := rediscache.New(func(store *rediscache.Builder) {
		store.Addr(redisTestEndpoint())
	})
	redisClient := (*redis.Client)(client)
	t.Cleanup(func() {
		if err := redisClient.Close(); err != nil {
			t.Errorf("close Redis client: %v", err)
		}
	})

	return client, ctx
}

// validRedisCounterTTL reports whether ttl is in the expected range.
func validRedisCounterTTL(ttl time.Duration) bool {
	return ttl > 0 && ttl <= redisCounterTTL
}

// putRedisCacheValue stores the expected cache data under key.
func putRedisCacheValue(t *testing.T, client *rediscache.Driver, ctx context.Context, key string) {
	t.Helper()

	if err := client.Put(ctx, key, []byte(redisExpectedCacheData), redisCounterTTL); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
}

// deleteRedisCacheValue removes key and fails the test when deletion fails.
func deleteRedisCacheValue(t *testing.T, client *rediscache.Driver, ctx context.Context, key string) {
	t.Helper()

	if err := client.Delete(ctx, key); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

// assertRedisKeyMissing verifies that key no longer exists.
func assertRedisKeyMissing(t *testing.T, client *rediscache.Driver, ctx context.Context, key string) {
	t.Helper()

	found, err := client.Has(ctx, key)

	if err != nil {
		t.Fatalf("Has() error = %v", err)
	}

	if found {
		t.Error("Has() = true after Delete(), want false")
	}
}

// redisTestKey returns an isolated Redis key for one test.
func redisTestKey(t *testing.T) string {
	t.Helper()

	random := make([]byte, 8)

	if _, err := rand.Read(random); err != nil {
		t.Fatalf("generate Redis key suffix: %v", err)
	}

	name := strings.Map(redisTestKeyCharacter, strings.ToLower(t.Name()))

	return "arkoren-" + name + "-" + hex.EncodeToString(random)
}

// redisTestKeyCharacter converts unsupported characters to hyphens.
func redisTestKeyCharacter(character rune) rune {
	if strings.ContainsRune(redisTestKeyAllowedCharacters, character) {
		return character
	}

	return '-'
}

// TestRedisConformance runs the cache conformance suite against Redis.
func TestRedisConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	cachetest.Run(t, func(t *testing.T) cache.Driver {
		client, _ := newRedisDriver(t)

		return client
	}, cachetest.WithExternalClock())
}
