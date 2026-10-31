// Package namespace contains invalid consumers of typed flag namespaces.
// Tests compile this fixture only to verify static type rejection.
package namespace

import "arkoren.dev/core/service/feature"

// reject exercises namespace and leaf type mismatches before runtime.
func reject() {
	names := feature.NewNamespace("checkout", false)
	_ = feature.Namespace[int](feature.NewNamespace("checkout", false))
	var other feature.Namespace[int] = feature.NewNamespace("checkout", false)
	_ = other
	_ = feature.Flag[int](names.Flag("enabled"))
	var leaf feature.Flag[int] = names.Flag("enabled")
	_ = leaf
	_ = feature.NewNamespace("object", struct{}{})
	_ = feature.NewNamespace("unsigned", uint(1))
	_ = feature.NewNamespace("pointer", new(bool))
}
