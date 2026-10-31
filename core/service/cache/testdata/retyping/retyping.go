// Package retyping intentionally fails compilation to protect typed identities.
package retyping

import "arkoren.dev/core/service/cache"

// convertKey must fail: changing the JSON type requires a new declaration.
var convertKey = cache.Key[int](cache.NewKey[string]("same"))

// convertSpace must fail even though both definitions hold the same policy.
var convertSpace = cache.Namespace[int](cache.NewNamespace[string]("same"))

// assignKey must fail without an explicit conversion too.
var assignKey cache.Key[int] = cache.NewKey[string]("same")

// wrongValue must fail inference rather than weaken the key's declared type.
func wrongValue(store *cache.Cache) {
	_ = store.Put(nil, cache.NewKey[string]("same"), 42)
}

// wrongGet must fail when an explicit output type disagrees with the key.
func wrongGet(store *cache.Cache) {
	_, _ = store.Get[int](nil, cache.NewKey[string]("same"))
}

// wrongPutKey must fail even when an explicit type argument matches the value.
func wrongPutKey(store *cache.Cache) {
	_ = store.Put[int](nil, cache.NewKey[string]("same"), 42)
}

// wrongCounters must fail because counters require the exact int64 key type.
func wrongCounters(store *cache.Cache) {
	key := cache.NewKey[string]("same")
	_, _ = store.Increment(nil, key, 1)
	_, _ = store.Decrement(nil, key, 1)
	_, _, _ = store.IncrementWithTTL(nil, key, 1, 1)
}

// rawMetadata must fail because metadata operations infer T from a typed key,
// not from a raw string identity.
func rawMetadata(store *cache.Cache) {
	_, _ = store.Has(nil, "raw-has")
	_ = store.Delete(nil, "raw-delete")
	_, _ = store.TTL(nil, "raw-ttl")
}
