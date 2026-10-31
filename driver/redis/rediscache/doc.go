// Package rediscache provides a Redis-backed cache Driver. It requires Redis
// 6.2 or later, because Pull uses GETDEL. Construction does no network I/O;
// Start checks connectivity and Stop closes the native client.
// CompareAndPut uses one atomic Lua execution to check an existing raw guard
// and write either that key or another key. Positive TTLs round up to whole
// milliseconds; zero means no expiry. Possibly executed guarded writes are
// not replayed after uncertain transport failures, even with native retries
// enabled. Atomicity does not guarantee Redis persistence or failover durability.
//
// New applies callbacks directly once in order on a fresh Builder. Nil entries
// panic; an expanded nil or empty callback slice makes no edits. NewFrom is a
// settings-free native conversion and Stop closes that client's connection too.
// Arrange one shutdown owner when sharing a client. TLS objects remain shared.
// New defaults to TCP at localhost:6379, database zero, empty credentials, and
// no TLS. Its client honors operation deadlines during socket I/O. NewFrom keeps
// native settings unchanged: enable ContextTimeoutEnabled on the supplied client
// before transferring ownership when request deadlines must bound socket I/O.
//
// TTL reads use millisecond precision. IncrementWithTTL preserves exact int64
// values and rounds positive windows up to milliseconds; an already-canceled
// operation wins over invalid TTL validation. CompareAndPut remains one atomic
// script and never replays a possibly executed guarded write after a lost reply.
// Counter overflow is an error without target/TTL mutation. Decrement handles
// MinInt64 subtraction with an atomic exact-decimal script, rather than asking
// native DECRBY to negate an argument it cannot represent.
//
// Example:
//
//	driver := rediscache.New(func(store *rediscache.Builder) {
//		store.Addr("localhost:6379")
//	})
//	store := cache.New(driver)
//	_ = store.Driver()
package rediscache
