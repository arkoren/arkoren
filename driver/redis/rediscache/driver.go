package rediscache

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"time"

	"arkoren.dev/core/service/cache"

	"github.com/redis/go-redis/v9"
)

// Config is a diagnostic Redis connection record. Its formatting and slog
// methods redact Password. It is not a constructor input: use Builder settings
// to configure New. Native TLS values remain shared rather than deep-cloned.
//
// Example:
//
//	record := rediscache.Config{
//		Addr:     "localhost:6379",
//		Password: "example",
//	}
//	slog.Info("Redis configuration", "config", record)
type Config struct {
	// Network is the network type passed to the Redis client.
	// New defaults to "tcp"; "unix" selects a Unix-domain socket.
	//
	// Example:
	//
	//	record := rediscache.Config{Network: "tcp"}
	//	_ = record.Network
	Network string

	// Addr is the Redis server address. New defaults to "localhost:6379";
	// for Network "unix", supply the socket's filesystem path instead.
	//
	// Example:
	//
	//	record := rediscache.Config{Addr: "cache.internal:6379"}
	//	_ = record.Addr
	Addr string

	// Username is the optional ACL username; New defaults to an empty username.
	//
	// Example:
	//
	//	record := rediscache.Config{Username: "billing"}
	//	_ = record.Username
	Username string

	// Password is the optional ACL password. Empty sends no password. Formatting
	// the complete record redacts it; reading this field returns the secret.
	//
	// Example:
	//
	//	record := rediscache.Config{Password: "example"}
	//	_ = record.String() // Contains [REDACTED], not the password.
	Password string

	// DB is the Redis logical database number. New defaults to database zero.
	//
	// Example:
	//
	//	record := rediscache.Config{DB: 1}
	//	_ = record.DB
	DB int

	// TLS enables TLS for the connection when non-nil, for example a
	// *tls.Config with MinVersion and RootCAs set. The default, nil, connects
	// without TLS. [New] passes it to the client as is; [NewFrom] ignores it
	// because the supplied client carries its own.
	//
	// Example:
	//
	//	record := rediscache.Config{TLS: &tls.Config{MinVersion: tls.VersionTLS12}}
	//	_ = record.TLS.MinVersion
	TLS *tls.Config
}

// Driver implements [cache.Driver] using a standalone Redis client. Create it
// with New or transfer a native client through NewFrom; Stop closes either one.
// Returned byte values are detached from Redis. Native Client calls bypass the
// cache facade and its telemetry, but use the same owned connection pool.
//
// Example:
//
//	driver := rediscache.New(func(store *rediscache.Builder) {
//		store.Addr("cache.internal:6379")
//	})
//	store := cache.New(driver)
//	_ = store.Driver()
type Driver redis.Client

var _ cache.Driver = (*Driver)(nil)

// Client returns the exact standalone native client used by this driver. Native
// operations bypass facade telemetry and share the driver's owned pool; Stop
// closes it even when the client was supplied through NewFrom. Configure native
// settings before transferring ownership rather than mutating Options in place.
//
// Example:
//
//	driver := rediscache.New()
//	stats := driver.Client().PoolStats()
//	_ = stats.TotalConns
func (driver *Driver) Client() *redis.Client {
	return (*redis.Client)(driver)
}

// defaultConfig returns the default Redis cache configuration.
func defaultConfig() Config {
	return Config{
		Network: "tcp",
		Addr:    "localhost:6379",
	}
}

// New creates a Driver without network I/O. Construction callbacks run once,
// in order on one fresh draft; nil entries panic. Empty connection settings use
// TCP and localhost:6379 are the connection defaults; database zero, empty ACL
// credentials, and no TLS are selected unless configured. TLS configurations
// remain shared native values. The created client honors operation deadlines
// during socket I/O; ordinary native retry policy is unchanged. Guarded writes
// retain CompareAndPut's no-uncertain-replay policy.
//
// Example:
//
//	driver := rediscache.New(func(store *rediscache.Builder) {
//		store.Addr("localhost:6379")
//	})
//	_ = driver.Client()
func New(configure ...BuilderFunc) *Driver {
	config := configureDriver(configure...).withDefaults()

	return NewFrom(redis.NewClient(&redis.Options{
		Network:               config.Network,
		Addr:                  config.Addr,
		Username:              config.Username,
		Password:              config.Password,
		DB:                    config.DB,
		TLSConfig:             config.TLS,
		ContextTimeoutEnabled: true,
	}))
}

