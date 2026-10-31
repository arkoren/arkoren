package unwrap_test

import (
	"testing"

	"arkoren.dev/core/internal/unwrap"
)

// driver is a minimal driver contract.
type driver interface {
	// Name identifies the concrete driver behind any wrappers.
	Name() string
}

// base is the concrete driver.
type base struct{}

// Name identifies base.
func (base) Name() string {
	return "base"
}

// wrapper wraps another driver.
type wrapper struct {
	// inner is the next driver exposed by Unwrap.
	inner driver
}

// Name identifies wrapper.
func (wrapper) Name() string {
	return "wrapper"
}

// Unwrap exposes the wrapped driver.
func (wrapped wrapper) Unwrap() driver {
	return wrapped.inner
}

// TestAsFindsWrappedDriver checks traversal through nested driver wrappers.
// Matching must respect the concrete value type rather than invent a pointer.
func TestAsFindsWrappedDriver(t *testing.T) {
	var d driver = wrapper{inner: wrapper{inner: base{}}}

	if _, ok := unwrap.As[base](d); !ok {
		t.Fatal("base not found through wrappers")
	}

	if _, ok := unwrap.As[*base](d); ok {
		t.Fatal("unexpected match")
	}
}
