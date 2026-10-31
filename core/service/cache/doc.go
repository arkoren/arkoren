// Package cache provides a vendor-neutral cache contract and typed facade.
//
// Cache operations use immutable Key[T] definitions. NewKey[T] declares an exact
// standalone identity, including an explicit empty string. Keys support Derive
// and retain their first error. TTL changes write policy, never identity;
// zero means no expiry.
//
// NewNamespace[T] groups keys under a literal prefix, separator (default ":"),
// and inherited write TTL. Namespace.Key and Namespace.Subnamespace join bytes
// without escaping or normalization; a child separator override applies only
// to future joins. Namespaces are immutable and also support Derive.
// Applications retaining keys created with the former dot default must set
// NamespaceBuilder.Separator(".") explicitly. There is no fallback lookup or
// automatic migration: choosing the new default addresses different entries.
// Literal prefix bytes and standalone NewKey identities are unchanged.
// Typed-key operations validate cancellation before definition errors, encoding,
// or I/O. Lease acquisition instead validates its literal name and positive TTL
// before asking the driver; invalid lease arguments never perform driver I/O.
// Name explicitly reveals identity; ordinary formatting and logging redact it.
// Counters use Key[int64] and their distinct driver expiry rules. Locks and the
// raw Driver continue to use opaque strings.
// Increment, Decrement, and IncrementWithTTL reject mathematical results outside
// int64 atomically, without changing the target value or TTL. Missing targets
// remain absent. Subtraction accepts MinInt64 as a delta when its mathematical
// result fits; overflow error identities remain backend-native.
// Reserve physical names beginning with "lock:" for facade leases, including
// Remember's internal locks. This is a naming convention in the same driver key
// space, not backend isolation or escaping. An ordinary Put to "lock:report"
// can overwrite the lease named "report". Also reserve logical lease names
// beginning with "remember:" for Remember's cache-miss coordination.
//
// CompareAndPut checks one existing raw Match and writes a typed destination
// atomically, using its key's TTL. The guard and destination may be the same
// key or different keys. Missing, expired, or changed guards reject the write;
// this is resource-level fencing, unlike an expiring cache lease. Read exact
// guard bytes through Driver.Get rather than re-encoding a decoded value.
// Matching bytes are not a monotonic version: restoring an earlier guard can
// permit an earlier writer again. Network errors may leave writes uncertain.
// Raw Match and Entry descriptors intentionally expose names and bytes; do not
// log them as if they were redacted Key definitions. Their slices are borrowed
// during a call, not retained afterwards. Ordinary JSON Put/Get detach cached
// data from caller values; Remember returns its computed value without cloning
// that application-owned result. Best-effort Remember lease release has an
// independent ten-second context budget and falls back to lease expiry on error.
//
// New takes Builder policies for Telemetry and Name, invoked directly once in
// order on a fresh draft. Nil entries panic; expanded nil/empty slices supply
// no edits. Publication detaches settings, not the driver or telemetry service;
// retaining a draft does not permit reconfiguration. Nil telemetry resolves the
// process default per operation, including changes after construction.
//
// Each facade cache operation emits one client span and the
// arkoren.cache.operations and arkoren.cache.operation.duration metrics when
// the selected telemetry (default: [telemetry.Default]) is started. Telemetry
// records the operation, store name ([Builder.Name]), and hit or miss, never keys,
// values, or error text.
// Driver and DriverAs are untraced accessors; Lease.Unlock and Lease.Extend
// are also untraced. Direct driver calls bypass facade instrumentation. A single
// facade operation can include multiple driver calls; native client transport
// retries are not additional facade operations or proof of exactly-once effects.
//
// Example:
//
//	store := cache.New(driver, func(store *cache.Builder) {
//		store.Name("sessions")
//	})
//	_ = store.Driver()
//	version := cache.NewKey[string]("application.version", func(entry *cache.KeyBuilder) {
//		entry.TTL(time.Minute)
//	})
//	if err := store.Put(ctx, version, "v1"); err != nil {
//		return err
//	}
//	users := cache.NewNamespace[string]("users")
//	_ = users.Key("42") // users:42
package cache