// withDefaults fills connection settings omitted from Config.
func (config Config) withDefaults() Config {
	defaults := defaultConfig()

	if config.Network == "" {
		config.Network = defaults.Network
	}

	if config.Addr == "" {
		config.Addr = defaults.Addr
	}

	return config
}

// NewFrom transfers ownership of a preconfigured standalone redis.Client to the
// driver. Stop closes that exact client, including its native connection pool;
// do not share it with code that must outlive the driver. No settings or hooks
// are changed and no connection is made by this conversion. Configure native
// ContextTimeoutEnabled before construction to honor operation deadlines during
// socket I/O. The client must be non-nil and initialized with redis.NewClient.
//
// Example:
//
//	client := redis.NewClient(&redis.Options{
//		Addr: "cache.internal:6379", ContextTimeoutEnabled: true,
//	})
//	driver := rediscache.NewFrom(client)
//	return driver.Stop(ctx) // Also closes client.
func NewFrom(client *redis.Client) *Driver {
	return (*Driver)(client)
}

// Get retrieves raw bytes by key. Returns [cache.ErrNotFound] when the
// key does not exist or has expired. Native transport errors are preserved.
//
// Example:
//
//	value, err := driver.Get(ctx, "version")
//	if err != nil {
//		return err
//	}
//	_ = string(value)
func (driver *Driver) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := (*redis.Client)(driver).Get(ctx, key).Bytes()

	if errors.Is(err, redis.Nil) {
		return nil, cache.ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	return value, nil
}

// validateTTL rejects negative TTLs, which go-redis would otherwise map to
// KEEPTTL or no expiry.
func validateTTL(ttl time.Duration) error {
	if ttl < 0 {
		return fmt.Errorf("redis: ttl %s: %w", ttl, cache.ErrInvalidTTL)
	}

	return nil
}

// Put stores raw bytes with the given TTL. A zero TTL means the key will not
// expire. A negative TTL returns [cache.ErrInvalidTTL]; an already-canceled
// context takes precedence and leaves the value and expiry unchanged.
//
// Example:
//
//	if err := driver.Put(ctx, "version", []byte("v2"), time.Minute); err != nil {
//		return err
//	}
func (driver *Driver) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := validateTTL(ttl); err != nil {
		return err
	}

	return (*redis.Client)(driver).Set(ctx, key, value, ttl).Err()
}

// Delete removes a key from Redis. Deleting a non-existent key is a no-op.
// Native failures are returned without changing their error identities.
//
// Example:
//
//	if err := driver.Delete(ctx, "version"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, key string) error {
	return (*redis.Client)(driver).Del(ctx, key).Err()
}

// Has reports whether the key exists in Redis and has not expired.
// The value and remaining expiry are left unchanged.
//
// Example:
//
//	found, err := driver.Has(ctx, "version")
//	if err != nil {
//		return err
//	}
//	_ = found
func (driver *Driver) Has(ctx context.Context, key string) (bool, error) {
	count, err := (*redis.Client)(driver).Exists(ctx, key).Result()

	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// Add stores raw bytes only if the key is absent or expired. False leaves an
// existing entry unchanged. TTL follows Put; cancellation precedes validation.
//
// Example:
//
//	added, err := driver.Add(ctx, "version", []byte("v1"), time.Minute)
//	if err != nil {
//		return err
//	}
//	_ = added
func (driver *Driver) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	if err := validateTTL(ttl); err != nil {
		return false, err
	}

	return (*redis.Client)(driver).SetNX(ctx, key, value, ttl).Result()
}

// Increment atomically increases the integer value stored at key by the given
// amount. Redis auto-creates the key with value 0 if it does not exist.
// Existing expiry is retained; noninteger values and int64 overflow fail.
//
// Example:
//
//	count, err := driver.Increment(ctx, "visits", 1)
//	if err != nil {
//		return err
//	}
//	_ = count
func (driver *Driver) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	return (*redis.Client)(driver).IncrBy(ctx, key, delta).Result()
}

