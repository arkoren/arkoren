package memorybroadcast

import "testing"

// TestBuilderZeroLimitNormalizesAfterPolicies preserves the existing nonpositive
// concurrency default without starting workers during construction.
func TestBuilderZeroLimitNormalizesAfterPolicies(t *testing.T) {
	t.Parallel()

	driver := New(func(broker *Builder) {
		broker.MaxConcurrentDeliveries(0)
	})

	if cap(driver.limit) != DefaultMaxConcurrentDeliveries {
		t.Fatalf("delivery limit = %d", cap(driver.limit))
	}
}

// TestBuilderNegativeLimitNormalizesAfterPolicies preserves the same default
// for negative limits, rather than disabling or unbounding delivery concurrency.
func TestBuilderNegativeLimitNormalizesAfterPolicies(t *testing.T) {
	t.Parallel()

	driver := New(func(broker *Builder) {
		broker.MaxConcurrentDeliveries(-1)
	})

	if cap(driver.limit) != DefaultMaxConcurrentDeliveries {
		t.Fatalf("delivery limit = %d", cap(driver.limit))
	}
}

// TestBuilderPoliciesPublishDetachedSettings verifies callback ordering and
// identity without starting a runner or invoking delivery handlers.
func TestBuilderPoliciesPublishDetachedSettings(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	driver := New(func(broker *Builder) {
		retained = broker
		calls++

		if broker.MaxConcurrentDeliveries(1) != broker {
			t.Fatal("setting returned another draft")
		}
	}, func(broker *Builder) {
		calls++

		if broker != retained {
			t.Fatal("policies received different drafts")
		}

		broker.MaxConcurrentDeliveries(2)
	})
	retained.MaxConcurrentDeliveries(3)

	if calls != 2 || cap(driver.limit) != 2 {
		t.Fatalf("calls = %d, delivery capacity = %d", calls, cap(driver.limit))
	}
}

// TestMemoryBroadcastPoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestMemoryBroadcastPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	component := New(func(broker *Builder) {
		calls++
		retained = broker
	}, func(broker *Builder) {
		if calls != 1 || retained != broker {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	New(func(broker *Builder) {
		if retained == broker {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestMemoryBroadcastNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestMemoryBroadcastNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*Builder)
	empty := []func(*Builder){}

	if New(absent...) == nil || New(empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestMemoryBroadcastNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestMemoryBroadcastNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		New(nil, func(broker *Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestMemoryBroadcastPolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestMemoryBroadcastPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		New(func(broker *Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestMemoryBroadcastMaxConcurrentDeliveriesReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMemoryBroadcastMaxConcurrentDeliveriesReturnsDraft(t *testing.T) {
	t.Parallel()

	broadcast := &Builder{}

	if broadcast.MaxConcurrentDeliveries(4) != broadcast {
		t.Fatal("setting returned a different draft")
	}
}

// TestMemoryBroadcastLoggerReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMemoryBroadcastLoggerReturnsDraft(t *testing.T) {
	t.Parallel()

	broadcast := &Builder{}

	if broadcast.Logger(nil) != broadcast {
		t.Fatal("setting returned a different draft")
	}
}
