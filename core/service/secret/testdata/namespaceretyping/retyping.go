// Package namespaceretyping deliberately fails compilation to verify that
// namespace and generated leaf decoding types cannot be reinterpreted.
package namespaceretyping

import "arkoren.dev/core/service/secret"

// invalid exercises direct assignments, conversions, and explicit Get type
// arguments. The parent test requires diagnostics for each retyping path.
func invalid(store *secret.Store) {
	_ = secret.Namespace[int](secret.NewNamespace[string]("prod", "/"))
	var credentials secret.Namespace[int] = secret.NewNamespace[string]("prod", "/")
	_ = credentials
	names := secret.NewNamespace[string]("prod", "/")
	_ = secret.Secret[int](names.Secret("token"))
	var definition secret.Secret[int] = names.Secret("token")
	_ = definition
	_, _ = store.Get[int](nil, names.Secret("token"))
}