// Pull atomically reads and deletes key with GETDEL, so concurrent callers
// cannot both receive the value. It returns [cache.ErrNotFound] when the key
// does not exist. GETDEL needs Redis 6.2 or later.
//
// Example:
//
//	value, err := driver.Pull(ctx, "one-time-token")
//	if err != nil {
//		return err
//	}
//	_ = value
func (driver *Driver) Pull(ctx context.Context, key string) ([]byte, error) {
	value, err := (*redis.Client)(driver).GetDel(ctx, key).Bytes()

	if errors.Is(err, redis.Nil) {
		return nil, cache.ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	return value, nil
}

// TryLock stores token under name with SET NX PX, so exactly one caller
// wins an absent or expired lock. It returns an error wrapping
// [cache.ErrInvalidTTL] when ttl is not positive. An already-canceled context
// takes precedence. Keep token unique per holder and release with Unlock.
//
// Example:
//
//	acquired, err := driver.TryLock(ctx, "report", token, time.Minute)
//	if err != nil {
//		return err
//	}
//	_ = acquired
func (driver *Driver) TryLock(ctx context.Context, name, token string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	if ttl <= 0 {
		return false, fmt.Errorf("redis: lock ttl %s: %w", ttl, cache.ErrInvalidTTL)
	}

	return (*redis.Client)(driver).SetNX(ctx, name, token, ttl).Result()
}

// unlockScript deletes KEYS[1] only while it holds the token ARGV[1].
// Redis runs the script atomically, so the comparison and the deletion cannot
// interleave with another client's acquire.
const unlockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

// extendLockScript resets the expiry of KEYS[1] to ARGV[2] milliseconds only
// while it holds the token ARGV[1].
const extendLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`

// Unlock deletes name only while it holds token, with a Lua script that
// compares and deletes atomically.
// False means the key no longer holds token and leaves the current holder alone.
//
// Example:
//
//	released, err := driver.Unlock(ctx, "report", token)
//	if err != nil {
//		return err
//	}
//	_ = released
func (driver *Driver) Unlock(ctx context.Context, name, token string) (bool, error) {
	deleted, err := (*redis.Client)(driver).Eval(ctx, unlockScript, []string{name}, token).Int64()

	if err != nil {
		return false, err
	}

	return deleted == 1, nil
}

// ExtendLock resets the expiry of name to ttl only while it holds token, with
// a Lua script that compares and updates atomically. Redis expiries have
// millisecond precision, so a ttl under one millisecond rounds up to one. It
// returns an error wrapping [cache.ErrInvalidTTL] when ttl is not positive.
// An already-canceled context takes precedence and does not submit the script.
//
// Example:
//
//	extended, err := driver.ExtendLock(ctx, "report", token, time.Minute)
//	if err != nil {
//		return err
//	}
//	_ = extended
func (driver *Driver) ExtendLock(ctx context.Context, name, token string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	if ttl <= 0 {
		return false, fmt.Errorf("redis: lock ttl %s: %w", ttl, cache.ErrInvalidTTL)
	}

	updated, err := (*redis.Client)(driver).Eval(ctx, extendLockScript, []string{name}, token, max(ttl.Milliseconds(), 1)).Int64()

	if err != nil {
		return false, err
	}

	return updated == 1, nil
}

// Decrement atomically decreases the integer value stored at key by the given
// amount. Redis auto-creates the key with value 0 if it does not exist.
// Existing expiry is retained; noninteger values and mathematical int64 overflow
// fail without changing the target or its TTL. Subtracting MinInt64 uses one
// atomic script because native DECRBY rejects that argument even for valid
// results; the script returns an exact decimal value and preserves native errors.
//
// Example:
//
//	count, err := driver.Decrement(ctx, "stock", 1)
//	if err != nil {
//		return err
//	}
//	_ = count
func (driver *Driver) Decrement(ctx context.Context, key string, delta int64) (int64, error) {
	if delta == math.MinInt64 {
		return (*redis.Client)(driver).Eval(ctx, decrementMinimumScript, []string{key}, delta).Int64()
	}

	return (*redis.Client)(driver).DecrBy(ctx, key, delta).Result()
}

// Ping verifies connectivity without changing stored entries. New-created
// clients honor operation deadlines; NewFrom preserves the native timeout policy.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	return (*redis.Client)(driver).Ping(ctx).Err()
}

// Start verifies connectivity so a misconfigured Redis fails fast. The
// client connects lazily, so [New] performs no IO.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	return driver.Ping(ctx)
}

// Stop closes the owned Redis client and its connection pool, including a client
// transferred through NewFrom. It does not wait for application work to finish;
// finish that work first. ctx is ignored, so expired contexts still release it.
//
// Example:
//
//	if err := driver.Stop(context.Background()); err != nil {
//		return err
//	}
func (driver *Driver) Stop(context.Context) error {
	return (*redis.Client)(driver).Close()
}
