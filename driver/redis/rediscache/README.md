# Arkoren Cache Redis Driver

`arkoren.dev/driver/redis/rediscache` implements `cache.Driver` using `github.com/redis/go-redis/v9`. It requires Redis 6.2 or later, because `Pull` uses `GETDEL`; cache locks use `SET NX PX` and Lua compare-and-delete/expire scripts.

`CompareAndPut` checks an existing guard's exact bytes and writes its destination
in one Lua execution, including when the keys differ. Positive write lifetimes
round up to milliseconds. Possibly executed guarded writes are not automatically
retried after a lost response; an error can mean the outcome is unknown. This
does not add Redis Cluster support or a persistence guarantee.

## Using the driver

Run `go get arkoren.dev/driver/redis` in your application's Go module. With a
Redis service available, construct and register the driver on your existing
`app`:

```go
import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/driver/redis/rediscache"
)

err := app.Run(ctx, func(compose *application.Composer) {
	driver := rediscache.New(func(store *rediscache.Builder) {
		store.Addr("127.0.0.1:6379")
	})
	compose.Cleanup(driver.Stop)
	compose.Startup(driver.Start)
	compose.Check("cache", driver.Ping)
	store := cache.New(driver)
	// Pass store to your application services here.
	_ = store
})
```

Startup verifies connectivity; shutdown closes the client. Use the
[cache write and read task](../../../cmd/arkoren/skills/arkoren/references/guides/cache/overview.md#storing-and-reading-values)
to verify the facade. Configure TLS and credentials for a remote service. The
[Redis guide](../../../cmd/arkoren/skills/arkoren/references/guides/cache/drivers.md#redis)
includes disposable local setup, native commands, and limits.

`New` accepts `func(store *rediscache.Builder)` callbacks. They run once in order
on a fresh draft; nil entries panic and expanded nil slices omit edits. Native
TLS objects remain shared. `NewFrom(client)` remains a settings-free native
conversion; `Stop` closes the supplied client, so do not share it with code
that must outlive the driver. The redacted
`Config` record is diagnostic data, not a constructor input.

By default, `New` uses TCP at `localhost:6379`, database zero, no ACL credentials,
and no TLS. Its native client honors operation deadlines during socket I/O.
`NewFrom` preserves native settings: set `redis.Options.ContextTimeoutEnabled`
before creating the client if deadlines must bound socket reads and writes.
This does not make a canceled write reversible; a lost response can leave its
outcome unknown.

`TTL` returns millisecond-resolution remaining lifetimes, so subsecond entries
are not reported as persistent. `IncrementWithTTL` returns exact int64 counts
and rounds positive windows up to whole milliseconds, including submillisecond
inputs. Existing expiries remain unchanged. Pre-canceled operations report the
context error before invalid TTL errors and submit no command.

Counter results outside the mathematical int64 range are errors and leave the
value and TTL unchanged. An overflowing absent target remains absent. Native
`DECRBY` rejects `MinInt64` even for a valid result, so the driver handles that
one argument in an atomic script using exact native integer operations. For
example, `MinInt64 - MinInt64` produces zero without resetting an existing expiry.

Cache namespaces join names in the facade, not Redis, and default to `":"`.
Keep deployed dot joins with `NamespaceBuilder.Separator(".")` when upgrading;
standalone key bytes stay unchanged. A name change also changes `Remember`'s
coordination key and can make existing webhook receipts invisible. See the
[namespace migration guidance](../../../cmd/arkoren/skills/arkoren/references/guides/cache/overview.md#choosing-names-and-separators).

See the [driver API reference](https://pkg.go.dev/arkoren.dev/driver/redis/rediscache).
