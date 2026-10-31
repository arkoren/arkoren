// Package storagetest provides a conformance suite for [storage.Driver]
// implementations, modeled on testing/slogtest and testing/fstest.
//
// [Run] executes one subtest per documented behavior of the contract:
// round trips, missing objects, atomic replacement, deletion, key validation,
// prefix listing and pagination, signed URLs, cancellation and concurrent
// writes. Drivers that cannot sign URLs pass [WithoutSignURL]. A driver
// that passes is interchangeable with every other driver behind
// [storage.Storage].
//
// Every subtest works under its own unique key prefix and removes what it
// created, so the suite is safe to run against a shared bucket or directory.
//
//	func TestDriver(t *testing.T) {
//		storagetest.Run(t, func(t *testing.T) storage.Driver {
//			return memorystorage.New()
//		}, storagetest.WithoutSignURL())
//	}
package storagetest
