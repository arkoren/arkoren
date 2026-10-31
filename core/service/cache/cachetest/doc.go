// Package cachetest provides a conformance suite for [cache.Driver]
// implementations, modeled on testing/slogtest and testing/fstest.
//
// [Run] executes one subtest per documented behavior of the contract:
// missing keys, expiry, conditional writes, counters, copy semantics,
// cancellation and concurrency. A driver that passes is interchangeable with
// every other driver behind [cache.Cache].
// Guarded-write cases cover exact existing-byte comparison, same/different
// destinations, expiry, rejected-write immutability, ownership, and contention.
// Counter cases cover both int64 bounds, overflow nonmutation of value and TTL,
// missing-target absence, MinInt64 subtraction, and contention at the bound.
// Overflow errors keep their backend-native identities.
//
// Time-dependent subtests run inside a testing/synctest bubble, so drivers
// that read the clock with the time package pass in zero real time. Drivers
// whose expiry is measured by an external process, such as Redis, pass
// [WithExternalClock] and the suite sleeps in real time instead.
//
//	func TestDriver(t *testing.T) {
//		cachetest.Run(t, func(t *testing.T) cache.Driver {
//			return memorycache.New()
//		})
//	}
package cachetest
