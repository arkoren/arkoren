// Package retyping contains intentionally invalid consumers of typed flags.
// The parent package compiles it only to assert compiler diagnostics.
package retyping

import (
	"context"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/memoryfeature"
)

// reject exercises type mismatches that must fail before runtime.
func reject() {
	driver := memoryfeature.New()
	flags := feature.New(driver)
	definition := feature.NewFlag("checkout", false)
	_ = feature.Flag[int](feature.NewFlag("checkout", false))
	var other feature.Flag[int] = feature.NewFlag("checkout", false)
	_ = other
	_, _ = flags.Value[int](context.Background(), definition, feature.Subject{})
	_, _ = flags.ValueDetail[int](context.Background(), definition, feature.Subject{})
	_ = driver.Set[int](definition, 1)
	_ = driver.SetFor[int](definition, "u1", 1)
	_ = feature.NewFlag("object", struct{}{})
}
