# Arkoren Cache Memory Driver

`arkoren.dev/core/service/cache/memorycache` is an in-process implementation of `cache.Driver`
using only the standard library.

It is suitable for tests, local development, and single-process applications.

## Using the driver

From your application's Go module, run `go get arkoren.dev/core`. Import the
facade and driver, then use your operation's `context.Context` named `ctx`:

```go
import (
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

store := cache.New(memorycache.New())
report := cache.NewKey[string]("reports:v1:daily", func(entry *cache.KeyBuilder) {
	entry.TTL(time.Minute)
})
err := store.Put(ctx, report, "ready")
```

Handle `err` before continuing. `store.Get(ctx, report)`
then returns `"ready"` while the entry exists. State is not shared with another
driver or process and disappears when this driver is discarded.

Optional `cache.NewNamespace[T]` definitions join names with `":"` by default;
standalone key names remain exact. Preserve deployed dot joins with
`NamespaceBuilder.Separator(".")` before upgrading. Namespace composition is
facade behavior, not storage isolation or a driver naming transformation.

Expiry is checked on access, so correctness never depends on sweeping. Writes
also remove expired entries inline at most once per `Cleanup` (default one
minute), so abandoned keys, such as rate-limit windows, do not grow memory
without `Start`. `Start` additionally launches a janitor on the same interval;
`Stop` ends it. A negative `Builder.Cleanup` disables all sweeping.

Every context-aware operation first returns `ctx.Err()` when canceled, before
reading or changing cache state. `Put` and successful `Add` copy their byte
input, and `Get` returns a byte copy; subsequent caller mutation cannot alter
the cached value. A zero TTL has no expiration and a negative TTL is rejected
with `cache.ErrInvalidTTL`. `Increment` and `Decrement` start missing counters
from zero when the result fits `int64`. `Pull`, `TryLock`, `Unlock`, and
`ExtendLock` are atomic under the same mutex.

`Increment`, `Decrement`, and `IncrementWithTTL` reject mathematical results
outside `int64` without changing the target value or TTL. Missing targets remain
absent, and `IncrementWithTTL` does not add an expiry on overflow. These checks
and writes hold the same mutex. Memory overflow errors wrap `strconv.ErrRange`;
other drivers keep their native error identities. `Decrement` checks subtraction
directly: subtracting `math.MinInt64` from itself returns zero, but subtracting it
from zero fails without mutation.

See the [cache guide](../../../../cmd/arkoren/skills/arkoren/references/guides/cache/overview.md)
and [memory driver reference](https://pkg.go.dev/arkoren.dev/core/service/cache/memorycache).
