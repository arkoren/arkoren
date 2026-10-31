// Package retyping contains deliberately invalid typed secret consumers. The parent
// test compiles it explicitly and checks that type mismatches are rejected.
package retyping

import "arkoren.dev/core/service/secret"

// invalid exercises each public path that could otherwise reinterpret a shared
// secret definition. This package must never compile successfully.
func invalid(store *secret.Store) {
	_ = secret.Secret[int](secret.NewSecret[string]("same"))
	var definition secret.Secret[int] = secret.NewSecret[string]("same")
	_ = definition
	_, _ = store.Get[int](nil, secret.NewSecret[string]("same"))
}
