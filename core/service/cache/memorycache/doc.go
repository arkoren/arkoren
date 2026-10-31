// Package memorycache provides an in-memory [cache.Driver] for tests, local
// development, and single-process applications.
//
// Every context-aware operation returns ctx.Err() before reading or mutating
// state. Put and successful Add copy supplied byte slices, and Get returns a
// copy, so caller mutation cannot change cached state. A zero TTL does not
// expire.
// CompareAndPut compares an existing guard and writes its destination under
// one mutex, including when the keys differ. Its successful write copies the
// supplied bytes; a mismatch preserves the entries and their lifetimes.
// Counter lookup, checked arithmetic, and write also share one mutex. Results
// outside int64 wrap strconv.ErrRange without changing the target value or TTL
// or creating a missing entry. Decrement checks subtraction directly, so a
// MinInt64 delta works whenever the mathematical result is representable.
//
// New takes Builder policies for Cleanup. They run directly once in order on a
// fresh draft; nil entries panic and expanded nil/empty slices supply no edits.
// Zero cleanup resolves to one minute; negative disables sweeping. Publication
// detaches settings and retained drafts cannot change the driver's lifecycle.
// Construction starts no janitor; Start owns any background work.
//
// Example:
//
//	driver := memorycache.New(func(store *memorycache.Builder) {
//		store.Cleanup(-time.Second)
//	})
//	store := cache.New(driver)
//	_ = store.Driver()
package memorycache
